package service

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// OAuth 授权与刷新把 account_uuid 写进 credentials（见 BuildClaudeAccountCredentials），
// 而 metadata.user_id 的读取侧一度只看 extra，导致生产上 36 个账号里 35 个明明有值，
// 发给上游的 metadata.user_id 里 account_uuid 却恒为空串——一个跨全部账号相同的常量。
// 本文件锁定"两处都读"的取值语义。
func TestAccountGetAccountUUIDFallsBackToCredentials(t *testing.T) {
	const fromExtra = "11111111-2222-3333-4444-555555555555"
	const fromCreds = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	tests := []struct {
		name    string
		account *Account
		want    string
	}{
		{
			name:    "extra 优先",
			account: &Account{Extra: map[string]any{"account_uuid": fromExtra}, Credentials: map[string]any{"account_uuid": fromCreds}},
			want:    fromExtra,
		},
		{
			name:    "extra 缺失时回退 credentials",
			account: &Account{Credentials: map[string]any{"account_uuid": fromCreds}},
			want:    fromCreds,
		},
		{
			name:    "extra 为空白时回退 credentials",
			account: &Account{Extra: map[string]any{"account_uuid": "   "}, Credentials: map[string]any{"account_uuid": fromCreds}},
			want:    fromCreds,
		},
		{
			name:    "两处都没有返回空",
			account: &Account{},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.account.GetAccountUUID(); got != tt.want {
				t.Errorf("GetAccountUUID() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormatMetadataUserIDCarriesAccountUUID 固化修复效果：
// 取到 account_uuid 后，发给上游的 metadata.user_id 里该字段必须是真实值而非空串。
func TestFormatMetadataUserIDCarriesAccountUUID(t *testing.T) {
	const deviceID = "9f1c3e7a55b2d84c6e0a1f93b7d25c48a3e6019d4b7f2c85e1a0d63f9c47b2e5"
	const sessionID = "6fb64a13-ce54-499e-8ae1-63aca9e5d2d6"

	account := &Account{Credentials: map[string]any{"account_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}}

	userID := FormatMetadataUserID(deviceID, account.GetAccountUUID(), sessionID, "2.1.220")

	got := gjson.Get(userID, "account_uuid").String()
	if got == "" {
		t.Fatalf("metadata.user_id 仍带空 account_uuid: %s", userID)
	}
	if got != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("account_uuid = %q, want credentials 中的值", got)
	}

	// 回归：不允许再出现 "account_uuid":"" 这种跨账号恒定的形态。
	if strings.Contains(userID, `"account_uuid":""`) {
		t.Errorf("metadata.user_id 含空 account_uuid: %s", userID)
	}
}

// TestBuildClaudeAccountCredentialsPersistsIdentityFields 锁定刷新链路会落库身份字段，
// 且不会用空值覆盖已有值（MergeCredentials 语义）。
func TestBuildClaudeAccountCredentialsPersistsIdentityFields(t *testing.T) {
	creds := BuildClaudeAccountCredentials(&TokenInfo{
		AccessToken:  "at",
		TokenType:    "Bearer",
		AccountUUID:  "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		OrgUUID:      "org-1",
		EmailAddress: "someone@example.com",
	})
	if creds["account_uuid"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("account_uuid 未落库: %v", creds["account_uuid"])
	}
	if creds["org_uuid"] != "org-1" {
		t.Errorf("org_uuid 未落库: %v", creds["org_uuid"])
	}

	// 刷新响应不带身份字段时不得写入空值，否则 MergeCredentials 会把已有值覆盖掉。
	bare := BuildClaudeAccountCredentials(&TokenInfo{AccessToken: "at", TokenType: "Bearer"})
	if _, exists := bare["account_uuid"]; exists {
		t.Error("响应无 account_uuid 时不应写入该键")
	}

	merged := MergeCredentials(
		map[string]any{"account_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		bare,
	)
	if merged["account_uuid"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("刷新后已有 account_uuid 被覆盖: %v", merged["account_uuid"])
	}
}
