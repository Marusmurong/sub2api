//go:build unit

package service

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

// 信封元数据必须与真实客户端同形。
//
// 依据是 2026-09-23 抓到的真值（reclaude-lab/sink/dump 里的 *proxy.txt，device 44145）：
// 顶层恒为 url / method / headers / traceId / edge / keepalive 六项，headers 里带
// host 与 content-length。此前我们漏了三项 —— Go 把 Host/ContentLength 放在结构体
// 字段而不是 Header map，edge/keepalive 又是 omitempty，不显式赋值就整个消失。
// 对端拿 headers 原样重建上游请求，缺了会被拒为「reclaude 客户端状态异常」。
func TestBuildReclaudeEnvelope_MatchesRealClientShape(t *testing.T) {
	innerBody := []byte(`{"model":"claude-opus-5","messages":[]}`)
	inner, err := http.NewRequest(http.MethodPost,
		"https://api.anthropic.com/v1/messages?beta=true", bytes.NewReader(innerBody))
	if err != nil {
		t.Fatal(err)
	}
	inner.Header.Set("Content-Type", "application/json")
	inner.Header.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")

	envelope, traceID, err := buildReclaudeEnvelope(inner, "https://www.reclaude.ai")
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	if traceID == "" {
		t.Fatal("traceId 不能为空")
	}

	metaLen := int(binary.BigEndian.Uint32(envelope[:reclaude.EnvelopeLengthPrefixBytes]))
	var meta map[string]any
	if err := json.Unmarshal(
		envelope[reclaude.EnvelopeLengthPrefixBytes:reclaude.EnvelopeLengthPrefixBytes+metaLen], &meta); err != nil {
		t.Fatalf("解析元数据: %v", err)
	}

	for _, key := range []string{"url", "method", "headers", "traceId", "edge", "keepalive"} {
		if _, ok := meta[key]; !ok {
			t.Fatalf("元数据缺字段 %q（真实客户端六项齐全）: %v", key, meta)
		}
	}
	// 🔴 edge 恒为 "unknown"。
	//
	// 真客户端的 computeEdgeLabel（📄 反编译）是一次 map 查表：命中返回 u.Host，
	// 未命中返回 "unknown"。表里装的是 route 节点，login 拿到的默认 gateway
	// （www.reclaude.ai）不在其中。
	//
	// ✅ 实测：reclaude-lab/sink/dump 的 26/26 条真实信封 edge 全是 "unknown"。
	//
	// 当天曾据一条 daemon 指向本地 sink 时的样本把断言改成主机名 —— 那不是常态，
	// 方向是错的，已撤回。
	if meta["edge"] != "unknown" {
		t.Fatalf("edge 应恒为 unknown，得到 %v", meta["edge"])
	}
	if meta["keepalive"] != true {
		t.Fatalf("keepalive 应为 true，得到 %v", meta["keepalive"])
	}

	headers, _ := meta["headers"].(map[string]any)
	if got := headers["host"]; got != "api.anthropic.com" {
		t.Fatalf("headers.host 应为 api.anthropic.com，得到 %v", got)
	}
	if got := headers["content-length"]; got != strconv.Itoa(len(innerBody)) {
		t.Fatalf("headers.content-length 应为内层 body 长度 %d，得到 %v", len(innerBody), got)
	}

	// 内层 body 仍须逐字节原样透传。
	gotBody := envelope[reclaude.EnvelopeLengthPrefixBytes+metaLen:]
	if !bytes.Equal(gotBody, innerBody) {
		t.Fatalf("内层 body 被改动了: %s", gotBody)
	}

	// 读干净，避免测试之间共享 reader 状态。
	_, _ = io.Copy(io.Discard, bytes.NewReader(nil))
}
