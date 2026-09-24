import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountCapacityCell from '../AccountCapacityCell.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
    })
  }
})

function reclaudeAccount(overrides: Record<string, unknown> = {}): Account {
  return {
    id: 229,
    name: 'rec/jimmus-mac-4170',
    platform: 'anthropic',
    type: 'reclaude',
    concurrency: 5,
    status: 'active',
    schedulable: true,
    ...overrides
  } as unknown as Account
}

// 🔴 容量列此前只给 reclaude 显示并发，会话数/设备数/RPM/配额全不显示 ——
// 三处 show* 卡在 isAnthropicOAuthOrSetupToken，配额卡在 apikey/bedrock。
//
// 后端已按各自的适用谓词放开这些限制（SupportsSessionLimit / SupportsDeviceLimit /
// SupportsRPMLimit，配额走美元制的 quota_daily_limit），前端必须跟上：
// 限额设了却看不见，运营无从判断这个号还能不能接量。
describe('AccountCapacityCell — reclaude', () => {
  const render = (acc: Account) => mount(AccountCapacityCell, { props: { account: acc } })

  it('显示会话数', () => {
    // 用「没设限额时不显示」做对照，避免数字在 HTML 里误匹配到别处。
    const withLimit = render(reclaudeAccount({ max_sessions: 15, active_sessions: 3 })).text()
    const without = render(reclaudeAccount()).text()

    expect(withLimit).toContain('15')
    expect(without).not.toContain('15')
  })

  it('显示设备数', () => {
    const wrapper = render(reclaudeAccount({ max_devices: 3, active_devices: 2 }))

    expect(wrapper.find('[data-testid="capacity-devices"]').exists()).toBe(true)
  })

  it('显示 RPM', () => {
    const withLimit = render(reclaudeAccount({ base_rpm: 25, current_rpm: 4 })).text()
    const without = render(reclaudeAccount()).text()

    expect(withLimit).toContain('25')
    expect(without).not.toContain('25')
  })

  it('显示美元日配额', () => {
    // reclaude 的日限额按套餐档位换算成美元，走的就是 quota_daily_limit。
    const withQuota = render(reclaudeAccount({ quota_daily_limit: 600, quota_daily_used: 12.5 })).text()
    const without = render(reclaudeAccount()).text()

    expect(withQuota).toContain('600')
    expect(without).not.toContain('600')
  })

  it('不显示窗口费用', () => {
    // 窗口费用锚在 session_window_start/end 上，reclaude 从不写这两个字段，
    // 显示出来等于假装知道一个我们其实没有的数（与后端 SupportsWindowCostLimit 一致）。
    const html = render(reclaudeAccount({ window_cost_limit: 100, current_window_cost: 20 })).html()

    expect(html).not.toContain('$20')
  })

  it('未设限额时不显示对应徽标', () => {
    const html = render(reclaudeAccount()).html()

    expect(html).not.toContain('data-testid="capacity-devices"')
  })
})
