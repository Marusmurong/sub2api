//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func mustParseSessionHashRequest(t *testing.T, body string, ctx *SessionContext) *ParsedRequest {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), domain.PlatformAnthropic)
	require.NoError(t, err)
	parsed.SessionContext = ctx
	return parsed
}

func mustParseGeminiSessionHashRequest(t *testing.T, body string, ctx *SessionContext) *ParsedRequest {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), domain.PlatformGemini)
	require.NoError(t, err)
	parsed.SessionContext = ctx
	return parsed
}

func mustParseResponsesSessionHashRequest(t *testing.T, body string, ctx *SessionContext) *ParsedRequest {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), "responses")
	require.NoError(t, err)
	parsed.SessionContext = ctx
	return parsed
}

func anthropicSessionBody(system any, messages []any, metadataUserID string) string {
	body := map[string]any{}
	if system != nil {
		body["system"] = system
	}
	if messages != nil {
		body["messages"] = messages
	}
	if metadataUserID != "" {
		body["metadata"] = map[string]any{"user_id": metadataUserID}
	}
	data, _ := json.Marshal(body)
	return string(data)
}

func geminiSessionBody(systemParts []any, contents []any) string {
	body := map[string]any{}
	if systemParts != nil {
		body["systemInstruction"] = map[string]any{"parts": systemParts}
	}
	if contents != nil {
		body["contents"] = contents
	}
	data, _ := json.Marshal(body)
	return string(data)
}

func msg(role string, content any) map[string]any {
	return map[string]any{"role": role, "content": content}
}

func geminiMsg(role string, texts ...string) map[string]any {
	parts := make([]any, 0, len(texts))
	for _, text := range texts {
		parts = append(parts, map[string]any{"text": text})
	}
	return map[string]any{"role": role, "parts": parts}
}

func TestGenerateSessionHash_NilParsedRequest(t *testing.T) {
	svc := &GatewayService{}
	require.Empty(t, svc.GenerateSessionHash(nil))
}

func TestGenerateSessionHash_EmptyRequest(t *testing.T) {
	svc := &GatewayService{}
	require.Empty(t, svc.GenerateSessionHash(&ParsedRequest{}))
}

func TestGenerateSessionHash_MetadataHasHighestPriority(t *testing.T) {
	svc := &GatewayService{}
	metadata := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_123e4567-e89b-12d3-a456-426614174000"
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello")}, metadata), nil)

	hash := svc.GenerateSessionHash(parsed)
	require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", hash, "metadata session_id should have highest priority")
}

func TestGenerateSessionHash_SystemPlusMessages(t *testing.T) {
	svc := &GatewayService{}
	withSystem := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello")}, ""), nil)
	withoutSystem := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "hello")}, ""), nil)

	h1 := svc.GenerateSessionHash(withSystem)
	h2 := svc.GenerateSessionHash(withoutSystem)
	require.NotEmpty(t, h1)
	require.NotEmpty(t, h2)
	require.NotEqual(t, h1, h2, "system prompt should be part of digest, producing different hash")
}

func TestGenerateSessionHash_SystemOnlyProducesHash(t *testing.T) {
	svc := &GatewayService{}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", nil, ""), nil)

	hash := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, hash, "system prompt alone should produce a hash as part of full digest")
}

func TestGenerateSessionHash_DifferentSystemsSameMessages(t *testing.T) {
	svc := &GatewayService{}
	parsed1 := mustParseSessionHashRequest(t, anthropicSessionBody("You are assistant A.", []any{msg("user", "hello")}, ""), nil)
	parsed2 := mustParseSessionHashRequest(t, anthropicSessionBody("You are assistant B.", []any{msg("user", "hello")}, ""), nil)

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h1, h2, "different system prompts with same messages should produce different hashes")
}

func TestGenerateSessionHash_SameSystemSameMessages(t *testing.T) {
	svc := &GatewayService{}
	mk := func() *ParsedRequest {
		return mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello"), msg("assistant", "hi")}, ""), nil)
	}

	h1 := svc.GenerateSessionHash(mk())
	h2 := svc.GenerateSessionHash(mk())
	require.Equal(t, h1, h2, "same system + same messages should produce identical hash")
}

