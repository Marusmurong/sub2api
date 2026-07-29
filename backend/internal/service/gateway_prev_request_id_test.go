package service

import (
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUpstreamRequestIDPrefersAnthropicHeader(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   string
	}{
		{
			name:   "Anthropic 的 request-id",
			header: http.Header{"Request-Id": []string{"req_011CTabc"}},
			want:   "req_011CTabc",
		},
		{
			name:   "回退 x-request-id",
			header: http.Header{"X-Request-Id": []string{"req_xyz"}},
			want:   "req_xyz",
		},
		{
			name: "两者都有时以 request-id 为准",
			header: http.Header{
				"Request-Id":   []string{"req_primary"},
				"X-Request-Id": []string{"req_alias"},
			},
			want: "req_primary",
		},
		{name: "都没有", header: http.Header{}, want: ""},
		{name: "nil header", header: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upstreamRequestID(tt.header); got != tt.want {
				t.Errorf("upstreamRequestID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCCPrevReqSessionID(t *testing.T) {
	body := []byte(`{"metadata":{"user_id":"{\"device_id\":\"d\",\"account_uuid\":\"a\",\"session_id\":\"6fb64a13-ce54-499e-8ae1-63aca9e5d2d6\"}"}}`)
	if got := ccPrevReqSessionID(body); got != "6fb64a13-ce54-499e-8ae1-63aca9e5d2d6" {
		t.Errorf("ccPrevReqSessionID() = %q", got)
	}

	if got := ccPrevReqSessionID([]byte(`{"messages":[]}`)); got != "" {
		t.Errorf("无 metadata 时应返回空，得到 %q", got)
	}
	if got := ccPrevReqSessionID([]byte(`{"metadata":{"user_id":"garbage"}}`)); got != "" {
		t.Errorf("无法解析时应返回空，得到 %q", got)
	}
}

func TestEnsureBillingHeaderPrevReq(t *testing.T) {
	const base = "x-anthropic-billing-header: cc_version=2.1.220.7bd; cc_entrypoint=cli; cch=00000;"

	tests := []struct {
		name string
		in   string
		prev string
		want string
	}{
		{
			name: "追加在末尾（对齐 k7n 的拼接顺序）",
			in:   base,
			prev: "req_011CTabc",
			want: base + " cc_prev_req=req_011CTabc;",
		},
		{
			name: "会话首轮不发",
			in:   base,
			prev: "",
			want: base,
		},
		{
			name: "已存在则不覆盖（透传路径以客户端为准）",
			in:   base + " cc_prev_req=req_client;",
			prev: "req_ours",
			want: base + " cc_prev_req=req_client;",
		},
		{
			// request id 来自上游响应头，会被拼进 system 文本，属跨信任边界数据。
			name: "含分号的值被拒绝，防止改变 block 结构",
			in:   base,
			prev: "req_abc; cc_entrypoint=evil",
			want: base,
		},
		{
			name: "含换行的值被拒绝",
			in:   base,
			prev: "req_abc\nx-anthropic-billing-header: fake",
			want: base,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ensureBillingHeaderPrevReq(tt.in, tt.prev); got != tt.want {
				t.Errorf("ensureBillingHeaderPrevReq()\n got  = %q\n want = %q", got, tt.want)
			}
		})
	}
}

// 完整字段顺序：cc_version; cc_entrypoint; cch; cc_prev_req;
func TestNormalizeBillingHeaderBlockFieldOrder(t *testing.T) {
	body := billingBody(t,
		"x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli;",
		"hello world this is a test message")

	out := normalizeBillingHeaderBlock(body, "claude-cli/2.1.220 (external, cli)", false, "req_011CTabc")
	got := gjson.GetBytes(out, "system.0.text").String()

	want := "x-anthropic-billing-header: cc_version=2.1.220.abc; cc_entrypoint=cli; cch=00000; cc_prev_req=req_011CTabc;"
	if got != want {
		t.Errorf("字段顺序不符\n got  = %q\n want = %q", got, want)
	}
}

// lookupPrevRequestID 在缓存未实现 PrevRequestIDStore 时静默降级，不得 panic。
func TestLookupPrevRequestIDDegradesWithoutStore(t *testing.T) {
	svc := &GatewayService{}
	if got := svc.lookupPrevRequestID(t.Context(), &Account{ID: 1}, "session"); got != "" {
		t.Errorf("无 store 时应返回空，得到 %q", got)
	}
	// 空会话标识同样不查
	if got := svc.lookupPrevRequestID(t.Context(), &Account{ID: 1}, ""); got != "" {
		t.Errorf("空 sessionID 应返回空，得到 %q", got)
	}
}
