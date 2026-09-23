package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
)

// logEnvelopeShapeOnReject 在 reclaude 网关拒绝时记录我们发出去的信封形状。
//
// 为什么需要：对方的错误文案是给终端用户看的（"reclaude 客户端状态异常，请重启
// reclaude 后重试"），对排查零信息量；code 里那个 bad_envelope 也只说"不对"，
// 不说哪里不对。没有这条日志，只能靠改代码重部署一遍遍试。
//
// 🔴 只记形状，不记内容：URL、method、**头名清单**（不含取值）、meta/body 长度。
// authorization / x-api-key 一类的取值一个字节都不落盘 —— 这条日志会进
// ops_system_logs，而那张表运维随手就能查。
func logEnvelopeShapeOnReject(inner, outer *http.Request, envelope []byte, traceID string, status int) {
	if inner == nil {
		return
	}

	metaLen, bodyLen := envelopeSegmentLengths(envelope)
	url := ""
	if inner.URL != nil {
		url = inner.URL.String()
	}

	// 信封头部字节：4 字节长度前缀 + meta JSON 的开头，足以看出前缀是否正确、
	// 有没有被压缩（gzip 会以 1f8b 开头）。这段里只有 url/method，不含凭据。
	prefix := envelope
	if len(prefix) > 48 {
		prefix = prefix[:48]
	}

	logger.LegacyPrintf("service.reclaude",
		"reclaude gateway rejected envelope: status=%d trace=%s url=%s method=%s host=%q meta_bytes=%d body_bytes=%d envelope_bytes=%d prefix=%x inner_headers=[%s] meta=%s",
		status, traceID, url, inner.Method, inner.Host,
		metaLen, bodyLen, len(envelope), prefix, strings.Join(sortedHeaderNames(inner.Header), " "),
		redactedMetaJSON(envelope, metaLen))

	// 外层请求同样要记：信封没问题时，问题只可能在我们打给网关的这一层。
	if outer != nil {
		outerURL := ""
		if outer.URL != nil {
			outerURL = outer.URL.String()
		}
		sum := sha256.Sum256(envelope)
		logger.LegacyPrintf("service.reclaude",
			"reclaude inner identity: trace=%s metadata.user_id_fields=[%s]",
			traceID, strings.Join(innerUserIDFields(envelope, metaLen, bodyLen), " "))

		logger.LegacyPrintf("service.reclaude",
			"reclaude inner body shape: trace=%s %s",
			traceID, innerBodyShape(envelope, metaLen, bodyLen))

		logger.LegacyPrintf("service.reclaude",
			"reclaude outer request: trace=%s url=%s content_length=%d envelope_sha256=%s outer_headers=[%s]",
			traceID, outerURL, outer.ContentLength,
			base64.RawURLEncoding.EncodeToString(sum[:]),
			strings.Join(sortedHeaderNames(outer.Header), " "))
	}
}

// envelopeSegmentLengths 拆出信封的 meta 与 body 两段长度。
// 拆不动（长度前缀本身就坏了）时返回 (-1, -1)，那本身就是结论。
func envelopeSegmentLengths(envelope []byte) (metaLen, bodyLen int) {
	if len(envelope) < reclaude.EnvelopeLengthPrefixBytes {
		return -1, -1
	}
	declared := int(binary.BigEndian.Uint32(envelope[:reclaude.EnvelopeLengthPrefixBytes]))
	if declared < 0 || reclaude.EnvelopeLengthPrefixBytes+declared > len(envelope) {
		return -1, -1
	}
	// 顺带验一次 meta 能不能解析：解不动说明问题在我们这边，不用再去问对端。
	var probe map[string]json.RawMessage
	if json.Unmarshal(
		envelope[reclaude.EnvelopeLengthPrefixBytes:reclaude.EnvelopeLengthPrefixBytes+declared], &probe) != nil {
		return -1, -1
	}
	return declared, len(envelope) - reclaude.EnvelopeLengthPrefixBytes - declared
}

