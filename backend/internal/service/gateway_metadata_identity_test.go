//go:build unit

package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

// 客户端的 metadata.user_id 解析不出 Claude Code 形状时，必须整体替换，而不是放行。
//
// 生产抓包实测（2026-07-31，10 分钟窗口 78 个上游请求）：其中 7 个把客户端原样的
//
//	{"frame_id": "...", "session_id": "..."}
//
// 送到了上游。frame_id 是 Claude Code 从不发送的字段，对上游而言是明确的第三方
// 特征。这 7 条的转发上下文与正常的 71 条完全一致（mimic_claude_code=true、
// fingerprint_applied=true、enable_mpt=false）——伪装开着、透传开关关着，却依然透传了。
//
// 成因在 RewriteUserID：它先用 ParseMetadataUserID 解析客户端原值，解析不出就原样
// 返回。而 ParseMetadataUserID 的 JSON 分支要求同时有 device_id 与 session_id，
// {frame_id, session_id} 没有 device_id → 返回 nil → 跳过改写。
//
// 逻辑与目标正好相反：客户端越不像 Claude Code，我们越不动它。
func TestRewriteUserIDReplacesUnparsableClientMetadata(t *testing.T) {
	const ourClientID = "1111111111111111111111111111111111111111111111111111111111111111"
	const ourAccountUUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	tests := []struct {
		name     string
		clientUD string
		reason   string
	}{
		{
			name:     "frame_id 形状（生产实际出现）",
			clientUD: `{\"frame_id\":\"f-123\",\"session_id\":\"6fb64a13-ce54-499e-8ae1-63aca9e5d2d6\"}`,
			reason:   "frame_id 是 CC 从不发送的字段",
		},
		{
			name:     "缺 device_id",
			clientUD: `{\"account_uuid\":\"x\",\"session_id\":\"6fb64a13-ce54-499e-8ae1-63aca9e5d2d6\"}`,
			reason:   "ParseMetadataUserID 的 JSON 分支要求 device_id 非空",
		},
		{
			name:     "完全陌生的形状",
			clientUD: `{\"foo\":\"bar\"}`,
			reason:   "任何我们不认识的形状都不该原样上行",
		},
		{
			name:     "非 JSON 非 legacy 的裸字符串",
			clientUD: "some-opaque-client-token",
			reason:   "同上",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"metadata":{"user_id":"` + tt.clientUD + `"},"messages":[{"role":"user","content":"hi"}]}`)
			svc := &IdentityService{}

			out, err := svc.RewriteUserID(body, 36, ourAccountUUID, ourClientID, "claude-cli/2.1.220 (external, cli)")
			if err != nil {
				t.Fatalf("RewriteUserID 失败: %v", err)
			}

			got := gjson.GetBytes(out, "metadata.user_id").String()
			parsed := ParseMetadataUserID(got)
			if parsed == nil {
				t.Fatalf("改写后仍不是 CC 形状: %q（%s）", got, tt.reason)
			}
			if parsed.DeviceID != ourClientID {
				t.Errorf("device_id = %q, want 我方指纹 %q", parsed.DeviceID, ourClientID)
			}
			if parsed.AccountUUID != ourAccountUUID {
				t.Errorf("account_uuid = %q, want %q", parsed.AccountUUID, ourAccountUUID)
			}
			if parsed.SessionID == "" {
				t.Errorf("session_id 不得为空: %q", got)
			}
		})
	}
}

// 替换后的 session_id 必须对同一个客户端会话保持稳定——否则同一个账号每轮换一个
// 会话身份，上游看到的仍是"一个号被很多客户端在用"。
func TestRewriteUserIDUnparsableSessionIsStable(t *testing.T) {
	const ourClientID = "1111111111111111111111111111111111111111111111111111111111111111"
	const ourAccountUUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	body := []byte(`{"metadata":{"user_id":"{\"frame_id\":\"f-123\",\"session_id\":\"s-abc\"}"},"messages":[]}`)

	svc := &IdentityService{}
	mk := func(acctID int64) string {
		out, err := svc.RewriteUserID(body, acctID, ourAccountUUID, ourClientID, "claude-cli/2.1.220 (external, cli)")
		if err != nil {
			t.Fatalf("RewriteUserID 失败: %v", err)
		}
		return gjson.GetBytes(out, "metadata.user_id").String()
	}
	if a, b := mk(36), mk(36); a != b {
		t.Errorf("同一输入必须产出同一身份\n a=%s\n b=%s", a, b)
	}
	if mk(36) == mk(37) {
		t.Errorf("不同账号不得产出同一个 session 身份")
	}
}
