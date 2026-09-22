import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ReclaudeStatusCell from '../ReclaudeStatusCell.vue'
import type { Account } from '@/types'

// 与既有 cell 测试一致：t 直接回 key，断言只看结构与取值，不依赖文案。
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

// 水位接口默认成功；单个用例里再覆盖。
const getReclaudeDailyUsage = vi.fn()
vi.mock('@/api/admin', () => ({
  default: { accounts: { getReclaudeDailyUsage: (...args: unknown[]) => getReclaudeDailyUsage(...args) } }
}))

beforeEach(() => {
  getReclaudeDailyUsage.mockReset()
  getReclaudeDailyUsage.mockResolvedValue({
    usage: { tokens: 0, upstream_calls: 0, failed_upstream_calls: 0 },
    daily_cap: 2_000_000,
    effective_tokens: 500_000,
    weekly_soft_limit: 11_200_000
  })
})

function account(overrides: Record<string, unknown> = {}): Account {
  return {
    id: 1,
    name: 'rec/mbp-3448',
    platform: 'anthropic',
    type: 'reclaude',
    credentials: { reclaude_client_version: 'v1.4.0' },
    extra: { reclaude_daily_token_cap: 2_000_000 },
    ...overrides
  } as unknown as Account
}

function render(acc: Account) {
  return mount(ReclaudeStatusCell, { props: { account: acc } })
}

describe('ReclaudeStatusCell', () => {
  it('显示日上限与客户端版本', () => {
    const text = render(account()).text()

    expect(text).toContain('2.0M')
    expect(text).toContain('v1.4.0')
  })

  it('日上限为 0 时明确标出未标定', () => {
    // 上限为 0 = 包络未标定，账号根本不会被调度。显示成 "0" 会被误读成「用完了」。
    const text = render(account({ extra: { reclaude_daily_token_cap: 0 } })).text()

    expect(text).toContain('reclaudeCellCapMissing')
  })

  it('缺少上限键时同样算未标定', () => {
    expect(render(account({ extra: {} })).text()).toContain('reclaudeCellCapMissing')
  })

  it('有绑定邮箱与换号时间时一并展示', () => {
    const text = render(
      account({
        extra: {
          reclaude_daily_token_cap: 1000,
          reclaude_bound_email: 'o***@example.com',
          reclaude_last_switched_at: '2026-09-20T10:00:00Z'
        }
      })
    ).text()

    expect(text).toContain('o***@example.com')
    expect(text).toContain('reclaudeCellSwitched')
  })

  it('换号时间不可解析时不显示该行', () => {
    const text = render(
      account({ extra: { reclaude_daily_token_cap: 1000, reclaude_last_switched_at: 'nope' } })
    ).text()

    expect(text).not.toContain('reclaudeCellSwitched')
  })

  it('显示今日已用水位', async () => {
    const wrapper = render(account())
    await flushPromises()

    expect(wrapper.text()).toContain('500K')
  })

  it('水位读不到时显示「不可读」而不是 0', async () => {
    // 🔴 回 0 会被读成「今天还没用」，而真相是「我们不知道」——
    // 这条链路上水位只有我们自己的计数这一个信源，读不到必须看得见。
    getReclaudeDailyUsage.mockRejectedValue(new Error('redis down'))

    const wrapper = render(account())
    await flushPromises()

    expect(wrapper.text()).toContain('reclaudeUsageUnavailable')
    expect(wrapper.text()).not.toContain('reclaudeUsageToday0')
  })
})
