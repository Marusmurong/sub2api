package service

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// 本文件实现 billing header 的 cc_prev_req（parent-link）。
//
// 真实 2.1.220 的取值（函数 r1_，见 docs/CC_2.1.220_EGRESS_SPEC.md）：
//
//	function r1_(e){
//	  for (let t = e.length-1; t >= 0; t--) {
//	    let r = e[t];
//	    if (r.type === "assistant" && r.requestId) return r.requestId
//	  }
//	}
//
// 即 transcript 里**最近一条**带 requestId 的 assistant 条目——上一轮响应的 id，
// 而非会话首轮的 id。发送条件是 provider=firstParty 且 baseURL 为首方（k7n 里的
// `r && i==="firstParty" && Yd()`）。
//
// requestId 是客户端 transcript 上的字段，API body 里不存在，因此网关只能自己
// 记住上一轮从上游拿到的 request id。

// ccPrevReqTTL 是 parent-link 的保留时长。
// 取值略大于常见的会话空闲间隔即可：过期只会退化成"本轮无 parent-link"，
// 与会话首轮同形，不会产生错误链接。
const ccPrevReqTTL = 2 * time.Hour

// ccPrevReqSessionIDKey 在 gin.Context 上暂存本次请求的会话标识，
// 供响应侧落存时复用，避免在响应侧重复解析 body。
const ccPrevReqSessionIDKey = "cc_prev_req_session_id"

// upstreamRequestIDPattern 限定可回填的 request id 形态。
//
// 该值来自上游响应头，最终会被拼进 system 块的文本里，属于跨信任边界的数据；
// 用白名单字符集挡住任何可能改变 block 结构的内容（分号、换行等）。
var upstreamRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// PrevRequestIDStore 存取会话在某账号上的上一轮 upstream request id。
//
// 独立于 GatewayCache：后者被大量测试桩实现，往其中加方法会波及无关测试。
// 实现方为 repository.gatewayCache（那里有编译期断言）。
type PrevRequestIDStore interface {
	GetPrevRequestID(ctx context.Context, accountID int64, sessionID string) (string, error)
	SetPrevRequestID(ctx context.Context, accountID int64, sessionID, requestID string, ttl time.Duration) error
}

// prevRequestIDStore 在缓存实现未提供该能力时返回 nil，能力静默降级关闭。
func (s *GatewayService) prevRequestIDStore() PrevRequestIDStore {
	if s == nil || s.cache == nil {
		return nil
	}
	store, ok := s.cache.(PrevRequestIDStore)
	if !ok {
		return nil
	}
	return store
}

// upstreamRequestID 读取上游响应的 request id。
//
// Anthropic 返回的是 request-id；x-request-id 是部分网关补的别名。此前全仓只读
// x-request-id，生产实测恒为空——既拿不到 id 回传给下游，出问题时也无法凭 id 向
// 上游报障。读法与 openai_embeddings 对齐。
func upstreamRequestID(h http.Header) string {
	if h == nil {
		return ""
	}
	if v := h.Get("request-id"); v != "" {
		return v
	}
	return h.Get("x-request-id")
}

// ccPrevReqSessionID 从最终 body 的 metadata.user_id 中取会话标识。
//
// 该字段在两条路径上都存在且跨轮稳定：透传路径是真实 Claude Code 的进程级
// session_id；mimic 路径是 buildOAuthMetadataUserID 按会话级种子派生的。
// 取不到时返回空串，调用方据此跳过 parent-link（等同于会话首轮）。
func ccPrevReqSessionID(body []byte) string {
	uid := gjson.GetBytes(body, "metadata.user_id").String()
	if uid == "" {
		return ""
	}
	parsed := ParseMetadataUserID(uid)
	if parsed == nil {
		return ""
	}
	return parsed.SessionID
}

// lookupPrevRequestID 取本会话在**本账号**上的上一轮 upstream request id。
//
// key 必须包含 accountID。换号（failover）后拿到的 id 是别的账号产生的，发上去
// 就是一条可被上游证伪的假 parent-link——那比缺字段更糟。换号即 miss，语义等同
// 于"会话首轮"，是安全的降级。
func (s *GatewayService) lookupPrevRequestID(ctx context.Context, account *Account, sessionID string) string {
	store := s.prevRequestIDStore()
	if store == nil || account == nil || sessionID == "" {
		return ""
	}
	id, err := store.GetPrevRequestID(ctx, account.ID, sessionID)
	if err != nil || !upstreamRequestIDPattern.MatchString(id) {
		return ""
	}
	return id
}

// rememberUpstreamRequestID 在上游接受本轮请求后记录 request id，供下一轮作 parent-link。
//
// 只在成功路径调用：失败重试若也写入，会用一个上游并未真正应答的 id 覆盖掉上一轮
// 的有效链接。
func (s *GatewayService) rememberUpstreamRequestID(ctx context.Context, c *gin.Context, account *Account, h http.Header) {
	store := s.prevRequestIDStore()
	if store == nil || account == nil || c == nil {
		return
	}

	requestID := upstreamRequestID(h)
	if !upstreamRequestIDPattern.MatchString(requestID) {
		return
	}

	sessionID, _ := c.Get(ccPrevReqSessionIDKey)
	sid, _ := sessionID.(string)
	if sid == "" {
		return
	}

	_ = store.SetPrevRequestID(ctx, account.ID, sid, requestID, ccPrevReqTTL)
}
