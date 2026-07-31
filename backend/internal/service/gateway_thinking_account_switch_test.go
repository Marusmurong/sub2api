package service

import (
	"strings"
	"testing"
)

// 跨账号复用会话时，历史 thinking 块的签名由前一个账号签发，当前账号必然拒绝
// （生产 2026-07-28 每天约 595 次 "Invalid `signature` in `thinking` block"）。
// 这些用例覆盖"何时该剥离"的判定，剥离本身复用既有的 FilterThinkingBlocksForRetry。

func TestStripThinkingForAccountSwitch_Condition(t *testing.T) {
	const body = `{"model":"claude-opus-4-8","messages":[
		{"role":"assistant","content":[{"type":"thinking","thinking":"reasoning here","signature":"sig-from-account-A"}]},
		{"role":"user","content":"continue"}]}`

	tests := []struct {
		name        string
		bound       int64
		selected    int64
		wantApplied bool
		reason      string
	}{
		{
			name: "账号切换时剥离", bound: 12, selected: 13,
			wantApplied: true, reason: "签名由账号 12 签发,账号 13 必然拒绝",
		},
		{
			name: "同账号不动", bound: 12, selected: 12,
			wantApplied: false, reason: "签名有效,且剥离会打掉 prompt 缓存命中",
		},
		{
			name: "无粘性绑定不动", bound: 0, selected: 13,
			wantApplied: false, reason: "首次请求,无历史账号",
		},
		{
			name: "绑定为负值不动", bound: -1, selected: 13,
			wantApplied: false, reason: "防御非法值",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, applied := stripThinkingForAccountSwitch([]byte(body), "claude-opus-4-8", tt.bound, tt.selected)

			if applied != tt.wantApplied {
				t.Fatalf("applied = %v, want %v (%s)", applied, tt.wantApplied, tt.reason)
			}
			if !applied && string(got) != body {
				t.Errorf("未触发时必须原样返回 body")
			}
			if applied && strings.Contains(string(got), "sig-from-account-A") {
				t.Errorf("触发后不得残留跨账号签名: %s", got)
			}
		})
	}
}

