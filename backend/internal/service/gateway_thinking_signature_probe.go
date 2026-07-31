package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// 本文件是一个**只读诊断**：回答"上游拒绝的那个 thinking 签名，是客户端本来就发错了，
// 还是我们在转发链上改坏的"。
//
// 为什么需要它：2026-07-31 连续两次归因失败。先怀疑跨账号签名（修了，没动），
// 再怀疑 StripEmptyTextBlocks 的序列化往返（修了，错误率反而从 16.1% 升到 35.5%）。
// 两次都是从时间巧合倒推原因，而不是先确认"我们到底有没有改动那些字节"。
//
// 它不记录任何 thinking 内容：只记录块的位置、签名前 8 字符、以及块原始字节的
// sha256 前 8 位。够用来判定"变了没有、变在第几块"，不足以还原用户对话。

// thinkingBlockPrint 是单个 thinking 块的最小可比较指纹。
type thinkingBlockPrint struct {
	path   string // messages.3.content.31
	sigPfx string // 签名前 8 字符，用于分辨"换了一个签名"与"签名没变但内容变了"
	rawSum string // 块原始字节的 sha256 前 8 位
}

// thinkingBlockPrints 提取 body 里全部 thinking / redacted_thinking 块的指纹。
func thinkingBlockPrints(body []byte) []thinkingBlockPrint {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return nil
	}
	var prints []thinkingBlockPrint
	msgs.ForEach(func(msgIdx, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			return true
		}
		for i, b := range content.Array() {
			switch b.Get("type").String() {
			case "thinking", "redacted_thinking":
			default:
				continue
			}
			sig := b.Get("signature").String()
			if len(sig) > 8 {
				sig = sig[:8]
			}
			sum := sha256.Sum256([]byte(b.Raw))
			prints = append(prints, thinkingBlockPrint{
				path:   "messages." + msgIdx.String() + ".content." + strconv.Itoa(i),
				sigPfx: sig,
				rawSum: hex.EncodeToString(sum[:])[:8],
			})
		}
		return true
	})
	return prints
}

// diffThinkingBlockPrints 比较转发前后的 thinking 块指纹，返回一行可直接进日志的描述。
// 完全一致时返回空串——绝大多数请求都该是这个结果，不该刷屏。
//
// 三类差异分别有不同的含义：
//
//	moved   位置变了（我们删了它前面的块）——正常，删块必然导致后面的下标前移
//	rewrote 位置与签名都在，但块的原始字节变了——**我们改坏了它**，这是要找的东西
//	dropped 块没了——我们删的，可能是删对了（签名本就缺失），也可能删多了
func diffThinkingBlockPrints(before, after []thinkingBlockPrint) string {
	if len(before) == 0 && len(after) == 0 {
		return ""
	}

	// 以「签名前缀 + 原始字节摘要」为身份：两者都在则块未被改动。
	afterByIdentity := make(map[string]string, len(after))
	afterBySig := make(map[string]string, len(after))
	for _, p := range after {
		afterByIdentity[p.sigPfx+"|"+p.rawSum] = p.path
		afterBySig[p.sigPfx] = p.rawSum
	}

	var rewrote, dropped, moved []string
	for _, p := range before {
		if newPath, ok := afterByIdentity[p.sigPfx+"|"+p.rawSum]; ok {
			if newPath != p.path {
				moved = append(moved, p.path+"->"+newPath)
			}
			continue
		}
		if newSum, ok := afterBySig[p.sigPfx]; ok {
			// 签名还在，字节变了——这就是会被上游判为签名无效的情形。
			rewrote = append(rewrote, p.path+"("+p.rawSum+"->"+newSum+")")
			continue
		}
		dropped = append(dropped, p.path+"/"+p.sigPfx)
	}

	if len(rewrote) == 0 && len(dropped) == 0 && len(moved) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("n_before=" + strconv.Itoa(len(before)) + " n_after=" + strconv.Itoa(len(after)))
	if len(rewrote) > 0 {
		sb.WriteString(" REWROTE=[" + strings.Join(rewrote, ",") + "]")
	}
	if len(dropped) > 0 {
		sb.WriteString(" dropped=[" + strings.Join(dropped, ",") + "]")
	}
	if len(moved) > 0 {
		sb.WriteString(" moved=" + strconv.Itoa(len(moved)))
	}
	return sb.String()
}
