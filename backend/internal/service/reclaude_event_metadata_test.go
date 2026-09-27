//go:build unit

package service

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func decodeAM(t *testing.T, am string) map[string]any {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(am)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func TestReclaudeEventAdditionalMetadata(t *testing.T) {
	t.Run("api_query 带完整 13 字段", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t), []string{ReclaudeEventAPIQuery})
		am := batch.Events[0].EventData.AdditionalMetadata
		require.NotEmpty(t, am)
		m := decodeAM(t, am)
		require.Len(t, m, 13)
		require.Equal(t, "fullscreen", m["renderer_mode"])
		require.Equal(t, "firstParty", m["provider"])
		require.Equal(t, "repl_main_thread", m["querySource"])
		require.Equal(t, "max", m["subscription_type"])
		require.NotEmpty(t, m["cc_prompt_id"])
	})

	t.Run("cache_breakpoints 带完整 8 字段", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t), []string{ReclaudeEventAPICacheBreakpoints})
		m := decodeAM(t, batch.Events[0].EventData.AdditionalMetadata)
		require.Len(t, m, 8)
		require.Equal(t, float64(1), m["markerCount"])
		require.Equal(t, false, m["forkPointPinned"])
	})

	t.Run("同批 query 与 cache_breakpoints 共享 cc_prompt_id", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t),
			[]string{ReclaudeEventAPIQuery, ReclaudeEventAPICacheBreakpoints})
		q := decodeAM(t, batch.Events[0].EventData.AdditionalMetadata)
		c := decodeAM(t, batch.Events[1].EventData.AdditionalMetadata)
		require.Equal(t, q["cc_prompt_id"], c["cc_prompt_id"])
	})

	t.Run("api_retry 无 additional_metadata（度量填不出，缺席）", func(t *testing.T) {
		batch := BuildReclaudeEventBatch(eventContext(t), []string{ReclaudeEventAPIRetry})
		require.Empty(t, batch.Events[0].EventData.AdditionalMetadata)
	})
}

func TestReclaudeBuildAgeMins(t *testing.T) {
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	require.Equal(t, 60, reclaudeBuildAgeMins("2026-09-27T09:00:00Z", now))
	require.Equal(t, 0, reclaudeBuildAgeMins("bad", now))
	require.Equal(t, 0, reclaudeBuildAgeMins("2026-09-27T11:00:00Z", now)) // 未来→0
}
