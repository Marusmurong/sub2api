//go:build integration

package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type GatewayCacheSuite struct {
	IntegrationRedisSuite
	cache service.GatewayCache
}

func (s *GatewayCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewGatewayCache(s.rdb)
}

func (s *GatewayCacheSuite) TestGetSessionAccountID_Missing() {
	_, err := s.cache.GetSessionAccountID(s.ctx, 1, "nonexistent")
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil for missing session")
}

func (s *GatewayCacheSuite) TestSetAndGetSessionAccountID() {
	sessionID := "s1"
	accountID := int64(99)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, sessionTTL), "SetSessionAccountID")

	sid, err := s.cache.GetSessionAccountID(s.ctx, groupID, sessionID)
	require.NoError(s.T(), err, "GetSessionAccountID")
	require.Equal(s.T(), accountID, sid, "session id mismatch")
}

func (s *GatewayCacheSuite) TestSessionAccountID_TTL() {
	sessionID := "s2"
	accountID := int64(100)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, sessionTTL), "SetSessionAccountID")

	sessionKey := buildSessionKey(groupID, sessionID)
	ttl, err := s.rdb.TTL(s.ctx, sessionKey).Result()
	require.NoError(s.T(), err, "TTL sessionKey after Set")
	s.AssertTTLWithin(ttl, 1*time.Second, sessionTTL)
}

func (s *GatewayCacheSuite) TestRefreshSessionTTL() {
	sessionID := "s3"
	accountID := int64(101)
	groupID := int64(1)
	initialTTL := 1 * time.Minute
	refreshTTL := 3 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, initialTTL), "SetSessionAccountID")

	require.NoError(s.T(), s.cache.RefreshSessionTTL(s.ctx, groupID, sessionID, refreshTTL), "RefreshSessionTTL")

	sessionKey := buildSessionKey(groupID, sessionID)
	ttl, err := s.rdb.TTL(s.ctx, sessionKey).Result()
	require.NoError(s.T(), err, "TTL after Refresh")
	s.AssertTTLWithin(ttl, 1*time.Second, refreshTTL)
}

func (s *GatewayCacheSuite) TestRefreshSessionTTL_MissingKey() {
	// RefreshSessionTTL on a missing key should not error (no-op)
	err := s.cache.RefreshSessionTTL(s.ctx, 1, "missing-session", 1*time.Minute)
	require.NoError(s.T(), err, "RefreshSessionTTL on missing key should not error")
}

func (s *GatewayCacheSuite) TestDeleteSessionAccountID() {
	sessionID := "openai:s4"
	accountID := int64(102)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, accountID, sessionTTL), "SetSessionAccountID")
	require.NoError(s.T(), s.cache.DeleteSessionAccountID(s.ctx, groupID, sessionID), "DeleteSessionAccountID")

	_, err := s.cache.GetSessionAccountID(s.ctx, groupID, sessionID)
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil after delete")
}

func (s *GatewayCacheSuite) TestGetSessionAccountID_CorruptedValue() {
	sessionID := "corrupted"
	groupID := int64(1)
	sessionKey := buildSessionKey(groupID, sessionID)

	// Set a non-integer value
	require.NoError(s.T(), s.rdb.Set(s.ctx, sessionKey, "not-a-number", 1*time.Minute).Err(), "Set invalid value")

	_, err := s.cache.GetSessionAccountID(s.ctx, groupID, sessionID)
	require.Error(s.T(), err, "expected error for corrupted value")
	require.False(s.T(), errors.Is(err, redis.Nil), "expected parsing error, not redis.Nil")
}

