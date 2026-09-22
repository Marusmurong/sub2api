package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// DefaultReclaudeOversellRatio 是售卖侧的安全系数：已售 token ≤ 可交付量 × 该值。
//
// 留 30% 余量的理由是「核无可核」：一个配额包消耗了多少，**只有我们自己的计数
// 这一个信源**（对方不提供任何用量接口）。计数有偏差时没有第二道防线，
// 只能靠余量吸收。
const DefaultReclaudeOversellRatio = 0.7

// ReclaudeSettings 是 reclaude 通道的运行时配置。
//
// 🔴 为什么是运行时设置而不是配置文件项：紧急止血必须能**立刻**生效。
// 配置文件项要重启，而重启在出事的时候恰恰是最不想做的动作。
//
// §8 里另外那几项（心跳周期、信封上限、日 token 上限、并发、作息）刻意不在这里：
// 前两个是协议常量，后三个是**账号级**字段 —— §6.8-A 明确要求作息按设备人设
// 各自生成，全局配一份等于 N 台「个人电脑」每天在同一分钟一起合盖。
type ReclaudeSettings struct {
	// Enabled 是总开关，默认 false。
	//
	// 关闭的语义是**已有 rec 账号立即不可调度**，不是「照跑到自然结束」。
	Enabled bool `json:"enabled"`

	// UnknownEventAlert 控制未知事件 Kind 是否告警。默认开：
	// 出现新 Kind 意味着协议变了，静默丢弃等于放弃唯一的预警。
	UnknownEventAlert bool `json:"unknown_event_alert"`

	// OversellRatio 见 DefaultReclaudeOversellRatio。
	OversellRatio float64 `json:"oversell_ratio"`
}

// DefaultReclaudeSettings 返回默认配置：**关闭**。
//
// 这条通道把全量明文 prompt 发给第三方网关，不该因为升了个版本就自己活过来。
func DefaultReclaudeSettings() *ReclaudeSettings {
	return &ReclaudeSettings{
		Enabled:           false,
		UnknownEventAlert: true,
		OversellRatio:     DefaultReclaudeOversellRatio,
	}
}

// Normalized 返回一份边界修正过的副本；不修改接收者。
func (s ReclaudeSettings) Normalized() ReclaudeSettings {
	out := s
	if out.OversellRatio <= 0 || out.OversellRatio > 1 {
		// 超卖率 > 1 就是字面意义上的超卖；≤ 0 是配置写错。两者都回落到默认。
		out.OversellRatio = DefaultReclaudeOversellRatio
	}
	return out
}

// GetReclaudeSettings 读取运行时配置；未配置时返回默认值。
func (s *SettingService) GetReclaudeSettings(ctx context.Context) (*ReclaudeSettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyReclaudeSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultReclaudeSettings(), nil
		}
		return nil, fmt.Errorf("get reclaude settings: %w", err)
	}

	var settings ReclaudeSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		// 解析失败按默认（即**关闭**）处理：读不懂配置时继续往第三方发明文
		// 是最糟的失败方向。
		return DefaultReclaudeSettings(), nil
	}

	normalized := settings.Normalized()
	return &normalized, nil
}

// IsReclaudeEnabled 是调度端用的快捷判定。
//
// ⚠️ 任何不确定路径一律返回 false（fail-closed），与本模块其它闸门一致：
// 误放行在这里没有补救手段。这与 sub2api 多数功能开关的 fail-open 相反，
// 是刻意的。
func (s *SettingService) IsReclaudeEnabled(ctx context.Context) bool {
	if s == nil {
		return false
	}
	settings, err := s.GetReclaudeSettings(ctx)
	if err != nil || settings == nil {
		return false
	}
	return settings.Enabled
}

// SetReclaudeSettings 写入运行时配置。
func (s *SettingService) SetReclaudeSettings(ctx context.Context, settings *ReclaudeSettings) error {
	if settings == nil {
		return fmt.Errorf("reclaude settings cannot be nil")
	}

	normalized := settings.Normalized()
	payload, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal reclaude settings: %w", err)
	}
	return s.settingRepo.Set(ctx, SettingKeyReclaudeSettings, string(payload))
}
