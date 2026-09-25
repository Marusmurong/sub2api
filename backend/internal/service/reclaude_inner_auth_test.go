//go:build unit

package service

import (
	"net/http"
	"strings"
	"testing"
)

// 内层 authorization 必须是本设备的 SK，且**只能有一个** header 键。
//
// 2026-09-23 的真实故障：伪装路径先用小写键写了 `authorization: "Bearer "`
// （reclaude 账号的 token 是空串），随后补 SK 时若用 Header.Set 会另建规范大小写的
// "Authorization" 键。装箱时所有头名统一转小写 ⇒ 两个键塌成一个，谁覆盖谁取决于
// map 遍历顺序，于是相当一部分请求带着空凭据出门，网关回 400 bad_envelope。
func TestReclaudeInnerAuthorizationSingleLowercaseKey(t *testing.T) {
	inner, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 复现伪装路径留下的空凭据（小写键，绕过 Go 的规范化）。
	setHeaderRaw(inner.Header, "authorization", "Bearer ")

	// 被测行为：ReclaudeUpstream.Do 里补 SK 用的就是这一句。
	setHeaderRaw(inner.Header, "authorization", "Bearer sk-rec-test")

	var authKeys []string
	for name := range inner.Header {
		if strings.EqualFold(name, "authorization") {
			authKeys = append(authKeys, name)
		}
	}
	if len(authKeys) != 1 {
		t.Fatalf("authorization 只能有一个 header 键，实际有 %d 个: %v", len(authKeys), authKeys)
	}
	// 注意：Header.Get 会把键规范化成 "Authorization"，取不到小写原始键，
	// 所以这里直接读 map —— 这也正是当初两个键能并存而没被发现的原因。
	if got := inner.Header[authKeys[0]]; len(got) != 1 || got[0] != "Bearer sk-rec-test" {
		t.Fatalf("内层 authorization 应为设备 SK，得到 %v", got)
	}

	// 装箱后同样只能剩一项，且是 SK。
	envelope, _, err := buildReclaudeEnvelope(inner, "https://www.reclaude.ai", nil)
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	if !strings.Contains(string(envelope), `"authorization":"Bearer sk-rec-test"`) {
		t.Fatal("信封里的 authorization 不是设备 SK")
	}
	if strings.Contains(string(envelope), `"authorization":"Bearer "`) {
		t.Fatal("信封里仍残留空凭据的 authorization")
	}
}
