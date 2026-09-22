package reclaude

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// EncodeEnvelope 把元数据与请求体拼成一个完整的请求信封。
//
// 返回 []byte 而不是 io.Reader 是**刻意的**：设备签名覆盖的是整个信封的
// sha256，所以请求体必须先完整 buffer 再发，不能 chunked 流式上传。
// 对 /v1/messages 无影响（请求体小）。响应侧仍然是流式的，见 DecodeResponseEnvelope。
func EncodeEnvelope(meta ClientRequestMetadata, body []byte) ([]byte, error) {
	for name := range meta.Headers {
		if name != strings.ToLower(name) {
			return nil, fmt.Errorf("envelope metadata header %q must be lowercase", name)
		}
	}

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope metadata: %w", err)
	}
	if len(metaJSON) > EnvelopeMaxMetadataBytes {
		return nil, fmt.Errorf("metadata length %d exceeds limit %d", len(metaJSON), EnvelopeMaxMetadataBytes)
	}

	out := make([]byte, EnvelopeLengthPrefixBytes, EnvelopeLengthPrefixBytes+len(metaJSON)+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(metaJSON)))
	out = append(out, metaJSON...)
	out = append(out, body...)
	return out, nil
}

// DecodeResponseEnvelope 读出响应信封的元数据段，并把**剩余流**原样交还给调用方。
//
// 返回 io.Reader 而不是 []byte 同样是刻意的：SSE 的首字节必须立刻到达下游，
// 不能等整个响应读完。
func DecodeResponseEnvelope(r io.Reader) (*GatewayResponseMetadata, io.Reader, error) {
	if r == nil {
		return nil, nil, fmt.Errorf("decode envelope: reader is nil")
	}

	prefix := make([]byte, EnvelopeLengthPrefixBytes)
	if _, err := io.ReadFull(r, prefix); err != nil {
		return nil, nil, fmt.Errorf("read envelope length prefix: %w", err)
	}

	metaLen := binary.BigEndian.Uint32(prefix)
	if metaLen == 0 {
		return nil, nil, fmt.Errorf("envelope metadata length is zero")
	}
	if metaLen > EnvelopeMaxMetadataBytes {
		return nil, nil, fmt.Errorf("metadata length %d exceeds limit %d", metaLen, EnvelopeMaxMetadataBytes)
	}

	metaJSON := make([]byte, metaLen)
	if _, err := io.ReadFull(r, metaJSON); err != nil {
		return nil, nil, fmt.Errorf("read envelope metadata (%d bytes): %w", metaLen, err)
	}

	var meta GatewayResponseMetadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return nil, nil, fmt.Errorf("unmarshal envelope metadata: %w", err)
	}

	return &meta, r, nil
}
