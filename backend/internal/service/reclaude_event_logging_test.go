//go:build unit

package service

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func eventAccount(t *testing.T) *Account {
	t.Helper()
	account, _ := probeAccount(t)
	account.Credentials[CredKeyReclaudeOrganizationUUID] = "47be3ed1-4b36-4bae-837d-40b5106369ec"
	account.Credentials[CredKeyReclaudeAccountUUID] = "9c67eb02-4001-4cde-a6e2-e40f1a71649e"
	account.Credentials[CredKeyReclaudeClaudeUserID] =
		"3c5a8384b29498aeb24ac2717c50dbccbffe818352a8eac8c1a6f29d690b2e31"
	account.Credentials[CredKeyReclaudeClientPlatform] = "linux/amd64"
	account.Credentials[CredKeyReclaudeClientVersion] = "2.1.280"
	// 真机快照：与登录 /auth/start 上报的同源。缺它会回落到 platform 推断，
	// 而推断值与真机对不上正是撤销的根因。
	account.Credentials[CredKeyReclaudeMachineEnv] = `{"node_version":"v18.19.1",` +
		`"arch":"x64","linux_distro_id":"ubuntu","linux_distro_version":"24.04",` +
		`"linux_kernel":"6.17.0-1017-aws","shell":"bash"}`
	return account
}

func eventContext(t *testing.T) ReclaudeEventContext {
	t.Helper()
	return ReclaudeEventContext{
		Account:   eventAccount(t),
		SessionID: "d078ca47-4646-4570-a843-7513d0196157",
		Model:     "claude-opus-5-5[1m]",
		Uptime:    39 * time.Second,
		Now:       time.Date(2026, 9, 25, 14, 38, 22, 100_000_000, time.UTC),
	}
}

func TestBuildReclaudeEventBatch(t *testing.T) {
	t.Run("顶层只有 events", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t), []string{ReclaudeEventAPIQuery})
		require.NotNil(t, batch)

		raw, err := json.Marshal(batch)
		require.NoError(t, err)
		var decoded map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.ElementsMatch(t, []string{"events"}, keysOf(decoded))
	})

	t.Run("event_data 字段与真值一致", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t), []string{ReclaudeEventAPIQuery})
		raw, err := json.Marshal(batch.Events[0].EventData)
		require.NoError(t, err)

		var decoded map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.ElementsMatch(t, []string{
			"event_name", "client_timestamp", "model", "session_id", "user_type",
			"betas", "env", "entrypoint", "is_interactive", "client_type",
			"process", "auth", "event_id", "device_id",
		}, keysOf(decoded))
	})

	t.Run("env 字段与真值一致", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t), []string{ReclaudeEventAPIQuery})
		raw, err := json.Marshal(batch.Events[0].EventData.Env)
		require.NoError(t, err)

		var decoded map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.ElementsMatch(t, []string{
			"platform", "node_version", "terminal", "package_managers", "runtimes",
			"is_running_with_bun", "is_ci", "is_claubbit", "is_github_action",
			"is_claude_code_action", "is_claude_ai_auth", "version", "arch",
			"is_claude_code_remote", "deployment_environment", "is_conductor",
			"version_base", "is_local_agent_mode", "platform_raw", "shell",
			// linux_* 带 omitempty：eventAccount 注入了 Linux 真机快照，故应出现。
			"linux_distro_id", "linux_distro_version", "linux_kernel",
		}, keysOf(decoded))
	})

	t.Run("时间戳格式对齐真值（毫秒 + Z）", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t), []string{ReclaudeEventAPIQuery})
		require.Equal(t, "2026-09-25T14:38:22.100Z", batch.Events[0].EventData.ClientTimestamp)
	})

	t.Run("每条事件的 event_id 唯一", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t), []string{
			ReclaudeEventAPIQuery, ReclaudeEventAPIRetry, ReclaudeEventAPICacheBreakpoints,
		})
		require.Len(t, batch.Events, 3)

		seen := map[string]bool{}
		for _, e := range batch.Events {
			require.NotEmpty(t, e.EventData.EventID)
			require.False(t, seen[e.EventData.EventID], "event_id 必须逐条唯一")
			seen[e.EventData.EventID] = true
		}
	})

	t.Run("身份字段用账号真值", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t), []string{ReclaudeEventAPIQuery})
		data := batch.Events[0].EventData
		require.Equal(t, "47be3ed1-4b36-4bae-837d-40b5106369ec", data.Auth.OrganizationUUID)
		require.Equal(t, "9c67eb02-4001-4cde-a6e2-e40f1a71649e", data.Auth.AccountUUID)
		require.Equal(t,
			"3c5a8384b29498aeb24ac2717c50dbccbffe818352a8eac8c1a6f29d690b2e31", data.DeviceID)
	})

	t.Run("arch 用 Node 命名：amd64 → x64", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t), []string{ReclaudeEventAPIQuery})
		require.Equal(t, "x64", batch.Events[0].EventData.Env.Arch)
		require.Equal(t, "linux", batch.Events[0].EventData.Env.Platform)
	})

	t.Run("无真机快照时回落到 platform 推断", func(t *testing.T) {
		// 一批设备共用同一套推断 env 是批量特征 —— 这是降级路径，不是正解。
		ctx := eventContext(t)
		delete(ctx.Account.Credentials, CredKeyReclaudeMachineEnv)
		ctx.Account.Credentials[CredKeyReclaudeClientPlatform] = "darwin/arm64"

		env := BuildReclaudeEventBatch(ctx, []string{ReclaudeEventAPIQuery}).Events[0].EventData.Env

		require.Equal(t, "darwin", env.Platform)
		require.Equal(t, "arm64", env.Arch)
		require.Equal(t, "zsh", env.Shell, "macOS 默认 shell 应与平台自洽")
		require.Equal(t, "unknown-darwin", env.DeploymentEnvironment)
	})

	// 🔴 撤销复盘的核心断言：env 必须用**真机快照**，不是硬编码/推断。
	t.Run("真机快照优先于 platform 推断", func(t *testing.T) {
		env := BuildReclaudeEventBatch(eventContext(t), []string{ReclaudeEventAPIQuery}).
			Events[0].EventData.Env

		require.Equal(t, "v18.19.1", env.NodeVersion, "不能再是硬编码的 v26.3.0")
		require.Equal(t, "x64", env.Arch)
		require.Equal(t, "ubuntu", env.LinuxDistroID)
		require.Equal(t, "24.04", env.LinuxDistroVersion)
		require.Equal(t, "6.17.0-1017-aws", env.LinuxKernel)
		require.Equal(t, "bash", env.Shell)
	})

	t.Run("坏的快照 JSON 不 panic，回落到推断", func(t *testing.T) {
		ctx := eventContext(t)
		ctx.Account.Credentials[CredKeyReclaudeMachineEnv] = "{not json"

		require.NotPanics(t, func() {
			env := BuildReclaudeEventBatch(ctx, []string{ReclaudeEventAPIQuery}).
				Events[0].EventData.Env
			require.Equal(t, reclaudeDefaultNodeVersion, env.NodeVersion)
		})
	})
}

