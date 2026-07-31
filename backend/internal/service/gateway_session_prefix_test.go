//go:build unit

package service

import (
	"strings"
	"testing"
)

// 会话哈希必须在对话增长过程中保持不变。
//
// 生产实测（2026-07-31）：88% 的下游请求不带 metadata.user_id，会话哈希退化为
// 「哈希全部消息内容」或「哈希带 cache_control 的消息」——两者都随对话追加而改变，
// 于是同一个对话每轮算出不同的哈希：
//
//	粘性未命中 70% → 每轮换账号 → 客户端的 thinking 签名属于上一个账号
//	→ 400 messages.N.content.M: Invalid `signature` in `thinking` block
//
// 修法是改用「对话前缀」：对话是追加式增长的，system + 头一条消息在整个对话
// 生命周期里不变。
func TestGenerateSessionHash_StableAcrossConversationGrowth(t *testing.T) {
	svc := &GatewayService{}

	turns := []string{
		`{"system":"You are a coding agent.","messages":[` +
			`{"role":"user","content":"帮我看下这个 bug"}]}`,

		`{"system":"You are a coding agent.","messages":[` +
			`{"role":"user","content":"帮我看下这个 bug"},` +
			`{"role":"assistant","content":[{"type":"thinking","thinking":"t1","signature":"s1"},{"type":"text","text":"好的"}]},` +
			`{"role":"user","content":"继续"}]}`,

		`{"system":"You are a coding agent.","messages":[` +
			`{"role":"user","content":"帮我看下这个 bug"},` +
			`{"role":"assistant","content":[{"type":"thinking","thinking":"t1","signature":"s1"},{"type":"text","text":"好的"}]},` +
			`{"role":"user","content":"继续"},` +
			`{"role":"assistant","content":[{"type":"text","text":"修好了"}]},` +
			`{"role":"user","content":"再看一处"}]}`,
	}

	var first string
	for i, body := range turns {
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), "")
		if err != nil {
			t.Fatalf("turn %d 解析失败: %v", i+1, err)
		}
		got := svc.GenerateSessionHash(parsed)
		if got == "" {
			t.Fatalf("turn %d 哈希为空", i+1)
		}
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Errorf("turn %d 的哈希与 turn 1 不同: %s != %s（对话增长不得改变会话身份）", i+1, got, first)
		}
	}
}

// 缓存断点随对话往后挪，同样不得改变会话身份。
// 这是生产上占比最大的一条路径（cacheable_content，58% 流量）。
func TestGenerateSessionHash_StableWhenCacheBreakpointMoves(t *testing.T) {
	svc := &GatewayService{}

	turn1 := `{"system":"sys","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"开场白","cache_control":{"type":"ephemeral"}}]}]}`
	turn2 := `{"system":"sys","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"开场白"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"回复"}]},` +
		`{"role":"user","content":[{"type":"text","text":"第二问","cache_control":{"type":"ephemeral"}}]}]}`

	h1 := hashOf(t, svc, turn1)
	h2 := hashOf(t, svc, turn2)
	if h1 != h2 {
		t.Errorf("缓存断点移动改变了会话身份: %s != %s", h1, h2)
	}
}

// metadata.user_id 仍是最高优先级：客户端给了稳定标识就用它，不要绕道内容推导。
func TestGenerateSessionHash_MetadataStillWins(t *testing.T) {
	svc := &GatewayService{}
	body := `{"metadata":{"user_id":"{\"device_id\":\"dev-1\",\"session_id\":\"123e4567-e89b-12d3-a456-426614174000\"}"},` +
		`"system":"sys","messages":[{"role":"user","content":"hi"}]}`

	got := hashOf(t, svc, body)
	if !strings.Contains(got, "123e4567") {
		t.Errorf("应直接返回 metadata 里的 session id，得到: %s", got)
	}
}

// 不同对话必须得到不同的会话身份，否则所有流量会挤到同一个账号上。
func TestGenerateSessionHash_DistinctConversationsDiffer(t *testing.T) {
	svc := &GatewayService{}

	a := `{"system":"sys","messages":[{"role":"user","content":"第一个对话的开场"}]}`
	b := `{"system":"sys","messages":[{"role":"user","content":"完全不同的另一个开场"}]}`

	if hashOf(t, svc, a) == hashOf(t, svc, b) {
		t.Errorf("不同开场的对话不应共用同一个会话身份")
	}
}

// 客户端上下文（IP / UA / api key）必须参与，避免不同客户发同样的开场白时撞车。
func TestGenerateSessionHash_SessionContextSeparatesClients(t *testing.T) {
	svc := &GatewayService{}
	const body = `{"system":"sys","messages":[{"role":"user","content":"hi"}]}`

	p1, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), "")
	if err != nil {
		t.Fatal(err)
	}
	p1.SessionContext = &SessionContext{ClientIP: "1.1.1.1", UserAgent: "cc/1.0", APIKeyID: 1}

	p2, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), "")
	if err != nil {
		t.Fatal(err)
	}
	p2.SessionContext = &SessionContext{ClientIP: "2.2.2.2", UserAgent: "cc/1.0", APIKeyID: 2}

	if svc.GenerateSessionHash(p1) == svc.GenerateSessionHash(p2) {
		t.Errorf("不同客户端发相同开场白不应共用会话身份")
	}
}

func hashOf(t *testing.T, svc *GatewayService, body string) string {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	h := svc.GenerateSessionHash(parsed)
	if h == "" {
		t.Fatalf("哈希为空")
	}
	return h
}