// sortedHeaderNames 返回排序后的小写头名，便于与真实客户端抓样逐项对照。
func sortedHeaderNames(header http.Header) []string {
	names := make([]string, 0, len(header))
	for name := range header {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	return names
}

// redactedMetaJSON 取出信封的 meta 段，把 authorization 的取值换成长度标记后返回。
//
// 头名清单不足以定位「对方为什么说 bad_envelope」—— 值里的空串、非法字符、
// 多出来的字段才是嫌疑点。这里保留除凭据外的全部取值：它们是 CC 伪装的产物
// （UA / beta 清单 / session id），不是用户内容，信封的 body 一个字节都不落盘。
func redactedMetaJSON(envelope []byte, metaLen int) string {
	if metaLen <= 0 || reclaude.EnvelopeLengthPrefixBytes+metaLen > len(envelope) {
		return "<unparsable>"
	}
	var meta struct {
		URL       string            `json:"url"`
		Method    string            `json:"method"`
		Headers   map[string]string `json:"headers"`
		TraceID   string            `json:"traceId,omitempty"`
		Edge      string            `json:"edge,omitempty"`
		Keepalive bool              `json:"keepalive,omitempty"`
	}
	if json.Unmarshal(envelope[reclaude.EnvelopeLengthPrefixBytes:reclaude.EnvelopeLengthPrefixBytes+metaLen], &meta) != nil {
		return "<unparsable>"
	}
	for name, value := range meta.Headers {
		if strings.EqualFold(name, "authorization") || strings.EqualFold(name, "x-api-key") {
			meta.Headers[name] = fmt.Sprintf("<redacted len=%d prefix=%s>", len(value), safePrefix(value))
		}
	}
	out, err := json.Marshal(meta)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(out)
}

// safePrefix 只保留凭据的类型前缀（"Bearer sk-rec-" 这一段），用于区分
// 「空凭据」「SK」「OAuth token」三种情况，不泄露密钥本体。
func safePrefix(value string) string {
	const keep = 14
	if len(value) <= keep {
		return value
	}
	return value[:keep]
}

// innerUserIDFields 返回内层 body 里 metadata.user_id 的**字段名**清单。
//
// reclaude 网关会校验这一项：缺 account_uuid 时回
// `{"status":400,"statusText":"metadata.user_id missing identity fields"}`，
// 而对外包装成 code=bad_envelope，文案还是"请重启 reclaude"——三层错位，
// 没有这条日志无从下手。只记字段名，device_id / session_id 的取值不落盘。
func innerUserIDFields(envelope []byte, metaLen, bodyLen int) []string {
	if metaLen <= 0 || bodyLen <= 0 {
		return []string{"<no-body>"}
	}
	body := envelope[reclaude.EnvelopeLengthPrefixBytes+metaLen:]
	var inner struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &inner) != nil {
		return []string{"<body-not-json>"}
	}
	raw := strings.TrimSpace(inner.Metadata.UserID)
	if raw == "" {
		return []string{"<user_id-empty>"}
	}
	if !strings.HasPrefix(raw, "{") {
		// 旧拼接格式 user_{device}_account_{uuid}_session_{uuid}。
		return []string{"<legacy-format>"}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return []string{"<user_id-not-json>"}
	}
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// innerBodyShape 返回内层 body 的结构摘要(不含用户内容):顶层字段名、system 块
// 数量与各块类型、messages 条数、第一条 content 类型、是否带 fallbacks/token。
// 用于和「探针能过的极简 body」对比出 sub2api 多注入了什么 —— 2026-09-24 排查:
// 信封/签名/identity 全对,唯一差异在 body。只记结构,不落任何文本取值。
func innerBodyShape(envelope []byte, metaLen, bodyLen int) string {
	if metaLen <= 0 || bodyLen <= 0 {
		return "<no-body>"
	}
	body := envelope[reclaude.EnvelopeLengthPrefixBytes+metaLen:]
	var b map[string]json.RawMessage
	if json.Unmarshal(body, &b) != nil {
		return "<body-not-json len=" + strconv.Itoa(len(body)) + ">"
	}
	top := make([]string, 0, len(b))
	for k := range b {
		top = append(top, k)
	}
	sort.Strings(top)

	sysShape := "-"
	if rawSys, ok := b["system"]; ok {
		var arr []map[string]json.RawMessage
		if json.Unmarshal(rawSys, &arr) == nil {
			types := make([]string, 0, len(arr))
			for _, blk := range arr {
				t := "?"
				if rt, ok := blk["type"]; ok {
					var ts string
					if json.Unmarshal(rt, &ts) == nil {
						t = ts
					}
				}
				if _, hasCC := blk["cache_control"]; hasCC {
					t += "+cc"
				}
				types = append(types, t)
			}
			sysShape = fmt.Sprintf("array[%d]{%s}", len(arr), strings.Join(types, ","))
		} else {
			sysShape = "string"
		}
	}

	msgCount := -1
	if rawMsgs, ok := b["messages"]; ok {
		var arr []json.RawMessage
		if json.Unmarshal(rawMsgs, &arr) == nil {
			msgCount = len(arr)
		}
	}

	_, hasFallbacks := b["fallbacks"]
	_, hasFallbackTok := b["fallback_credit_token"]

	return fmt.Sprintf("top=[%s] system=%s messages=%d fallbacks=%v fallback_token=%v",
		strings.Join(top, " "), sysShape, msgCount, hasFallbacks, hasFallbackTok)
}

// reclaudeEnvelopeDumpEnv 指定一个目录，把被拒的信封**完整字节**写进去。
//
// 🔴 只在排障时临时打开，用完必须清掉环境变量并删除转储文件：
// 落盘的是完整信封，内含 authorization（SK）与全量明文 prompt。
// 与 logEnvelopeShapeOnReject 分开正是因为那条日志刻意不记内容 ——
// 常态下不该有任何一处把这些写进磁盘。
const reclaudeEnvelopeDumpEnv = "SUB2API_DEBUG_RECLAUDE_ENVELOPE_DIR"

// dumpRejectedEnvelope 在配置了目录时把信封原样落盘，供离线逐字节比对。
func dumpRejectedEnvelope(envelope []byte, traceID string) {
	dir := strings.TrimSpace(os.Getenv(reclaudeEnvelopeDumpEnv))
	if dir == "" || len(envelope) == 0 {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	name := filepath.Join(dir, fmt.Sprintf("envelope-%s.bin", traceID))
	// 0600：内含 SK 与明文 prompt。
	if err := os.WriteFile(name, envelope, 0o600); err != nil {
		logger.LegacyPrintf("service.reclaude", "reclaude envelope dump failed: %v", err)
		return
	}
	logger.LegacyPrintf("service.reclaude", "reclaude envelope dumped to %s", name)
}

// dumpOutboundRequest 把外层请求的**完整头部取值**落盘（排障专用）。
//
// 🔴 与 logEnvelopeShapeOnReject 的区别：那条刻意只记头名不记取值，因为它进
// ops_system_logs；这条记全部取值（含 Authorization 与签名头），只在
// SUB2API_DEBUG_RECLAUDE_ENVELOPE_DIR 配置时启用，用完必须清掉。
//
// 存在的理由：2026-09-24 排查 bad_envelope 时，同一串信封字节离线回放能拿到
// 200、经 sub2api 发出却被拒，而日志只有头名 —— 差异必然在某个取值上，
// 没有这条就只能靠猜。
func dumpOutboundRequest(outer *http.Request, traceID string) {
	dir := strings.TrimSpace(os.Getenv(reclaudeEnvelopeDumpEnv))
	if dir == "" || outer == nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\n", outer.Method, outer.URL.String(), outer.Proto)
	fmt.Fprintf(&b, "Host: %s\n", outer.Host)
	fmt.Fprintf(&b, "ContentLength: %d\n", outer.ContentLength)
	names := make([]string, 0, len(outer.Header))
	for name := range outer.Header {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, value := range outer.Header[name] {
			fmt.Fprintf(&b, "%s: %s\n", name, value)
		}
	}

	name := filepath.Join(dir, fmt.Sprintf("outbound-%s.txt", traceID))
	if err := os.WriteFile(name, []byte(b.String()), 0o600); err != nil {
		return
	}
	logger.LegacyPrintf("service.reclaude", "reclaude outbound request dumped to %s", name)
}