func TestGenerateSessionHash_DifferentMessagesProduceDifferentHash(t *testing.T) {
	svc := &GatewayService{}
	parsed1 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "help me with Go")}, ""), nil)
	parsed2 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "help me with Python")}, ""), nil)

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h1, h2, "same system but different messages should produce different hashes")
}

func TestGenerateSessionHash_DifferentSessionContextProducesDifferentHash(t *testing.T) {
	svc := &GatewayService{}
	body := anthropicSessionBody(nil, []any{msg("user", "hello")}, "")
	parsed1 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "192.168.1.1", UserAgent: "Mozilla/5.0", APIKeyID: 100})
	parsed2 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "10.0.0.1", UserAgent: "curl/7.0", APIKeyID: 200})

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEmpty(t, h1)
	require.NotEmpty(t, h2)
	require.NotEqual(t, h1, h2, "same messages but different SessionContext should produce different hashes")
}

func TestGenerateSessionHash_SameSessionContextProducesSameHash(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "192.168.1.1", UserAgent: "Mozilla/5.0", APIKeyID: 100}
	body := anthropicSessionBody(nil, []any{msg("user", "hello")}, "")
	mk := func() *ParsedRequest { return mustParseSessionHashRequest(t, body, ctx) }

	h1 := svc.GenerateSessionHash(mk())
	h2 := svc.GenerateSessionHash(mk())
	require.Equal(t, h1, h2, "same messages + same SessionContext should produce identical hash")
}

func TestGenerateSessionHash_MetadataOverridesSessionContext(t *testing.T) {
	svc := &GatewayService{}
	metadata := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_123e4567-e89b-12d3-a456-426614174000"
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "hello")}, metadata), &SessionContext{ClientIP: "192.168.1.1", UserAgent: "Mozilla/5.0", APIKeyID: 100})

	hash := svc.GenerateSessionHash(parsed)
	require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", hash, "metadata session_id should take priority over SessionContext")
}

func TestGenerateSessionHash_MetadataJSON_HasHighestPriority(t *testing.T) {
	svc := &GatewayService{}
	metadata := `{"device_id":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","account_uuid":"","session_id":"c72554f2-1234-5678-abcd-123456789abc"}`
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello")}, metadata), nil)

	hash := svc.GenerateSessionHash(parsed)
	require.Equal(t, "c72554f2-1234-5678-abcd-123456789abc", hash, "JSON format metadata session_id should have highest priority")
}

func TestGenerateSessionHash_NilSessionContextBackwardCompatible(t *testing.T) {
	svc := &GatewayService{}
	body := anthropicSessionBody(nil, []any{msg("user", "hello")}, "")
	withCtx := mustParseSessionHashRequest(t, body, nil)
	withoutCtx := mustParseSessionHashRequest(t, body, nil)

	h1 := svc.GenerateSessionHash(withCtx)
	h2 := svc.GenerateSessionHash(withoutCtx)
	require.Equal(t, h1, h2, "nil SessionContext should produce same hash as no SessionContext")
}

// 需求在 2026-07-31 反转了：本用例原先断言「每一轮应当产生不同的哈希」，
// 而那正是生产故障的成因。
//
// 粘性会话的用途是把同一个对话钉在同一个账号上。哈希每轮都变，等于每轮都换号：
//
//	88% 的下游请求不带 metadata.user_id → 哈希退化为内容推导 → 每轮都变
//	→ 粘性未命中 70% → 每轮换账号
//	→ 客户端回传的 thinking 签名属于上一个账号，必然被上游拒绝
//	   400 messages.N.content.M: Invalid `signature` in `thinking` block
//	→ 且上游看到的是碎片化会话，与「一个号 = 一个客户端」直接冲突
//
// 同文件里 responses 路径的用例（"Responses input growth should preserve the hash
// when the conversation prefix is stable"）早已是这个语义，messages 路径只是一直没跟上。
func TestGenerateSessionHash_ContinuousConversation_HashStableAcrossRounds(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	round1 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello")}, ""), ctx)
	round2 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello"), msg("assistant", "Hi there!"), msg("user", "How are you?")}, ""), ctx)
	round3 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello"), msg("assistant", "Hi there!"), msg("user", "How are you?"), msg("assistant", "I'm doing well!"), msg("user", "Tell me a joke")}, ""), ctx)

	h1 := svc.GenerateSessionHash(round1)
	h2 := svc.GenerateSessionHash(round2)
	h3 := svc.GenerateSessionHash(round3)
	require.NotEmpty(t, h1)
	require.Equal(t, h1, h2, "对话追加一轮不应改变会话身份")
	require.Equal(t, h2, h3, "对话继续增长仍不应改变会话身份")

	// 反向保护：不同的对话（开场白不同）必须仍然分得开，否则所有流量会挤到一个账号。
	other := mustParseSessionHashRequest(t, anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "completely different opening")}, ""), ctx)
	require.NotEqual(t, h1, svc.GenerateSessionHash(other), "不同对话必须分得开")
}

