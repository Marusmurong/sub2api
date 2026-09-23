package service

import (
	"math/rand"
	"time"
)

const (
	// ExtraKeyReclaudeOfflineWindow 是账号级的「模拟关机」作息。
	//
	// 🔴 必须是**账号级**而不是全局配置：全局配置意味着 N 台「个人电脑」每天在
	// 同一分钟一起合盖 —— 那比版本号集体同步更显眼（它每天发生一次），
	// 而且与「每台设备时区不同」的人设直接矛盾。
	ExtraKeyReclaudeOfflineWindow = "reclaude_offline_window"

	// ReclaudeOfflineReason 是模拟关机写入的冷却原因。
	//
	// 必须与节点不可用可区分：运维看到一个冷却中的账号，要一眼分得出
	// 「这是它的作息」还是「网关出事了」。
	ReclaudeOfflineReason = "reclaude_offline_window"

	// ReclaudeGatewayUnavailableReason 是网关/节点不可用写入的冷却原因。
	ReclaudeGatewayUnavailableReason = "reclaude_gateway_unavailable"
)

// 作息生成的取值范围：凌晨合盖、睡 5–9 小时，像个正常作息的人。
const (
	reclaudeOfflineStartHourMin = 0
	reclaudeOfflineStartHourMax = 6
	reclaudeOfflineDurationMin  = 5
	reclaudeOfflineDurationMax  = 9
	reclaudeOfflineDurationCap  = 24
)

// ReclaudeOfflineUntil 判断账号此刻是否处于「模拟关机」窗口内。
//
// 返回 (窗口结束时刻, 是否离线)。调用方据此写 TempUnschedulableUntil ——
// IsSchedulable() 刻意不读任何时间窗配置，冷却统一走那一个字段。
func ReclaudeOfflineUntil(account *Account, at time.Time) (time.Time, bool) {
	if account == nil || !account.IsReclaude() {
		return time.Time{}, false
	}

	raw, ok := account.Extra[ExtraKeyReclaudeOfflineWindow].(map[string]any)
	if !ok {
		return time.Time{}, false
	}

	startHour := int(credentialInt64(raw, "start_hour"))
	// 老账号的 extra 没有这个键 —— 缺席时按整点处理，而不是判成「永不离线」。
	startMinute := int(credentialInt64(raw, "start_minute"))
	if startMinute < 0 || startMinute > 59 {
		startMinute = 0
	}
	durationHours := int(credentialInt64(raw, "duration_hours"))
	if durationHours <= 0 || durationHours >= reclaudeOfflineDurationCap {
		return time.Time{}, false
	}
	if startHour < 0 || startHour > 23 {
		return time.Time{}, false
	}

	location := loadOfflineLocation(credentialString(raw, "timezone"))
	local := at.In(location)

	// 同时检查「今天的窗口」和「昨天开始、跨午夜延续到今天的窗口」。
	for _, dayOffset := range []int{0, -1} {
		day := local.AddDate(0, 0, dayOffset)
		start := time.Date(day.Year(), day.Month(), day.Day(), startHour, startMinute, 0, 0, location)
		end := start.Add(time.Duration(durationHours) * time.Hour)
		if !local.Before(start) && local.Before(end) {
			return end, true
		}
	}
	return time.Time{}, false
}

// loadOfflineLocation 解析时区，失败退回 UTC。
//
// 退回而不是报错：时区串写错的后果应当是「作息不准」，不应当是「这台设备
// 永远不停机」—— 后者恰恰是本机制要消除的特征。
func loadOfflineLocation(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

// GenerateReclaudeOfflineWindow 按设备人设时区生成一份带抖动的作息。
//
// 抖动是重点：不抖动就等于给 N 台设备配了同一个作息，而那正是批量特征。
func GenerateReclaudeOfflineWindow(timezone string) map[string]any {
	if timezone == "" {
		timezone = "UTC"
	}
	startHour := reclaudeOfflineStartHourMin +
		rand.Intn(reclaudeOfflineStartHourMax-reclaudeOfflineStartHourMin+1)
	duration := reclaudeOfflineDurationMin +
		rand.Intn(reclaudeOfflineDurationMax-reclaudeOfflineDurationMin+1)

	// 🔴 分钟级抖动是**人设时区集中的必要补偿**。
	//
	// rec 屏蔽中国 IP ⇒ 其客群本来就是中国用户翻墙 ⇒ 人设时区集中在
	// Asia/*（见 gen-persona.py 的 LOCALES）。此时若作息只精确到整点，
	// N 台「个人电脑」会在北京时间同一分钟一起合盖 —— §6.10 点名这比
	// 版本号同步更显眼，因为它每天发生一次。
	return map[string]any{
		"timezone":       timezone,
		"start_hour":     startHour,
		"start_minute":   rand.Intn(60),
		"duration_hours": duration,
	}
}
