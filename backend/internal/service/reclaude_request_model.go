package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// reclaudeRequestModelMaxBytes 是为读取 model 字段而缓冲的上限。
//
// model 在 JSON 体的顶层，通常在头几十字节内。设上限是因为这只是为了
// 填一个遥测字段，不值得为它把一个超大 prompt 完整读进内存两次。
const reclaudeRequestModelMaxBytes = 64 << 10

// reclaudeRequestModel 读出请求体里的 model，**并把 body 原样还回去**。
//
// 🔴 不还回去就是灾难：Body 是一次性的 Reader，读掉之后
// buildReclaudeEnvelope 会读到空，每条推理都变成空请求体发上去。
// 这个函数的正确性标准不是「读到了 model」，而是**「读完之后 body 与读之前
// 完全一致」**。
func reclaudeRequestModel(req *http.Request) string {
	if req == nil || req.Body == nil {
		return ""
	}

	original := req.Body
	buffered, err := io.ReadAll(io.LimitReader(original, reclaudeRequestModelMaxBytes))
	// 无论成功与否都要还：已缓冲的部分 + 尚未读到的剩余部分。
	//
	// 🔴 用 restoredBody 而不是 io.NopCloser：NopCloser 会把原 Body 的 Close
	// 丢掉，而那个 Close 负责归还底层连接 —— 每条推理泄漏一个连接。
	req.Body = &restoredBody{
		Reader: io.MultiReader(bytes.NewReader(buffered), original),
		closer: original,
	}
	if err != nil {
		return ""
	}

	var payload struct {
		Model string `json:"model"`
	}
	// 截断导致的解析失败是预期内的（超大 prompt），静默返回空串：
	// 遥测少一个字段远好过为它冒险动 body。
	if err := json.Unmarshal(buffered, &payload); err != nil {
		return ""
	}
	return payload.Model
}

// restoredBody 把缓冲过的前缀与剩余流拼回成一个 ReadCloser，
// 并保留**原始** Body 的 Close —— 那是归还底层连接的唯一途径。
type restoredBody struct {
	io.Reader
	closer io.Closer
}

func (b *restoredBody) Close() error {
	return b.closer.Close()
}