func TestGenerateSessionHash_ContinuousConversation_SameRoundSameHash(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	body := anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello"), msg("assistant", "Hi there!"), msg("user", "How are you?")}, "")
	mk := func() *ParsedRequest { return mustParseSessionHashRequest(t, body, ctx) }

	h1 := svc.GenerateSessionHash(mk())
	h2 := svc.GenerateSessionHash(mk())
	require.Equal(t, h1, h2, "same conversation state should produce identical hash on retry")
}

func TestGenerateSessionHash_ResponsesDifferentInputProducesDifferentHash(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "codex_cli_rs/0.1.0", APIKeyID: 1}
	first := mustParseResponsesSessionHashRequest(t, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"help me with Go"}]}]}`, ctx)
	second := mustParseResponsesSessionHashRequest(t, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"help me with Python"}]}]}`, ctx)

	h1 := svc.GenerateSessionHash(first)
	h2 := svc.GenerateSessionHash(second)
	require.NotEmpty(t, h1)
	require.NotEmpty(t, h2)
	require.NotEqual(t, h1, h2, "different Responses input should produce different hashes for the same client")
}

func TestGenerateSessionHash_ResponsesGrowingInputKeepsStableHash(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "codex_cli_rs/0.1.0", APIKeyID: 1}
	round1 := mustParseResponsesSessionHashRequest(t, `{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"Be concise."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"help me with Go"}]}]}`, ctx)
	round2 := mustParseResponsesSessionHashRequest(t, `{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"Be concise."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"help me with Go"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Sure."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"add tests"}]}]}`, ctx)

	h1 := svc.GenerateSessionHash(round1)
	h2 := svc.GenerateSessionHash(round2)
	require.NotEmpty(t, h1)
	require.Equal(t, h1, h2, "Responses input growth should preserve the hash when the conversation prefix is stable")
}

func TestGenerateSessionHash_MessagesPathIgnoresResponsesInput(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	first := mustParseResponsesSessionHashRequest(t, `{"messages":[{"role":"user","content":"hello"}],"input":"first"}`, ctx)
	second := mustParseResponsesSessionHashRequest(t, `{"messages":[{"role":"user","content":"hello"}],"input":"second"}`, ctx)

	h1 := svc.GenerateSessionHash(first)
	h2 := svc.GenerateSessionHash(second)
	require.Equal(t, h1, h2, "existing messages fallback should remain authoritative when messages contain text")
}

func TestGenerateSessionHash_ResponsesInputDoesNotOverrideHigherPrioritySources(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}

	t.Run("metadata user id", func(t *testing.T) {
		metadata := "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_123e4567-e89b-12d3-a456-426614174000"
		parsed := mustParseResponsesSessionHashRequest(t, `{"metadata":{"user_id":"`+metadata+`"},"input":"hello"}`, ctx)
		require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", svc.GenerateSessionHash(parsed))
	})

	t.Run("cache control", func(t *testing.T) {
		body := `{"system":[{"type":"text","text":"stable cache anchor","cache_control":{"type":"ephemeral"}}],"input":"hello"}`
		first := mustParseResponsesSessionHashRequest(t, body, ctx)
		second := mustParseResponsesSessionHashRequest(t, body, &SessionContext{ClientIP: "9.8.7.6", UserAgent: "other", APIKeyID: 2})
		require.Equal(t, svc.GenerateSessionHash(first), svc.GenerateSessionHash(second))
	})
}

