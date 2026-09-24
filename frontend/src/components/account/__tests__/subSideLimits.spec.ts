import { describe, expect, it } from 'vitest'
import { applySubSideLimits, type SubSideLimitInputs } from '../subSideLimits'

function inputs(overrides: Partial<SubSideLimitInputs> = {}): SubSideLimitInputs {
  return {
    sessionLimitEnabled: false, maxSessions: null, sessionIdleTimeout: null,
    deviceLimitEnabled: false, maxDevices: null, maxDevicesDaily: null, deviceWindowMinutes: null,
    rpmLimitEnabled: false, baseRpm: null, rpmStrategy: 'soft', rpmStickyBuffer: null,
    ...overrides
  }
}

// 🔴 这段逻辑此前只在 oauth 分支里，reclaude 走不到 —— 页面上设了限额、
// 保存后库里没有，且保存本身不报错。抽成共享函数后两个分支都用同一份。
describe('applySubSideLimits', () => {
  it('写入会话数与空闲超时', () => {
    const extra = applySubSideLimits({}, inputs({
      sessionLimitEnabled: true, maxSessions: 15, sessionIdleTimeout: 8
    }))

    expect(extra.max_sessions).toBe(15)
    expect(extra.session_idle_timeout_minutes).toBe(8)
  })

  it('写入设备数两层与窗口', () => {
    const extra = applySubSideLimits({}, inputs({
      deviceLimitEnabled: true, maxDevices: 3, maxDevicesDaily: 6, deviceWindowMinutes: 120
    }))

    expect(extra.max_devices).toBe(3)
    expect(extra.max_devices_daily).toBe(6)
    expect(extra.device_window_minutes).toBe(120)
  })

  it('设备窗口留空时用默认值', () => {
    const extra = applySubSideLimits({}, inputs({ deviceLimitEnabled: true, maxDevices: 3 }))

    expect(extra.device_window_minutes).toBe(60)
  })

  it('写入 RPM，留空时用默认基数', () => {
    const extra = applySubSideLimits({}, inputs({ rpmLimitEnabled: true, rpmStrategy: 'hard' }))

    expect(extra.base_rpm).toBe(15)
    expect(extra.rpm_strategy).toBe('hard')
  })

  it('关闭时删除对应键，而不是写 0', () => {
    // 写 0 会让「没设过」和「设成 0」无法区分，而后端把 0 当作未启用。
    const extra = applySubSideLimits(
      { max_sessions: 10, max_devices: 3, base_rpm: 25, rpm_strategy: 'soft' },
      inputs()
    )

    expect(extra).not.toHaveProperty('max_sessions')
    expect(extra).not.toHaveProperty('max_devices')
    expect(extra).not.toHaveProperty('base_rpm')
    expect(extra).not.toHaveProperty('rpm_strategy')
  })

  it('两层设备限额互相独立', () => {
    const extra = applySubSideLimits({}, inputs({
      deviceLimitEnabled: true, maxDevices: null, maxDevicesDaily: 6
    }))

    expect(extra).not.toHaveProperty('max_devices')
    expect(extra.max_devices_daily).toBe(6)
  })

  it('不动 extra 里的其它键', () => {
    const extra = applySubSideLimits(
      { reclaude_plan_tier: '20x', quota_daily_limit: 600 },
      inputs({ rpmLimitEnabled: true, baseRpm: 25 })
    )

    expect(extra.reclaude_plan_tier).toBe('20x')
    expect(extra.quota_daily_limit).toBe(600)
  })
})
