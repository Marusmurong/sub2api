package service

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReclaudeCountTokensQuota(t *testing.T) {
	account, _ := reclaudeTestAccount(t)

	t.Run("成功的 count_tokens 记一次调用与保守 token", func(t *testing.T) {
		// 🔴 count_tokens 不产生 usage ⇒ 不进 RecordUsage ⇒ 按 0 计就是**看不见的**
		// 配额消耗。文档点名它是重试放大之外的第二个「配额暗漏」。
		store := &fakeReclaudeUsageStore{}
		gateway := &GatewayService{reclaudeQuota: NewReclaudeQuotaGate(store)}

		gateway.recordReclaudeCountTokensCall(context.Background(), account)

		require.Len(t, store.deltas, 1)
		require.Equal(t, int64(1), store.deltas[0].UpstreamCalls)
		require.Equal(t, ReclaudeCountTokensTokenEstimate, store.deltas[0].Tokens)
	})

	t.Run("非 reclaude 账号不记", func(t *testing.T) {
		store := &fakeReclaudeUsageStore{}
		gateway := &GatewayService{reclaudeQuota: NewReclaudeQuotaGate(store)}

		gateway.recordReclaudeCountTokensCall(context.Background(),
			&Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth})

		require.Empty(t, store.deltas)
	})

	t.Run("缺件时不 panic", func(t *testing.T) {
		var gateway *GatewayService
		gateway.recordReclaudeCountTokensCall(context.Background(), account)
		(&GatewayService{}).recordReclaudeCountTokensCall(context.Background(), account)
	})
}

// 结构性护栏：count_tokens 的上游调用必须全部走 doCountTokensUpstream。
//
// 与 TestDoWithTLSIsFunneledThroughDispatch 同源的理由：这里有三个调用点
// （主路径 + 签名纠错重试 + 自定义 base URL 路径），漏掉任何一个，
// 那条路径上的 count_tokens 就从配额水位里消失 —— 而它的失败方式是**静默低估**，
// 不会有任何报错。
func TestCountTokensUpstreamIsFunneled(t *testing.T) {
	content, err := os.ReadFile(filepath.Clean("gateway_count_tokens.go"))
	require.NoError(t, err)

	var violations []string
	for i, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(line, "s.doUpstream(") {
			violations = append(violations, "gateway_count_tokens.go:"+strconv.Itoa(i+1)+": "+trimmed)
		}
	}

	require.Emptyf(t, violations,
		"以下 count_tokens 调用点绕过了配额记账，这条路径上的请求配额消耗会静默消失：\n%s",
		strings.Join(violations, "\n"))
}