// 重发/回滚同一个对话的最后一条消息，仍是同一个对话，应当留在同一个账号上。
//
// 本用例原先断言"改了最后一条消息就该换哈希"。那等于每次编辑重发都换号，
// 而客户端回传的 thinking 签名属于旧账号，必然被拒。会话身份由对话前缀决定，
// 与最后一条消息无关。
func TestGenerateSessionHash_MessageRollback(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	original := mustParseSessionHashRequest(t, anthropicSessionBody("System prompt", []any{msg("user", "msg1"), msg("assistant", "reply1"), msg("user", "msg2"), msg("assistant", "reply2"), msg("user", "msg3")}, ""), ctx)
	rollback := mustParseSessionHashRequest(t, anthropicSessionBody("System prompt", []any{msg("user", "msg1"), msg("assistant", "reply1"), msg("user", "msg2"), msg("assistant", "reply2"), msg("user", "different msg3")}, ""), ctx)

	hOrig := svc.GenerateSessionHash(original)
	hRollback := svc.GenerateSessionHash(rollback)
	require.Equal(t, hOrig, hRollback, "回滚重发仍是同一个对话，必须留在同一个账号")
}

func TestGenerateSessionHash_MessageRollbackSameContent(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	body := anthropicSessionBody("System prompt", []any{msg("user", "msg1"), msg("assistant", "reply1"), msg("user", "msg2")}, "")
	mk := func() *ParsedRequest { return mustParseSessionHashRequest(t, body, ctx) }

	h1 := svc.GenerateSessionHash(mk())
	h2 := svc.GenerateSessionHash(mk())
	require.Equal(t, h1, h2, "rollback and resend same content should produce same hash")
}

func TestGenerateSessionHash_SameSystemDifferentUsers(t *testing.T) {
	svc := &GatewayService{}
	user1 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a code reviewer.", []any{msg("user", "Review this Go code")}, ""), &SessionContext{ClientIP: "1.1.1.1", UserAgent: "vscode", APIKeyID: 1})
	user2 := mustParseSessionHashRequest(t, anthropicSessionBody("You are a code reviewer.", []any{msg("user", "Review this Python code")}, ""), &SessionContext{ClientIP: "2.2.2.2", UserAgent: "vscode", APIKeyID: 2})

	h1 := svc.GenerateSessionHash(user1)
	h2 := svc.GenerateSessionHash(user2)
	require.NotEqual(t, h1, h2, "different users with different messages should get different hashes")
}

func TestGenerateSessionHash_SameSystemSameMessageDifferentContext(t *testing.T) {
	svc := &GatewayService{}
	body := anthropicSessionBody("You are a helpful assistant.", []any{msg("user", "hello")}, "")
	user1 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: "Mozilla/5.0", APIKeyID: 10})
	user2 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "2.2.2.2", UserAgent: "Mozilla/5.0", APIKeyID: 20})

	h1 := svc.GenerateSessionHash(user1)
	h2 := svc.GenerateSessionHash(user2)
	require.NotEqual(t, h1, h2, "CRITICAL: same system+messages but different users should get different hashes")
}

func TestGenerateSessionHash_SessionContext_IPDifference(t *testing.T) {
	svc := &GatewayService{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	base := func(ip string) *ParsedRequest {
		return mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: ip, UserAgent: "same-ua", APIKeyID: 1})
	}

	h1 := svc.GenerateSessionHash(base("1.1.1.1"))
	h2 := svc.GenerateSessionHash(base("2.2.2.2"))
	require.NotEqual(t, h1, h2, "different IP should produce different hash")
}

func TestGenerateSessionHash_SessionContext_UADifference(t *testing.T) {
	svc := &GatewayService{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	base := func(ua string) *ParsedRequest {
		return mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: ua, APIKeyID: 1})
	}

	h1 := svc.GenerateSessionHash(base("Mozilla/5.0"))
	h2 := svc.GenerateSessionHash(base("curl/7.0"))
	require.NotEqual(t, h1, h2, "different User-Agent should produce different hash")
}