func TestStripThinkingForAccountSwitch_PreservesNonThinking(t *testing.T) {
	// 无 thinking 块的请求即使账号切换也不该被改动
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`)

	got, applied := stripThinkingForAccountSwitch(body, "claude-opus-4-8", 12, 13)

	if applied {
		t.Errorf("applied = true, want false（无 thinking 块无需剥离）")
	}
	if string(got) != string(body) {
		t.Errorf("body 不应被改动: %s", got)
	}
}

func TestStripThinkingForAccountSwitch_PassbackRequiredUpstream(t *testing.T) {
	// passback-required 上游（DeepSeek/Kimi/GLM 等）要求历史 thinking 原样回传，
	// 剥离反而制造 400。FilterThinkingBlocksForRetry 内部按 ShouldApplyRetryFilters 把关，
	// 这里确认该约束在账号切换路径上同样成立。
	body := []byte(`{"model":"deepseek-chat","messages":[
		{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"s"}]}]}`)

	_, applied := stripThinkingForAccountSwitch(body, "deepseek-chat", 12, 13)

	if applied {
		t.Errorf("applied = true, want false（passback-required 上游不得剥离）")
	}
}

func TestStripThinkingForAccountSwitch_EmptyAndInvalidBody(t *testing.T) {
	if _, applied := stripThinkingForAccountSwitch(nil, "claude-opus-4-8", 12, 13); applied {
		t.Errorf("nil body: applied = true, want false")
	}
	if _, applied := stripThinkingForAccountSwitch([]byte(`not json`), "claude-opus-4-8", 12, 13); applied {
		t.Errorf("invalid json: applied = true, want false")
	}
}

// resolveSignatureOwnerAccountID 的用例。
//
// 生产数据（2026-07-31，一天内）：前置剥离命中 166 次，仍有 175 次发到上游才报
// "Invalid `signature` in `thinking` block"，其中 16 次连重试都没救回来、直接漏给
// 了客户端。这 175 次只能来自"粘性绑定已不存在"——绑定还在且账号相同则签名本就有
// 效，绑定还在且账号不同则早被前置剥离了。绑定恰恰是在账号 429/被驱逐时删掉的，
// 于是我们在最需要"上一轮是谁签的"这条信息时把它扔了。
func TestResolveSignatureOwnerAccountID(t *testing.T) {
	tests := []struct {
		name     string
		sticky   int64
		recorded int64
		want     int64
		reason   string
	}{
		{
			name: "粘性绑定优先", sticky: 12, recorded: 9, want: 12,
			reason: "绑定活着就是当前事实,签名归属记录可能更旧",
		},
		{
			name: "绑定已被清理时退回归属记录", sticky: 0, recorded: 9, want: 9,
			reason: "账号 429/驱逐后绑定被删,但历史签名仍是账号 9 签的",
		},
		{
			name: "两者都没有", sticky: 0, recorded: 0, want: 0,
			reason: "首次请求,无历史账号",
		},
		{
			name: "非法值一律不采信", sticky: -1, recorded: -5, want: 0,
			reason: "防御性:负值不得被当成账号 ID 参与剥离判定",
		},
		{
			name: "粘性非法时退回归属记录", sticky: -1, recorded: 9, want: 9,
			reason: "单个来源非法不应连累另一个",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSignatureOwnerAccountID(tt.sticky, tt.recorded); got != tt.want {
				t.Errorf("resolveSignatureOwnerAccountID(%d, %d) = %d, want %d (%s)",
					tt.sticky, tt.recorded, got, tt.want, tt.reason)
			}
		})
	}
}

// 归属记录接上剥离判定后的端到端行为：绑定已被清理，但记录说上一轮是别的账号，
// 必须在发出前就剥离，而不是发出去吃一个 400 再重试。
func TestStripThinkingForAccountSwitch_UsesRecordedOwnerWhenStickyGone(t *testing.T) {
	const body = `{"model":"claude-opus-4-8","messages":[
		{"role":"assistant","content":[{"type":"thinking","thinking":"r","signature":"sig-from-account-9"}]},
		{"role":"user","content":"continue"}]}`

	owner := resolveSignatureOwnerAccountID(0, 9)
	got, applied := stripThinkingForAccountSwitch([]byte(body), "claude-opus-4-8", owner, 13)

	if !applied {
		t.Fatalf("applied = false, want true（绑定已清理但记录显示换了账号,必须前置剥离）")
	}
	if strings.Contains(string(got), "sig-from-account-9") {
		t.Errorf("剥离后不得残留跨账号签名: %s", got)
	}
}

// 反向保护：记录显示还是同一个账号时不得剥离——剥离会打掉 prompt 缓存前缀命中，
// 推高 token 消耗，反过来加剧 429。
func TestStripThinkingForAccountSwitch_RecordedOwnerSameAccount(t *testing.T) {
	const body = `{"model":"claude-opus-4-8","messages":[
		{"role":"assistant","content":[{"type":"thinking","thinking":"r","signature":"sig-from-account-13"}]}]}`

	owner := resolveSignatureOwnerAccountID(0, 13)
	got, applied := stripThinkingForAccountSwitch([]byte(body), "claude-opus-4-8", owner, 13)

	if applied {
		t.Errorf("applied = true, want false（同账号签名有效,剥离只会白白打掉缓存）")
	}
	if string(got) != body {
		t.Errorf("未触发时必须原样返回 body")
	}
}

// 会话「签名已污染」标记的判定。
//
// 生产实测（2026-07-31 09:22 之后）：残留的签名错误全部落在 content.16 / content.58 /
// content.101 这类深处下标——上游校验历史里的**每一个** thinking 块，不只是最近一轮。
//
// 推论：一个对话只要换过一次账号，历史里就永久混有别的账号签发的签名，之后每一轮
// 都会被拒、每一轮都靠 400 重试救回（重试成功率 95%，但每次都多一轮上游往返，
// 且每次都在账号上留下一个异常请求）。
//
// 签名归属（sig_owner）救不了这个：它只记最近一次绑定，而历史里可能混着好几个账号。
// 能救的是「这个会话曾经换过账号」这一位状态——一旦置位就一直前置剥离。
func TestShouldPreStripThinking(t *testing.T) {
	tests := []struct {
		name     string
		owner    int64
		selected int64
		tainted  bool
		want     bool
		reason   string
	}{
		{
			name: "换账号", owner: 12, selected: 13, tainted: false, want: true,
			reason: "本轮就在换号,历史签名必然被拒",
		},
		{
			name: "同账号但已污染", owner: 13, selected: 13, tainted: true, want: true,
			reason: "这个会话以前换过号,历史深处仍混有别的账号的签名",
		},
		{
			name: "同账号且未污染", owner: 13, selected: 13, tainted: false, want: false,
			reason: "全程同一个账号,签名有效,剥离只会白白打掉缓存前缀",
		},
		{
			name: "无归属但已污染", owner: 0, selected: 13, tainted: true, want: true,
			reason: "归属记录过期不影响已污染这个事实",
		},
		{
			name: "无归属且未污染", owner: 0, selected: 13, tainted: false, want: false,
			reason: "首次请求,无历史账号",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPreStripThinking(tt.owner, tt.selected, tt.tainted); got != tt.want {
				t.Errorf("shouldPreStripThinking(%d,%d,%v) = %v, want %v (%s)",
					tt.owner, tt.selected, tt.tainted, got, tt.want, tt.reason)
			}
		})
	}
}

// 已污染的会话即使本轮没换号也要剥离——这是本次改动的全部意义。
func TestStripThinkingForAccountSwitch_TaintedSameAccount(t *testing.T) {
	const body = `{"model":"claude-opus-4-8","messages":[
		{"role":"assistant","content":[{"type":"thinking","thinking":"r","signature":"sig-from-old-account"}]},
		{"role":"user","content":"continue"}]}`

	if !shouldPreStripThinking(13, 13, true) {
		t.Fatal("已污染会话应当剥离")
	}
	got, applied := stripThinkingBlocksBeforeForward([]byte(body), "claude-opus-4-8")
	if !applied {
		t.Fatal("applied = false, want true")
	}
	if strings.Contains(string(got), "sig-from-old-account") {
		t.Errorf("剥离后不得残留旧账号签名: %s", got)
	}
}
