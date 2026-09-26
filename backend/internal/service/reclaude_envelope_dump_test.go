package service

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/reclaude"
	"github.com/stretchr/testify/require"
)

// E0（RECLAUDE_REVOCATION_VERIFY_PLAN_2026-09-26 §2.1）：成功路径的信封也要落盘。
//
// 此前只有被拒的信封才 dump，09-26 拿到的 10 条 sub 实发信封是靠驱动一个
// 已撤销账号触发 reject 得到的，而那条 /v1/messages 是 curl 测试体 —— 不代表
// 真实推理。要对照真客户端，必须拿到 200 的那条。
func TestDumpReclaudeEnvelopeAllStatuses(t *testing.T) {
	t.Run("未配置目录时什么都不写", func(t *testing.T) {
		t.Setenv(reclaudeEnvelopeDumpEnv, "")
		require.NotPanics(t, func() { dumpReclaudeEnvelope([]byte("x"), "trace", 200) })
	})

	t.Run("文件名带内层 status,200 也落盘", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(reclaudeEnvelopeDumpEnv, dir)

		dumpReclaudeEnvelope([]byte("envelope-bytes"), "abc123", 200)
		dumpReclaudeEnvelope([]byte("envelope-bytes"), "def456", 502)
		dumpReclaudeEnvelope([]byte("envelope-bytes"), "ghi789", 0)

		got, err := os.ReadFile(filepath.Join(dir, "envelope-abc123-200.bin"))
		require.NoError(t, err)
		require.Equal(t, "envelope-bytes", string(got))
		_, err = os.Stat(filepath.Join(dir, "envelope-def456-502.bin"))
		require.NoError(t, err)
		// 传输层失败没有 status,用 err 标记,不能和 0 混淆。
		_, err = os.Stat(filepath.Join(dir, "envelope-ghi789-err.bin"))
		require.NoError(t, err)
	})
}

func TestReclaudeUpstream_Do_DumpsSuccessfulEnvelope(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(reclaudeEnvelopeDumpEnv, dir)
	account, cipher := reclaudeTestAccount(t)
	upstream := &capturingUpstream{response: &http.Response{
		StatusCode: 200, Header: http.Header{},
		Body: io.NopCloser(bytes.NewReader(envelopeResponse(t, 200, reclaude.GatewayResponseMetadata{}, "ok"))),
	}}
	sut := NewReclaudeUpstream(upstream, cipher, nil)

	resp, err := sut.Do(innerRequest(t, `{"model":"claude"}`), account, "http://proxy:1080")
	require.NoError(t, err)
	_ = resp.Body.Close()

	matches, err := filepath.Glob(filepath.Join(dir, "envelope-*-200.bin"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "成功的信封必须落盘")
	got, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	require.Equal(t, upstream.gotBody, got, "落盘的必须是实际出站的字节")
}