func TestGenerateSessionHash_SessionContext_UAVersionNoiseIgnored(t *testing.T) {
	svc := &GatewayService{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	base := func(ua string) *ParsedRequest {
		return mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: ua, APIKeyID: 1})
	}

	h1 := svc.GenerateSessionHash(base("Mozilla/5.0 codex_cli_rs/0.1.0"))
	h2 := svc.GenerateSessionHash(base("Mozilla/5.0 codex_cli_rs/0.1.1"))
	require.Equal(t, h1, h2, "version-only User-Agent changes should not perturb the sticky session hash")
}

func TestGenerateSessionHash_SessionContext_FreeformUAVersionNoiseIgnored(t *testing.T) {
	svc := &GatewayService{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	base := func(ua string) *ParsedRequest {
		return mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: ua, APIKeyID: 1})
	}

	h1 := svc.GenerateSessionHash(base("Codex CLI 0.1.0"))
	h2 := svc.GenerateSessionHash(base("Codex CLI 0.1.1"))
	require.Equal(t, h1, h2, "free-form version-only User-Agent changes should not perturb the sticky session hash")
}

func TestGenerateSessionHash_SessionContext_APIKeyIDDifference(t *testing.T) {
	svc := &GatewayService{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	base := func(keyID int64) *ParsedRequest {
		return mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: "same-ua", APIKeyID: keyID})
	}

	h1 := svc.GenerateSessionHash(base(1))
	h2 := svc.GenerateSessionHash(base(2))
	require.NotEqual(t, h1, h2, "different APIKeyID should produce different hash")
}

func TestGenerateSessionHash_MultipleUsersSameFirstMessage(t *testing.T) {
	svc := &GatewayService{}
	hashes := make(map[string]bool)
	body := anthropicSessionBody(nil, []any{msg("user", "hello")}, "")
	for i := 0; i < 5; i++ {
		parsed := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "192.168.1." + string(rune('1'+i)), UserAgent: "client-" + string(rune('A'+i)), APIKeyID: int64(i + 1)})
		h := svc.GenerateSessionHash(parsed)
		require.NotEmpty(t, h)
		require.False(t, hashes[h], "hash collision detected for user %d", i)
		hashes[h] = true
	}
	require.Len(t, hashes, 5, "5 different users should produce 5 unique hashes")
}

// 对话逐轮增长时会话身份必须不变——这正是粘性会话存在的意义。
// 原用例断言"每一轮都该换哈希"，见 TestGenerateSessionHash_ContinuousConversation_HashStableAcrossRounds
// 的说明：那是 2026-07-31 生产故障（签名错误率 35%）的直接成因。
func TestGenerateSessionHash_SameUserGrowingConversation(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "browser", APIKeyID: 42}
	messages := []any{
		msg("user", "msg1"), msg("assistant", "reply1"), msg("user", "msg2"), msg("assistant", "reply2"),
		msg("user", "msg3"), msg("assistant", "reply3"), msg("user", "msg4"),
	}

	prevHash := ""
	for round := 1; round <= len(messages); round += 2 {
		parsed := mustParseSessionHashRequest(t, anthropicSessionBody("System", messages[:round], ""), ctx)
		h := svc.GenerateSessionHash(parsed)
		require.NotEmpty(t, h, "round %d hash should not be empty", round)
		if prevHash != "" {
			require.Equal(t, prevHash, h, "round %d 与上一轮必须是同一个会话身份", round)
		}
		prevHash = h
		h2 := svc.GenerateSessionHash(parsed)
		require.Equal(t, h, h2, "retry of round %d should produce same hash", round)
	}
}

