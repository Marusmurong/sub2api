package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

// 回归：mimicry 路径把 header 指纹换成我们的，但客户端自带的 metadata.user_id
// 若不一并改写，就会出现「header 说 A 机器、body 说 B 机器」的身份矛盾。
//
// 两处逻辑会保留客户端原值：
//   - buildOAuthMetadataUserID：parsed.MetadataUserID 非空时直接返回 ""，不生成
//   - ensureClaudeOAuthMetadataUserID：metadata.user_id 已存在时不覆盖
//
// 真正负责改写的是 RewriteUserID，而它被 accountUUID != "" 挡着。account_uuid 一度
// 只从 extra 读、而 OAuth 把它写在 credentials，导致该值恒为空、改写恒被跳过——
// 客户端的 device_id 就这样原样上行。
//
// 本测试锁住修复后的行为：拿得到 account_uuid 时，出口的 device_id 必须是我们的
// 指纹 ClientID，而不是客户端的。
func TestRewriteUserIDReplacesClientDeviceIdentity(t *testing.T) {
	const clientDeviceID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const ourClientID = "1111111111111111111111111111111111111111111111111111111111111111"
	const ourAccountUUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	clientBody := []byte(`{"metadata":{"user_id":"{\"device_id\":\"` + clientDeviceID +
		`\",\"account_uuid\":\"9999aaaa-bbbb-cccc-dddd-eeeeeeeeeeee\",\"session_id\":\"6fb64a13-ce54-499e-8ae1-63aca9e5d2d6\"}"},"messages":[{"role":"user","content":"hi"}]}`)

	svc := &IdentityService{}
	out, err := svc.RewriteUserID(clientBody, 36, ourAccountUUID, ourClientID, "claude-cli/2.1.220 (external, cli)")
	if err != nil {
		t.Fatalf("RewriteUserID 失败: %v", err)
	}

	userID := gjson.GetBytes(out, "metadata.user_id").String()
	parsed := ParseMetadataUserID(userID)
	if parsed == nil {
		t.Fatalf("改写后的 user_id 无法解析: %s", userID)
	}

	if parsed.DeviceID == clientDeviceID {
		t.Errorf("device_id 仍是客户端的值，header 与 body 身份不一致: %s", userID)
	}
	if parsed.DeviceID != ourClientID {
		t.Errorf("device_id = %q, want 我方指纹 ClientID %q", parsed.DeviceID, ourClientID)
	}
	if parsed.AccountUUID != ourAccountUUID {
		t.Errorf("account_uuid = %q, want %q", parsed.AccountUUID, ourAccountUUID)
	}
}

// account_uuid 取不到时 RewriteUserID 直接跳过——这正是修复前的状态。
// 本测试固化该分支的语义，避免有人误以为"跳过"也算安全：跳过意味着客户端
// device_id 原样上行。真正的保障是 GetAccountUUID 能从 credentials 取到值
// （见 TestAccountGetAccountUUIDFallsBackToCredentials）。
func TestRewriteUserIDSkipsWithoutAccountUUID(t *testing.T) {
	clientBody := []byte(`{"metadata":{"user_id":"{\"device_id\":\"cccc\",\"account_uuid\":\"\",\"session_id\":\"6fb64a13-ce54-499e-8ae1-63aca9e5d2d6\"}"},"messages":[]}`)

	svc := &IdentityService{}
	out, err := svc.RewriteUserID(clientBody, 36, "", "someclientid", "claude-cli/2.1.220 (external, cli)")
	if err != nil {
		t.Fatalf("RewriteUserID 失败: %v", err)
	}
	if string(out) != string(clientBody) {
		t.Errorf("accountUUID 为空时应原样返回，实际发生了改写: %s", out)
	}
}

// 生产账号的真实形态：account_uuid 只在 credentials 里。
// 这是上面那条改写链路能生效的前提。
func TestAccountUUIDResolvableForProductionShapedAccount(t *testing.T) {
	account := &Account{
		ID:          36,
		Credentials: map[string]any{"account_uuid": "2e7299a4-1f3b-4c5d-8e9a-0b1c2d3e4f56"},
	}
	if got := account.GetAccountUUID(); got == "" {
		t.Fatal("生产形态账号取不到 account_uuid，metadata 改写会被跳过")
	}
}
