package service

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
)

// ReclaudeSelfCheckRunner 是「测试账号」按钮需要的最小面。
//
// 收窄成接口而不是直接依赖 *ReclaudeSelfChecker：测试路径只需要「跑一次、
// 拿结果」，而自检器本身还牵着仓储、探针、转发器三个依赖。
type ReclaudeSelfCheckRunner interface {
	Run(ctx context.Context, accountID int64) (*ReclaudeSelfCheckResult, error)
}

// SetReclaudeSelfChecker 注入自检器；未注入时测试按钮如实报「未配置」。
func (s *AccountTestService) SetReclaudeSelfChecker(checker ReclaudeSelfCheckRunner) {
	if s != nil {
		s.reclaudeSelfChecker = checker
	}
}

// testReclaudeAccountConnection 把 reclaude 的三步自检渲染成测试面板的 SSE。
//
// 🔴 为什么不走通用 Claude 测试路径：那条路径直接拿 access_token 打
// api.anthropic.com/v1/messages，而 reclaude 的请求必须封成信封
// （uint32BE(len(meta)) ++ meta ++ body）、带 ed25519 设备签名、打对方的
// route 节点。走错路径的结果是必然 401，而且白白消耗一次对方的请求配额。
//
// 之前这里返回 "Unsupported account type: reclaude" —— 号建好了却没有任何
// 办法验证它能不能用。
func (s *AccountTestService) testReclaudeAccountConnection(c *gin.Context, account *Account) error {
	s.prepareReclaudeTestSSE(c)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: "reclaude self-check"})

	if s == nil || s.reclaudeSelfChecker == nil {
		return s.sendErrorAndEnd(c, "reclaude self-check is not configured")
	}

	result, err := s.reclaudeSelfChecker.Run(c.Request.Context(), account.ID)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("reclaude self-check failed: %s", err.Error()))
	}

	// 逐步汇报：三步的排查方向完全不同 —— 网关不可达查代理，凭据无效查设备
	// 是否已在 rec 后台被删，信封失败查签名。只说「失败」等于没说。
	for _, step := range result.Steps {
		s.sendEvent(c, TestEvent{Type: "content", Text: formatReclaudeSelfCheckStep(step)})
	}

	if result.BoundEmail != "" {
		s.sendEvent(c, TestEvent{Type: "content", Text: "bound account: " + result.BoundEmail})
	}

	if !result.Passed {
		return s.sendErrorAndEnd(c, "reclaude self-check did not pass")
	}

	if result.Activated {
		s.sendEvent(c, TestEvent{Type: "content", Text: "account activated for scheduling"})
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func formatReclaudeSelfCheckStep(step ReclaudeSelfCheckStep) string {
	if step.OK {
		return "✓ " + step.Name
	}
	if step.Detail == "" {
		return "✗ " + step.Name
	}
	return "✗ " + step.Name + ": " + step.Detail
}

func (s *AccountTestService) prepareReclaudeTestSSE(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()
}
