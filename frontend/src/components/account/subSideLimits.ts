/**
 * 会话数 / 设备数 / RPM 三项「sub 端准入控制」的 extra 写入。
 *
 * 🔴 抽出来是因为它此前只写在 EditAccountModal 的 oauth 分支里，
 * reclaude 分支完全走不到 —— 表现是页面上设了限额、保存后库里没有，
 * 而保存本身没有任何报错（2026-09-24 实测发现）。
 *
 * 这三项都是请求发往上游**之前**的 Redis 计数，与账号类型无关，
 * 必须与后端的 SupportsSessionLimit / SupportsDeviceLimit / SupportsRPMLimit 一致。
 *
 * ⚠️ 窗口费用不在此列：它锚在 session_window_start/end 上，
 * reclaude 从不写那两个字段（见后端 SupportsWindowCostLimit）。
 */
export interface SubSideLimitInputs {
  sessionLimitEnabled: boolean
  maxSessions: number | null
  sessionIdleTimeout: number | null

  deviceLimitEnabled: boolean
  maxDevices: number | null
  maxDevicesDaily: number | null
  deviceWindowMinutes: number | null

  rpmLimitEnabled: boolean
  baseRpm: number | null
  rpmStrategy: string
  rpmStickyBuffer: number | null
}

export const DEFAULT_DEVICE_WINDOW_MINUTES = 60
export const DEFAULT_BASE_RPM = 15

/**
 * applySubSideLimits 就地把三项限额写进 extra（未启用则删除对应键）。
 *
 * 删除而不是写 0：0 在后端语义里是「未启用」，但留着键会让
 * IsQuotaExceeded 一类的判断多一层分支，也让人分不清「没设过」和「设成了 0」。
 */
export function applySubSideLimits(
  extra: Record<string, unknown>,
  input: SubSideLimitInputs
): Record<string, unknown> {
  // 会话数
  if (input.sessionLimitEnabled && input.maxSessions != null && input.maxSessions > 0) {
    extra.max_sessions = input.maxSessions
    extra.session_idle_timeout_minutes = input.sessionIdleTimeout ?? 5
  } else {
    delete extra.max_sessions
    delete extra.session_idle_timeout_minutes
  }

  // 设备数：两层互相独立，0 / 留空 / 关闭 = 该层不限制
  const wantMaxDevices = input.deviceLimitEnabled && input.maxDevices != null && input.maxDevices > 0
  const wantMaxDevicesDaily =
    input.deviceLimitEnabled && input.maxDevicesDaily != null && input.maxDevicesDaily > 0
  if (wantMaxDevices) {
    extra.max_devices = Math.floor(input.maxDevices!)
    extra.device_window_minutes =
      input.deviceWindowMinutes != null && input.deviceWindowMinutes > 0
        ? Math.floor(input.deviceWindowMinutes)
        : DEFAULT_DEVICE_WINDOW_MINUTES
  } else {
    delete extra.max_devices
    delete extra.device_window_minutes
  }
  if (wantMaxDevicesDaily) {
    extra.max_devices_daily = Math.floor(input.maxDevicesDaily!)
  } else {
    delete extra.max_devices_daily
  }

  // RPM
  if (input.rpmLimitEnabled) {
    extra.base_rpm = input.baseRpm != null && input.baseRpm > 0 ? input.baseRpm : DEFAULT_BASE_RPM
    extra.rpm_strategy = input.rpmStrategy
    if (input.rpmStickyBuffer != null && input.rpmStickyBuffer > 0) {
      extra.rpm_sticky_buffer = input.rpmStickyBuffer
    } else {
      delete extra.rpm_sticky_buffer
    }
  } else {
    delete extra.base_rpm
    delete extra.rpm_strategy
    delete extra.rpm_sticky_buffer
  }

  return extra
}
