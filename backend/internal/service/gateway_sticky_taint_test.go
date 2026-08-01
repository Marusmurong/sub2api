//go:build unit

package service

import (
	"context"
	"testing"
	"time"
)

type taintRecorder struct {
	StubLikeCache
	deleted []string
	tainted []string
}

func (c *taintRecorder) DeleteSessionAccountID(_ context.Context, groupID int64, sessionHash string) error {
	c.deleted = append(c.deleted, sessionHash)
	return nil
}

func (c *taintRecorder) MarkSignatureTainted(_ context.Context, groupID int64, sessionHash string) error {
	c.tainted = append(c.tainted, sessionHash)
	return nil
}

// 账号变为不可用而清理粘性绑定时，必须同时把该会话标为「签名已污染」。
//
// 清绑定意味着下一轮必然换号，而客户端手里的 thinking 历史是旧账号签发的——
// 不标记的话，我们要先盲发一个必然被 400 拒绝的请求，才靠那次失败学会剥离。
// 生产实测：账号被吊销后，其上的会话正是以「每个会话一次 400」的方式重新学习的。
func TestClearStickyAlsoMarksTainted(t *testing.T) {
	c := &taintRecorder{}
	s := &GatewayService{cache: c}
	gid := int64(28)

	s.clearStickyAndMarkTainted(context.Background(), gid, "sess-abc")

	if len(c.deleted) != 1 || c.deleted[0] != "sess-abc" {
		t.Errorf("应删除粘性绑定, got %v", c.deleted)
	}
	if len(c.tainted) != 1 || c.tainted[0] != "sess-abc" {
		t.Errorf("应同时标记污染, got %v", c.tainted)
	}
}

// 空会话标识不得触发任何写入。
func TestClearStickyIgnoresEmptySession(t *testing.T) {
	c := &taintRecorder{}
	s := &GatewayService{cache: c}
	s.clearStickyAndMarkTainted(context.Background(), 28, "")
	if len(c.deleted) != 0 || len(c.tainted) != 0 {
		t.Errorf("空会话不应产生写入: deleted=%v tainted=%v", c.deleted, c.tainted)
	}
}

// StubLikeCache 提供 GatewayCache 其余方法的空实现。
type StubLikeCache struct{}

func (StubLikeCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, nil
}
func (StubLikeCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}
func (StubLikeCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (StubLikeCache) DeleteSessionAccountID(context.Context, int64, string) error { return nil }
func (StubLikeCache) MarkSignatureTainted(context.Context, int64, string) error   { return nil }
func (StubLikeCache) IsSignatureTainted(context.Context, int64, string) bool      { return false }
func (StubLikeCache) GetSignatureOwnerAccountID(context.Context, int64, string) (int64, error) {
	return 0, nil
}