// 改动对话中段的某条消息不改变会话身份：会话由前缀（system + 头一条消息）决定。
// 原用例断言"改任何一条消息都该换哈希"，那会让上下文压缩、消息编辑、重试等
// 常见操作全都触发换号。
func TestGenerateSessionHash_MultipleUserMessages(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "first"), msg("user", "second"), msg("user", "third"), msg("user", "fourth"), msg("user", "fifth")}, ""), ctx)
	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h)

	parsed2 := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "first"), msg("user", "CHANGED"), msg("user", "third"), msg("user", "fourth"), msg("user", "fifth")}, ""), ctx)
	h2 := svc.GenerateSessionHash(parsed2)
	require.Equal(t, h, h2, "中段消息变化不改变会话身份")

	// 反向保护：头一条消息不同就是不同的对话，必须分得开。
	parsed3 := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "DIFFERENT OPENING"), msg("user", "second")}, ""), ctx)
	require.NotEqual(t, h, svc.GenerateSessionHash(parsed3), "开场白不同即不同对话")
}

func TestGenerateSessionHash_MessageOrderMatters(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	parsed1 := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "alpha"), msg("user", "beta")}, ""), ctx)
	parsed2 := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", "beta"), msg("user", "alpha")}, ""), ctx)

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h1, h2, "message order should affect the hash")
}

func TestGenerateSessionHash_StructuredContent(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	content := []any{map[string]any{"type": "text", "text": "Look at this"}, map[string]any{"type": "text", "text": "And this too"}}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{msg("user", content)}, ""), ctx)

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "structured content should produce a hash")
}

func TestGenerateSessionHash_ArraySystemPrompt(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	system := []any{map[string]any{"type": "text", "text": "You are a helpful assistant."}, map[string]any{"type": "text", "text": "Be concise."}}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(system, []any{msg("user", "hello")}, ""), ctx)

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "array system prompt should produce a hash")
}

// 有 messages 时，会话身份必须把客户端上下文算进去。
//
// 原用例断言"cache_control 优先级更高，SessionContext 不该影响结果"——那意味着
// 两个**不同的下游客户**（不同 IP、不同 api key）只要 system 相同就共用一个会话，
// 进而共用一个上游账号。这与「一个号 = 一个客户端」直接冲突：上游会看到同一个
// 账号被两个客户端同时使用，而这正是最快招致封号的模式。
//
// cache_control 锚点路径仍然保留，但只用于没有 messages 的 responses 协议
// （见 TestGenerateSessionHash_ResponsesInputDoesNotOverrideHigherPrioritySources）。
func TestGenerateSessionHash_CacheControlDoesNotMergeDistinctClients(t *testing.T) {
	svc := &GatewayService{}
	system := []any{map[string]any{"type": "text", "text": "You are a tool-specific assistant.", "cache_control": map[string]any{"type": "ephemeral"}}}
	body := anthropicSessionBody(system, []any{msg("user", "hello")}, "")
	parsed1 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: "ua1", APIKeyID: 100})
	parsed2 := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "2.2.2.2", UserAgent: "ua2", APIKeyID: 200})

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h1, h2, "不同下游客户不得共用会话身份，否则一个上游账号会被多客户端同时使用")
}

func TestGenerateSessionHash_EmptyMessages(t *testing.T) {
	svc := &GatewayService{}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{}, ""), &SessionContext{ClientIP: "1.1.1.1", UserAgent: "test", APIKeyID: 1})

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "empty messages with SessionContext should still produce a hash from context")
}

func TestGenerateSessionHash_EmptyMessagesNoContext(t *testing.T) {
	svc := &GatewayService{}
	parsed := mustParseSessionHashRequest(t, anthropicSessionBody(nil, []any{}, ""), nil)

	h := svc.GenerateSessionHash(parsed)
	require.Empty(t, h, "empty messages without SessionContext should produce empty hash")
}

func TestGenerateSessionHash_SessionContextWithEmptyFields(t *testing.T) {
	svc := &GatewayService{}
	body := anthropicSessionBody(nil, []any{msg("user", "test")}, "")
	withEmptyCtx := mustParseSessionHashRequest(t, body, &SessionContext{ClientIP: "", UserAgent: "", APIKeyID: 0})
	withoutCtx := mustParseSessionHashRequest(t, body, nil)

	h1 := svc.GenerateSessionHash(withEmptyCtx)
	h2 := svc.GenerateSessionHash(withoutCtx)
	require.NotEqual(t, h1, h2, "empty-field SessionContext should still differ from nil SessionContext")
}

