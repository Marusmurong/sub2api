package admin

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// gin binding 的 oneof 是**真正的类型白名单**：不加 reclaude，管理端创建直接 400，
// 无论 service 层做了多少登记。
func TestAccountRequestTypeWhitelistIncludesReclaude(t *testing.T) {
	bindingTagOf := func(t *testing.T, request any) string {
		t.Helper()
		field, ok := reflect.TypeOf(request).FieldByName("Type")
		require.True(t, ok, "Type 字段不存在，结构体可能已被上游重命名")
		return field.Tag.Get("binding")
	}

	t.Run("创建请求", func(t *testing.T) {
		tag := bindingTagOf(t, CreateAccountRequest{})
		require.Contains(t, tag, "oneof=")
		require.Containsf(t, tag, service.AccountTypeReclaude, "创建表单会 400：%s", tag)
	})

	t.Run("更新请求", func(t *testing.T) {
		tag := bindingTagOf(t, UpdateAccountRequest{})
		require.Contains(t, tag, "oneof=")
		require.Containsf(t, tag, service.AccountTypeReclaude, "更新表单会 400：%s", tag)
	})

	t.Run("既有类型没被挤掉", func(t *testing.T) {
		tag := bindingTagOf(t, CreateAccountRequest{})
		for _, accountType := range []string{
			service.AccountTypeOAuth, service.AccountTypeSetupToken, service.AccountTypeAPIKey,
			service.AccountTypeUpstream, service.AccountTypeBedrock, service.AccountTypeServiceAccount,
		} {
			require.Containsf(t, tag, accountType, "%s 从白名单里消失了", accountType)
		}
	})
}

// V-10：数据导入是绕开建号表单的第二个入口。导出一份 rec 账号 JSON、改名再导入
// ⇒ 两行同 device_id，直接违反 V-4。所以根本不把 reclaude 放进导入白名单，
// 让它只能走建号表单。
func TestDataImportRejectsReclaudeType(t *testing.T) {
	item := DataAccount{
		Name:        "rec/mbp-dev-3448",
		Platform:    "anthropic",
		Type:        service.AccountTypeReclaude,
		Credentials: map[string]any{service.CredKeyReclaudeSK: "sk-rec-abc"},
	}

	err := validateDataAccount(item)

	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "not supported"),
		"错误信息应当说明类型不被导入通道接受，实际：%v", err)
}