// 🔴 缺真值身份字段就整批不发 —— 编一个 UUID 比沉默更危险。
func TestReclaudeEventBatchRefusesToFabricate(t *testing.T) {
	for _, tc := range []struct {
		name string
		drop string
	}{
		{"缺 organization_uuid", CredKeyReclaudeOrganizationUUID},
		{"缺 account_uuid", CredKeyReclaudeAccountUUID},
		{"缺 claude_user_id", CredKeyReclaudeClaudeUserID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := eventContext(t)
			delete(ctx.Account.Credentials, tc.drop)

			require.Nil(t, BuildReclaudeEventBatch(ctx, []string{ReclaudeEventAPIQuery}),
				"缺真值身份时必须不发，而不是用合成值顶上")
		})
	}

	t.Run("空事件名列表不产出批次", func(t *testing.T) {
		require.Nil(t, BuildReclaudeEventBatch(eventContext(t), nil))
	})

	t.Run("nil 账号不 panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			require.Nil(t, BuildReclaudeEventBatch(
				ReclaudeEventContext{}, []string{ReclaudeEventAPIQuery}))
		})
	})
}

func TestReclaudeProcessField(t *testing.T) {
	t.Run("是 base64 的 JSON，字段对齐真值", func(t *testing.T) {
		encoded := EncodeReclaudeProcess(collectReclaudeProcessStats(39 * time.Second))

		raw, err := base64.StdEncoding.DecodeString(encoded)
		require.NoError(t, err, "真值用标准编码带填充，不是签名头的 RawURL")

		var decoded map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.ElementsMatch(t, []string{
			"uptime", "rss", "heapTotal", "heapUsed", "external", "arrayBuffers",
			"constrainedMemory", "cpuUsage", "cpuPercent", "cpuWindowMs",
		}, keysOf(decoded))
	})

	t.Run("uptime 如实反映会话时长", func(t *testing.T) {
		raw, err := base64.StdEncoding.DecodeString(
			EncodeReclaudeProcess(collectReclaudeProcessStats(90 * time.Second)))
		require.NoError(t, err)

		var stats ReclaudeProcessStats
		require.NoError(t, json.Unmarshal(raw, &stats))
		require.InDelta(t, 90.0, stats.Uptime, 0.001)
	})

	t.Run("内存数字不是定值", func(t *testing.T) {
		// 🔴 抄一组定值 = 整批设备共享同一份内存指纹，一次 group by 就暴露。
		stats := collectReclaudeProcessStats(time.Second)
		require.NotZero(t, stats.RSS)
		require.NotZero(t, stats.HeapTotal)
		require.NotEqual(t, uint64(215101440), stats.RSS, "不能是样本里的定值")
	})
}