// 长对话继续追加消息，会话身份不变。
func TestGenerateSessionHash_LongConversation(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "test", APIKeyID: 1}
	messages := make([]any, 0, 40)
	for i := 0; i < 20; i++ {
		messages = append(messages, msg("user", "user message "+string(rune('A'+i))))
		messages = append(messages, msg("assistant", "assistant reply "+string(rune('A'+i))))
	}

	parsed := mustParseSessionHashRequest(t, anthropicSessionBody("System prompt", messages, ""), ctx)
	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h)

	moreMessages := append(append([]any{}, messages...), msg("user", "one more"), msg("assistant", "ok"))
	parsed2 := mustParseSessionHashRequest(t, anthropicSessionBody("System prompt", moreMessages, ""), ctx)
	h2 := svc.GenerateSessionHash(parsed2)
	require.Equal(t, h, h2, "长对话追加消息不改变会话身份")
}

func TestGenerateSessionHash_GeminiContentsProducesHash(t *testing.T) {
	svc := &GatewayService{}
	parsed := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "Hello from Gemini")}), &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1})

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "Gemini contents with parts should produce a non-empty hash")
}

func TestGenerateSessionHash_GeminiDifferentContentsDifferentHash(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	parsed1 := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "Hello")}), ctx)
	parsed2 := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "Goodbye")}), ctx)

	h1 := svc.GenerateSessionHash(parsed1)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h1, h2, "different Gemini contents should produce different hashes")
}

func TestGenerateSessionHash_GeminiSameContentsSameHash(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	body := geminiSessionBody(nil, []any{geminiMsg("user", "Hello"), geminiMsg("model", "Hi there!")})
	mk := func() *ParsedRequest { return mustParseGeminiSessionHashRequest(t, body, ctx) }

	h1 := svc.GenerateSessionHash(mk())
	h2 := svc.GenerateSessionHash(mk())
	require.Equal(t, h1, h2, "same Gemini contents should produce identical hash")
}

func TestGenerateSessionHash_GeminiMultiTurnHashChanges(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	round1 := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "hello")}), ctx)
	round2 := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "hello"), geminiMsg("model", "Hi!"), geminiMsg("user", "How are you?")}), ctx)

	h1 := svc.GenerateSessionHash(round1)
	h2 := svc.GenerateSessionHash(round2)
	require.NotEmpty(t, h1)
	require.NotEmpty(t, h2)
	require.NotEqual(t, h1, h2, "Gemini multi-turn should produce different hashes per round")
}

func TestGenerateSessionHash_GeminiDifferentUsersSameContentDifferentHash(t *testing.T) {
	svc := &GatewayService{}
	body := geminiSessionBody(nil, []any{geminiMsg("user", "hello")})
	user1 := mustParseGeminiSessionHashRequest(t, body, &SessionContext{ClientIP: "1.1.1.1", UserAgent: "gemini-cli", APIKeyID: 10})
	user2 := mustParseGeminiSessionHashRequest(t, body, &SessionContext{ClientIP: "2.2.2.2", UserAgent: "gemini-cli", APIKeyID: 20})

	h1 := svc.GenerateSessionHash(user1)
	h2 := svc.GenerateSessionHash(user2)
	require.NotEqual(t, h1, h2, "CRITICAL: different Gemini users with same content must get different hashes")
}

func TestGenerateSessionHash_GeminiSystemInstructionAffectsHash(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	withSys := mustParseGeminiSessionHashRequest(t, geminiSessionBody([]any{map[string]any{"text": "You are a coding assistant."}}, []any{geminiMsg("user", "hello")}), ctx)
	withoutSys := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "hello")}), ctx)

	h1 := svc.GenerateSessionHash(withSys)
	h2 := svc.GenerateSessionHash(withoutSys)
	require.NotEqual(t, h1, h2, "systemInstruction should affect the hash")
}

