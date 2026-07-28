package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestHoistSystemRoleMessages(t *testing.T) {
	t.Run("字符串内容的 system 消息提升到顶层", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"hi"}]}`)

		got, changed := hoistSystemRoleMessages(body)

		if !changed {
			t.Fatalf("changed = false, want true")
		}
		if gjson.GetBytes(got, "system").String() != "You are helpful" {
			t.Errorf("system = %q, body=%s", gjson.GetBytes(got, "system").String(), got)
		}
		msgs := gjson.GetBytes(got, "messages").Array()
		if len(msgs) != 1 || msgs[0].Get("role").String() != "user" {
			t.Errorf("messages should only keep the user turn: %s", got)
		}
	})

	t.Run("文本块数组内容的 system 消息", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"Rule A"},{"type":"text","text":"Rule B"}]},{"role":"user","content":"hi"}]}`)

		got, changed := hoistSystemRoleMessages(body)

		if !changed {
			t.Fatalf("changed = false, want true")
		}
		sys := gjson.GetBytes(got, "system").String()
		if sys != "Rule A\n\nRule B" {
			t.Errorf("system = %q", sys)
		}
	})

	t.Run("已有顶层 system 时前置合并", func(t *testing.T) {
		body := []byte(`{"system":"Existing","messages":[{"role":"system","content":"Extra"},{"role":"user","content":"hi"}]}`)

		got, changed := hoistSystemRoleMessages(body)

		if !changed {
			t.Fatalf("changed = false, want true")
		}
		if sys := gjson.GetBytes(got, "system").String(); sys != "Existing\n\nExtra" {
			t.Errorf("system = %q", sys)
		}
	})

	t.Run("多条 system 消息全部提升", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"system","content":"A"},{"role":"user","content":"hi"},{"role":"system","content":"B"}]}`)

		got, changed := hoistSystemRoleMessages(body)

		if !changed {
			t.Fatalf("changed = false, want true")
		}
		if sys := gjson.GetBytes(got, "system").String(); sys != "A\n\nB" {
			t.Errorf("system = %q", sys)
		}
		if len(gjson.GetBytes(got, "messages").Array()) != 1 {
			t.Errorf("only the user turn should remain: %s", got)
		}
	})

	t.Run("developer 角色同样处理", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"developer","content":"Dev rule"},{"role":"user","content":"hi"}]}`)

		got, changed := hoistSystemRoleMessages(body)

		if !changed {
			t.Fatalf("changed = false, want true")
		}
		if gjson.GetBytes(got, "system").String() != "Dev rule" {
			t.Errorf("system = %q", gjson.GetBytes(got, "system").String())
		}
	})

	// —— 不该改动的情况 ——
	t.Run("没有 system 消息时不改动", func(t *testing.T) {
		body := []byte(`{"system":"top","messages":[{"role":"user","content":"hi"}]}`)

		_, changed := hoistSystemRoleMessages(body)

		if changed {
			t.Errorf("changed = true, want false")
		}
	})

	t.Run("顶层 system 为块数组时不动(避免破坏 cache_control)", func(t *testing.T) {
		body := []byte(`{"system":[{"type":"text","text":"CC","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"system","content":"X"},{"role":"user","content":"hi"}]}`)

		_, changed := hoistSystemRoleMessages(body)

		if changed {
			t.Errorf("changed = true, want false (块数组 system 含 cache_control,合并会破坏缓存)")
		}
	})

	t.Run("system 消息内容为空时仅移除不写入", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"system","content":"   "},{"role":"user","content":"hi"}]}`)

		got, changed := hoistSystemRoleMessages(body)

		if !changed {
			t.Fatalf("changed = false, want true")
		}
		if gjson.GetBytes(got, "system").Exists() {
			t.Errorf("empty system must not be written: %s", got)
		}
		if len(gjson.GetBytes(got, "messages").Array()) != 1 {
			t.Errorf("system message should be removed: %s", got)
		}
	})

	t.Run("全部是 system 消息时不改动(避免留下空 messages)", func(t *testing.T) {
		body := []byte(`{"messages":[{"role":"system","content":"A"}]}`)

		_, changed := hoistSystemRoleMessages(body)

		if changed {
			t.Errorf("changed = true, want false (Anthropic 要求 messages 非空)")
		}
	})

	t.Run("空 body 与非法 JSON", func(t *testing.T) {
		if _, changed := hoistSystemRoleMessages(nil); changed {
			t.Errorf("nil: changed = true")
		}
		if _, changed := hoistSystemRoleMessages([]byte(`not json`)); changed {
			t.Errorf("invalid: changed = true")
		}
	})
}
