package service

import (
	"context"
	"strings"
	"time"
)

// 退役模型的本地拦截。
//
// 客户端请求一个上游已下架的模型（生产观测：claude-3-5-haiku-20241022、
// claude-3-5-sonnet-20240620/20241022、claude-opus-4-20250514、claude-sonnet-4-20250514，
// 以及客户端拼错的 Opus-5）时，上游返回 404 model not found。
//
// 原有处理是把 (账号, 模型) 冷却 30 分钟并**故障转移到下一个账号**——于是同一次客户端
// 请求会依次打到多个账号上，每个账号都往 Anthropic 发一次不存在的模型名、每个都被冷却。
// 生产上 claude-3-5-haiku-20241022 就同时出现在账号 15/17/36 的冷却记录里。对订阅号而言
// 这既是无谓的暴露，也是明确的异常信号。
//
// 这里记住的是**模型本身不存在**这件事，与账号无关：任何一个账号撞到 404 之后，后续
// 请求在选号之前就被拒，一个账号都不再碰。
//
// 为什么不用硬编码的退役型号清单：清单要人维护、必然滞后，而且覆盖不了客户端拼错的名字
// （Opus-5 / claude-Opus-5 这类）。从上游的回答里学，自维护。

// modelNotFoundTTL 是"该模型不存在"的记忆时长。
//
// 取几小时而非永久：模型下架是长期事实，但万一是上游临时抽风或我们误判，
// 过期后会自动重新探测一次，最多再浪费一个请求。
const modelNotFoundTTL = 6 * time.Hour

// ModelNotFoundStore 记录"某平台上某模型不存在"。
//
// 独立于 GatewayCache 的理由同 PrevRequestIDStore：后者被大量测试桩实现，
// 往其中加方法会波及无关测试。实现方为 repository.gatewayCache。
type ModelNotFoundStore interface {
	IsModelNotFound(ctx context.Context, platform, model string) (bool, error)
	MarkModelNotFound(ctx context.Context, platform, model string, ttl time.Duration) error
}

// modelNotFoundStore 在缓存实现未提供该能力时返回 nil，能力静默降级关闭。
func (s *GatewayService) modelNotFoundStore() ModelNotFoundStore {
	if s == nil || s.cache == nil {
		return nil
	}
	store, _ := s.cache.(ModelNotFoundStore)
	return store
}

// IsKnownMissingModel 报告该模型是否已被记为"上游不存在"，供 handler 在选号之前拦截。
//
// 任何异常（缓存不可用等）都返回 false —— 拦截是优化不是正确性要求，
// 失败开放只会退回到改动前的行为。
func (s *GatewayService) IsKnownMissingModel(ctx context.Context, platform, model string) bool {
	store := s.modelNotFoundStore()
	if store == nil {
		return false
	}
	missing, err := store.IsModelNotFound(ctx, platform, normalizeMissingModelKey(model))
	if err != nil {
		return false
	}
	return missing
}

// rememberMissingModel 在上游确认模型不存在后记下它。
//
// account 用于判断这次 404 是否**只**因为该账号的模型映射而起：账号自带
// model_mapping 且把请求模型映射成了别的名字时，404 说明的是"映射后的那个名字在这个
// 账号上不存在"，不能推广成全局事实——别的账号可能映射到别处、或压根不映射。
// 只有请求模型与实际发出的模型一致时，这个 404 才是关于模型本身的。
func (s *GatewayService) rememberMissingModel(ctx context.Context, account *Account, requestedModel string) {
	store := s.modelNotFoundStore()
	if store == nil || account == nil {
		return
	}
	model := normalizeMissingModelKey(requestedModel)
	if model == "" {
		return
	}
	if mapped := strings.TrimSpace(account.GetMappedModel(requestedModel)); mapped != "" &&
		!strings.EqualFold(mapped, strings.TrimSpace(requestedModel)) {
		return
	}
	_ = store.MarkModelNotFound(ctx, account.Platform, model, modelNotFoundTTL)
}

// normalizeMissingModelKey 统一大小写与空白，让 "Opus-5" 与 "opus-5" 命中同一条记忆。
// 上游的模型名比对本身不区分大小写以外的形态，这里只做最小归一。
func normalizeMissingModelKey(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}