func TestGenerateSessionHash_GeminiMultiPartMessage(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	parsed := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "Part 1", "Part 2", "Part 3")}), ctx)
	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "multi-part Gemini message should produce a hash")

	parsed2 := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, []any{geminiMsg("user", "Part 1", "CHANGED", "Part 3")}), ctx)
	h2 := svc.GenerateSessionHash(parsed2)
	require.NotEqual(t, h, h2, "changing a part should change the hash")
}

func TestGenerateSessionHash_GeminiNonTextPartsIgnored(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "1.2.3.4", UserAgent: "gemini-cli", APIKeyID: 1}
	content := []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Describe this image"}, map[string]any{"inline_data": map[string]any{"mime_type": "image/png", "data": "base64..."}}}}}
	parsed := mustParseGeminiSessionHashRequest(t, geminiSessionBody(nil, content), ctx)

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "Gemini message with mixed parts should still produce a hash from text parts")
}

func TestGenerateSessionHash_GeminiMultiTurnHashNotSticky(t *testing.T) {
	svc := &GatewayService{}
	ctx := &SessionContext{ClientIP: "10.0.0.1", UserAgent: "gemini-cli", APIKeyID: 42}
	rounds := []string{
		geminiSessionBody([]any{map[string]any{"text": "You are a coding assistant."}}, []any{geminiMsg("user", "Write a Go function")}),
		geminiSessionBody([]any{map[string]any{"text": "You are a coding assistant."}}, []any{geminiMsg("user", "Write a Go function"), geminiMsg("model", "func hello() {}"), geminiMsg("user", "Add error handling")}),
		geminiSessionBody([]any{map[string]any{"text": "You are a coding assistant."}}, []any{geminiMsg("user", "Write a Go function"), geminiMsg("model", "func hello() {}"), geminiMsg("user", "Add error handling"), geminiMsg("model", "func hello() error { return nil }"), geminiMsg("user", "Now add tests")}),
	}

	hashes := make([]string, len(rounds))
	for i, body := range rounds {
		parsed := mustParseGeminiSessionHashRequest(t, body, ctx)
		hashes[i] = svc.GenerateSessionHash(parsed)
		require.NotEmpty(t, hashes[i], "round %d hash should not be empty", i+1)
	}
	require.NotEqual(t, hashes[0], hashes[1], "round 1 vs 2 hash should differ (contents grow)")
	require.NotEqual(t, hashes[1], hashes[2], "round 2 vs 3 hash should differ (contents grow)")
	require.NotEqual(t, hashes[0], hashes[2], "round 1 vs 3 hash should differ")

	parsedAgain := mustParseGeminiSessionHashRequest(t, rounds[1], ctx)
	h2Again := svc.GenerateSessionHash(parsedAgain)
	require.Equal(t, hashes[1], h2Again, "retry of same round should produce same hash")
}

func TestGenerateSessionHash_GeminiEndToEnd(t *testing.T) {
	svc := &GatewayService{}
	body := geminiSessionBody([]any{map[string]any{"text": "You are a coding assistant."}}, []any{geminiMsg("user", "Write a Go function"), geminiMsg("model", "Here is a function..."), geminiMsg("user", "Now add error handling")})
	parsed := mustParseGeminiSessionHashRequest(t, body, &SessionContext{ClientIP: "10.0.0.1", UserAgent: "gemini-cli/1.0", APIKeyID: 42})

	h := svc.GenerateSessionHash(parsed)
	require.NotEmpty(t, h, "end-to-end Gemini flow should produce a hash")

	parsed2 := mustParseGeminiSessionHashRequest(t, body, &SessionContext{ClientIP: "10.0.0.1", UserAgent: "gemini-cli/1.0", APIKeyID: 42})
	h2 := svc.GenerateSessionHash(parsed2)
	require.Equal(t, h, h2, "same request should produce same hash")

	parsed3 := mustParseGeminiSessionHashRequest(t, body, &SessionContext{ClientIP: "10.0.0.2", UserAgent: "gemini-cli/1.0", APIKeyID: 99})
	h3 := svc.GenerateSessionHash(parsed3)
	require.NotEqual(t, h, h3, "different user with same Gemini request should get different hash")
}
