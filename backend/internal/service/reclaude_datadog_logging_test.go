//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildReclaudeDatadogBatch(t *testing.T) {
	t.Run("单事件且发 tengu_started（可如实填的会话启动事件）", func(t *testing.T) {
		// 🔴 红线：不发 tengu_feature_ok（CLI 子系统事件）；也不发 tengu_api_success
		// （带 40+ 个拿不到真值的推理内部度量 → 填即矛盾）。tengu_started 字段全可如实填。
		batch := BuildReclaudeDatadogBatch(eventContext(t))
		require.Len(t, batch, 1)
		require.Equal(t, "tengu_started", batch[0].Message)
		// swe_bench_* / tmux / worktree 如实填空/false（我们不是那些环境）。
		require.Equal(t, "", batch[0].SweBenchRunID)
		require.False(t, batch[0].InTmuxWorktree)
		require.False(t, batch[0].TmuxFlag)
		require.False(t, batch[0].WorktreeFlag)
	})

	t.Run("入口画像钉 cli 且 is_interactive 是字符串 true", func(t *testing.T) {
		e := BuildReclaudeDatadogBatch(eventContext(t))[0]
		require.Equal(t, "cli", e.Entrypoint)
		require.Equal(t, "cli", e.ClientType)
		// 🔴 真值 is_interactive 是字符串 "true"，不是 bool true。
		require.Equal(t, "true", e.IsInteractive)
		require.True(t, e.IsClaudeAIAuth)
	})

	t.Run("定值字段对齐真值", func(t *testing.T) {
		e := BuildReclaudeDatadogBatch(eventContext(t))[0]
		require.Equal(t, "nodejs", e.DDSource)
		require.Equal(t, "claude-code", e.Service)
		require.Equal(t, "claude-code", e.Hostname)
		require.Equal(t, "external", e.Env)
		require.Equal(t, "external", e.UserType)
		require.Equal(t, "git", e.VCS)
		require.True(t, e.IsRunningWithBun)
	})

	t.Run("env 主机指纹取自账号真机快照（与 event_logging 同源）", func(t *testing.T) {
		e := BuildReclaudeDatadogBatch(eventContext(t))[0]
		// 与 eventAccount 里的 machine_env 快照一致。
		require.Equal(t, "x64", e.Arch)
		require.Equal(t, "v26.3.0", e.NodeVersion)
		require.Equal(t, "bash", e.Shell)
		require.Equal(t, "ubuntu", e.LinuxDistroID)
		require.Equal(t, "24.04", e.LinuxDistroVersion)
		require.Equal(t, "6.17.0-1017-aws", e.LinuxKernel)
		require.Equal(t, "linux", e.Platform)
		require.Equal(t, "2.1.280", e.Version)

		// 🔴 两路遥测严格同源：Datadog 的平铺字段必须与 event_logging 的 env 块一致。
		el := buildReclaudeEventEnv(eventContext(t).Account)
		require.Equal(t, el.Arch, e.Arch)
		require.Equal(t, el.NodeVersion, e.NodeVersion)
		require.Equal(t, el.LinuxKernel, e.LinuxKernel)
		require.Equal(t, el.Shell, e.Shell)
		require.Equal(t, el.Terminal, e.Terminal)
		require.Equal(t, el.BuildTime, e.BuildTime)
	})

	t.Run("process_metrics 逐会话变化（非定值）", func(t *testing.T) {
		short := eventContext(t)
		short.Uptime = 1 * time.Second
		long := eventContext(t)
		long.Uptime = 500 * time.Second
		es := BuildReclaudeDatadogBatch(short)[0]
		el := BuildReclaudeDatadogBatch(long)[0]
		// uptime 直接反映会话时长，两次必须不同。
		require.NotEqual(t, es.ProcessMetrics.Uptime, el.ProcessMetrics.Uptime)
		require.InDelta(t, 1.0, es.ProcessMetrics.Uptime, 0.01)
		require.InDelta(t, 500.0, el.ProcessMetrics.Uptime, 0.01)
	})

	t.Run("缺 account_uuid 返回 nil（不发可解释，不造假）", func(t *testing.T) {
		ctx := eventContext(t)
		delete(ctx.Account.Credentials, CredKeyReclaudeAccountUUID)
		require.Nil(t, BuildReclaudeDatadogBatch(ctx))
	})

	t.Run("nil account 返回 nil", func(t *testing.T) {
		require.Nil(t, BuildReclaudeDatadogBatch(ReclaudeEventContext{}))
	})
}

func TestReclaudeUserBucket(t *testing.T) {
	t.Run("稳定：同账号每次同桶", func(t *testing.T) {
		uuid := "9c67eb02-4001-4cde-a6e2-e40f1a71649e"
		require.Equal(t, reclaudeUserBucket(uuid), reclaudeUserBucket(uuid))
	})
	t.Run("范围 0-9", func(t *testing.T) {
		for _, u := range []string{"a", "b", "c-d-e", "9c67eb02-4001-4cde-a6e2-e40f1a71649e"} {
			b := reclaudeUserBucket(u)
			require.GreaterOrEqual(t, b, 0)
			require.LessOrEqual(t, b, 9)
		}
	})
	t.Run("分散：不同账号不全同桶（避免批量特征）", func(t *testing.T) {
		seen := map[int]bool{}
		uuids := []string{
			"9c67eb02-4001-4cde-a6e2-e40f1a71649e",
			"11111111-2222-3333-4444-555555555555",
			"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"deadbeef-0000-1111-2222-333333333333",
			"00000000-0000-0000-0000-000000000001",
		}
		for _, u := range uuids {
			seen[reclaudeUserBucket(u)] = true
		}
		// 5 个账号至少落进 2 个不同桶 —— 统一填一个值会只有 1 个。
		require.Greater(t, len(seen), 1)
	})
}

func TestReclaudeDatadogTagsAndRequest(t *testing.T) {
	t.Run("ddtags 键序与真值一致", func(t *testing.T) {
		e := BuildReclaudeDatadogBatch(eventContext(t))[0]
		// event:...,arch:...,client_type:cli,entrypoint:cli,model:...,platform:...
		require.True(t, strings.HasPrefix(e.DDTags, "event:tengu_started,arch:x64,client_type:cli,entrypoint:cli,model:"))
		require.Contains(t, e.DDTags, ",user_type:external,")
		require.Contains(t, e.DDTags, ",platform:linux,")
	})

	t.Run("请求头对齐真值：axios/无 auth/dd-api-key", func(t *testing.T) {
		batch := BuildReclaudeDatadogBatch(eventContext(t))
		body, err := json.Marshal(batch)
		require.NoError(t, err)
		req := BuildReclaudeDatadogRequest(body, 3*time.Second)

		require.Equal(t, "POST", req.Method)
		require.Equal(t, ReclaudeDatadogLogsURL, req.URL)
		require.False(t, req.NeedsAuthorization) // 🔴 真值无 authorization
		require.Equal(t, "axios/1.15.2", req.Headers["user-agent"])
		require.Equal(t, reclaudeDatadogAPIKey, req.Headers["dd-api-key"])
		require.Equal(t, "http-intake.logs.us5.datadoghq.com", req.Headers["host"])
		require.Equal(t, "close", req.Headers["connection"])
	})

	t.Run("信封体是 JSON 数组（Datadog logs 格式）", func(t *testing.T) {
		batch := BuildReclaudeDatadogBatch(eventContext(t))
		body, err := json.Marshal(batch)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(strings.TrimSpace(string(body)), "["))
	})
}
