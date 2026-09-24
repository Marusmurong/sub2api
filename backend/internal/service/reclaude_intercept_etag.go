package service

import "sync"

// ReclaudeInterceptETagStore 记住每台设备的拦截清单版本（ETag）。
//
// 🔴 为什么需要：真客户端把 intercept.Snapshot.Version（形如 "0:0"）持久化，
// 之后每次 GET /client/intercept-domains 都带 If-None-Match —— 第一次 200，
// 此后一整天都是 304（✅ 真值抓包确认）。
//
// 我们此前从不带这个头，每 60 秒强制一次全量 200。那是一条**每分钟重复、
// 跨设备完全一致**的信号：一台"清单永远在变"的设备，而真设备永远是 304。
//
// 存内存而非落库：版本丢了最坏结果是多一次 200，下一轮就恢复；
// 而为它加一张表会把一个可观测性修补变成 schema 变更。
type ReclaudeInterceptETagStore struct {
	mu   sync.RWMutex
	tags map[int64]string
}

// NewReclaudeInterceptETagStore 构造存储。
func NewReclaudeInterceptETagStore() *ReclaudeInterceptETagStore {
	return &ReclaudeInterceptETagStore{tags: map[int64]string{}}
}

// Get 取该账号已知的 ETag；没有时返回 false。
//
// 🔴 没有就不带这个头 —— 凭空编一个版本号只会与服务端记录对不上，
// 比不带更可疑。
func (s *ReclaudeInterceptETagStore) Get(accountID int64) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.tags[accountID]
	return value, ok && value != ""
}

// Remember 记下响应里的 ETag。
//
// 🔴 空值不覆盖：304 响应通常不重复带 ETag，此时清掉已记住的版本会让
// 下一轮退回全量 200 —— 正是我们要消除的那个特征。
func (s *ReclaudeInterceptETagStore) Remember(accountID int64, etag string) {
	if s == nil || etag == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tags[accountID] = etag
}