// 签名归属记录必须比粘性绑定活得久。
//
// DeleteSessionAccountID 被调用的时刻正是账号 429/被驱逐、下一轮必然换号的时刻；
// 若归属记录跟着一起没了，下一轮就只能盲发一个带着旧账号签名、必然被上游 400 拒绝
// 的请求（生产实测每天 175 次，其中 16 次重试预算耗尽后直接漏给客户端）。
func (s *GatewayCacheSuite) TestSignatureOwner_SurvivesSessionDelete() {
	const sessionID = "sig-owner-survives"
	const groupID = int64(7)

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, 42, 1*time.Minute))

	owner, err := s.cache.GetSignatureOwnerAccountID(s.ctx, groupID, sessionID)
	require.NoError(s.T(), err, "绑定时应同步写入签名归属")
	require.Equal(s.T(), int64(42), owner)

	require.NoError(s.T(), s.cache.DeleteSessionAccountID(s.ctx, groupID, sessionID))

	_, err = s.cache.GetSessionAccountID(s.ctx, groupID, sessionID)
	require.True(s.T(), errors.Is(err, redis.Nil), "路由绑定应已删除")

	owner, err = s.cache.GetSignatureOwnerAccountID(s.ctx, groupID, sessionID)
	require.NoError(s.T(), err, "签名归属不得被一并删除")
	require.Equal(s.T(), int64(42), owner, "删绑定后仍须知道上一轮是哪个账号签的名")
}

// 归属记录跟随最新绑定更新：会话换到新账号后，历史签名的归属也随之变成新账号。
func (s *GatewayCacheSuite) TestSignatureOwner_FollowsLatestBinding() {
	const sessionID = "sig-owner-rebind"
	const groupID = int64(7)

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, 42, 1*time.Minute))
	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, 43, 1*time.Minute))

	owner, err := s.cache.GetSignatureOwnerAccountID(s.ctx, groupID, sessionID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(43), owner)
}

// 无记录时返回 redis.Nil，由上层判定为"无归属"。
func (s *GatewayCacheSuite) TestSignatureOwner_Missing() {
	_, err := s.cache.GetSignatureOwnerAccountID(s.ctx, 7, "never-bound")
	require.True(s.T(), errors.Is(err, redis.Nil))
}

// 「签名已污染」标记：一旦置位就保持，且不受粘性绑定删除影响。
// 上游校验历史里的每一个 thinking 块，一个换过账号的对话即使回到同一个账号，
// 历史深处仍混着别的账号的签名——这一位就是记住这个不可逆事实。
func (s *GatewayCacheSuite) TestSignatureTainted_PersistsAcrossSessionDelete() {
	const sessionID = "tainted-persists"
	const groupID = int64(9)

	require.False(s.T(), s.cache.IsSignatureTainted(s.ctx, groupID, sessionID), "初始应为未污染")

	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, 42, 1*time.Minute))
	require.NoError(s.T(), s.cache.MarkSignatureTainted(s.ctx, groupID, sessionID))
	require.True(s.T(), s.cache.IsSignatureTainted(s.ctx, groupID, sessionID))

	require.NoError(s.T(), s.cache.DeleteSessionAccountID(s.ctx, groupID, sessionID))
	require.True(s.T(), s.cache.IsSignatureTainted(s.ctx, groupID, sessionID), "删除路由绑定不得清除污染标记")

	// 重新绑定到新账号同样不得清除：历史里的旧签名不会因为换绑就消失。
	require.NoError(s.T(), s.cache.SetSessionAccountID(s.ctx, groupID, sessionID, 43, 1*time.Minute))
	require.True(s.T(), s.cache.IsSignatureTainted(s.ctx, groupID, sessionID), "换绑不得清除污染标记")
}

// 重复置位是幂等的。
func (s *GatewayCacheSuite) TestSignatureTainted_Idempotent() {
	const sessionID = "tainted-idem"
	require.NoError(s.T(), s.cache.MarkSignatureTainted(s.ctx, 9, sessionID))
	require.NoError(s.T(), s.cache.MarkSignatureTainted(s.ctx, 9, sessionID))
	require.True(s.T(), s.cache.IsSignatureTainted(s.ctx, 9, sessionID))
}

func TestGatewayCacheSuite(t *testing.T) {
	suite.Run(t, new(GatewayCacheSuite))
}
