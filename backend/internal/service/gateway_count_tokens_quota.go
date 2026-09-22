package service

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// doCountTokensUpstream 是 count_tokens 的转发收口：在 doUpstream 之上补一次
// 配额记账。
//
// 🔴 为什么不能直接用 doUpstream：推理路径上成功调用的 token 由
// GatewayService.RecordUsage 记，而 count_tokens **不产生 usage**、根本走不到
// 那里。不在这里记，这条路径的配额消耗就从水位里彻底消失。
//
// 记账在**读响应之前**：请求已经出网，对方的配额已经扣了，读 body 失败与否
// 都不改变这个事实。
func (s *GatewayService) doCountTokensUpstream(
	req *http.Request, proxyURL string, account *Account, profile *tlsfingerprint.Profile,
) (*http.Response, error) {
	resp, err := s.doUpstream(req, proxyURL, account, profile)
	if err == nil {
		s.recordReclaudeCountTokensCall(req.Context(), account)
	}
	return resp, err
}
