package service

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// event_logging 事件的 additional_metadata 字段（2026-09-27 cli 完整会话抓包）。
//
// 🔴 真值里每条 tengu_api_query / tengu_api_cache_breakpoints 都带 additional_metadata
// （base64 编码的 JSON）。sub 此前**缺这个字段** —— 一个不带 additional_metadata 的
// tengu_api_query 是「非真实 CLI」的直接特征（每次遥测都暴露）。
//
// 🔴 只给这两个事件补 —— 它们的字段全部可如实填（定值 / 会话属性 / 现算）。
// **不给 tengu_api_success 补**：它的 additional_metadata 有 50+ 字段，含 ttftMs /
// firstContentMs / snapshotHash / toolsCharLength / queryChainId 等 CLI 内部度量，
// 代理层拿不到真值，填即矛盾（同 Datadog 那条不发 api_success 的理由）。

// additional_metadata 里的定值（真值样本一致）。
const (
	reclaudeMetaRendererMode   = "fullscreen"       // 交互式 CLI 全屏渲染
	reclaudeMetaProvider       = "firstParty"       // 直连 Anthropic
	reclaudeMetaPermissionMode = "default"          // 默认权限模式
	reclaudeMetaThinkingType   = "disabled"         // 回落：多数请求不开 thinking
	reclaudeMetaQuerySource    = "repl_main_thread" // 🔴 真值有 3 种(repl_main_thread /
	// generate_session_title / prompt_suggestion)，取最常见的主线程，不编造特定功能来源。
	reclaudeMetaTemperature = 1
)

// reclaudeAPIQueryMetadata 构造 tengu_api_query 的 additional_metadata。
//
// ✅ 真值 13 字段。model/betas/subscription_type 与外层 env 同源；
// cc_prompt_id 逐会话共享（同一会话的 query/cache_breakpoints 用同一个）；
// buildAgeMins 从 build_time 现算。
func reclaudeAPIQueryMetadata(model, betas, subscription, ccPromptID string, buildAgeMins int) map[string]any {
	return map[string]any{
		"renderer_mode":     reclaudeMetaRendererMode,
		"subscription_type": subscription,
		"cc_prompt_id":      ccPromptID,
		"model":             model,
		"messagesLength":    1, // 回落真值样本值；有下游 body 时应取 messages 数组长度
		"temperature":       reclaudeMetaTemperature,
		"provider":          reclaudeMetaProvider,
		"buildAgeMins":      buildAgeMins,
		"betas":             betas,
		"permissionMode":    reclaudeMetaPermissionMode,
		"querySource":       reclaudeMetaQuerySource,
		"thinkingType":      reclaudeMetaThinkingType,
		"fastMode":          false,
	}
}

// reclaudeCacheBreakpointsMetadata 构造 tengu_api_cache_breakpoints 的 additional_metadata。
//
// ✅ 真值 8 字段。markerCount 恒 1、forkPointPinned 恒 false；cachingEnabled /
// skipCacheWrite 真值随请求变（true/false），代理层无本次缓存详情，回落 false。
func reclaudeCacheBreakpointsMetadata(subscription, ccPromptID string) map[string]any {
	return map[string]any{
		"renderer_mode":     reclaudeMetaRendererMode,
		"subscription_type": subscription,
		"cc_prompt_id":      ccPromptID,
		"totalMessageCount": 1,
		"cachingEnabled":    false,
		"skipCacheWrite":    false,
		"forkPointPinned":   false,
		"markerCount":       1,
	}
}

// encodeReclaudeMetadata 把 metadata map 编成 base64(JSON)。
//
// ✅ 真值是 base64.StdEncoding(JSON)（带填充，与 process 字段同）。
func encodeReclaudeMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// reclaudeBuildAgeMins 从 build_time 算「构建至今多少分钟」。
//
// ✅ 真值 additional_metadata.buildAgeMins（如 7949）。build_time 缺/坏时返回 0
// （比编一个假龄更诚实：0 只是「刚构建」，不是矛盾）。
func reclaudeBuildAgeMins(buildTime string, now time.Time) int {
	t, err := time.Parse(time.RFC3339, buildTime)
	if err != nil {
		return 0
	}
	mins := int(now.Sub(t).Minutes())
	if mins < 0 {
		return 0
	}
	return mins
}

// reclaudeNewCCPromptID 生成一个 cc_prompt_id。
//
// 🔴 同一会话的 query 与 cache_breakpoints 共享同一个 —— 真值样本里两者的
// cc_prompt_id 相同。逐会话变化（不同会话不同 id）。
func reclaudeNewCCPromptID() string {
	return uuid.NewString()
}

// reclaudeEventAdditionalMetadata 按事件名产出 base64 编码的 additional_metadata。
//
// 🔴 只有 tengu_api_query / tengu_api_cache_breakpoints 有可如实填的 metadata。
// 其余事件（含 api_success、api_retry）返回空串 —— api_success 的度量填不出真值，
// 与其编造不如缺席（omitempty 让字段消失，真值里也不是每个事件都带）。
func reclaudeEventAdditionalMetadata(
	eventName, model, subscription, ccPromptID string, buildAgeMins int,
) string {
	switch eventName {
	case ReclaudeEventAPIQuery:
		return encodeReclaudeMetadata(
			reclaudeAPIQueryMetadata(model, reclaudeEventBetas, subscription, ccPromptID, buildAgeMins))
	case ReclaudeEventAPICacheBreakpoints:
		return encodeReclaudeMetadata(
			reclaudeCacheBreakpointsMetadata(subscription, ccPromptID))
	default:
		return ""
	}
}
