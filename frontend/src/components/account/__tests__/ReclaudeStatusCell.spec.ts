import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import ReclaudeStatusCell from '../ReclaudeStatusCell.vue'
import type { Account } from '@/types'

// 与既有 cell 测试一致：t 直接回 key，断言只看结构与取值，不依赖文案。
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

function account(overrides: Record<string, unknown> = {}): Account {
  return {
    id: 1,
    name: 'rec/mbp-3448',
    platform: 'anthropic',
    type: 'reclaude',
    credentials: { reclaude_client_version: 'v1.4.0' },
    extra: {
      reclaude_plan_tier: '20x',
      quota_daily_limit: 600,
      quota_daily_used: 120
    },
    ...overrides
  } as unknown as Account
}

function render(acc: Account) {
  return mount(ReclaudeStatusCell, { props: { account: acc } })
}

describe('ReclaudeStatusCell', () => {
  // 🔴 口径必须与其它账号类型一致：都是**美元**日限额，都直接读账号行。
  // 之前这里单独拉 Redis 的 token 水位，页面上两种账号显示两种东西，
  // 运营对不上账。
  it('显示套餐档位与美元日用量/限额', () => {
    const text = render(account()).text()

    expect(text).toContain('20X')
    expect(text).toContain('$120.00')
    expect(text).toContain('$600.00')
    expect(text).toContain('v1.4.0')
  })

  it('没设日限额时明确标出未标定', () => {
    // 未标定 = 账号根本不会被调度。显示成 "$0" 会被误读成「还没用」。
    const text = render(account({ extra: { quota_daily_limit: 0 } })).text()

    expect(text).toContain('reclaudeCellCapMissing')
  })

  it('缺少限额键时同样算未标定', () => {
    const text = render(account({ extra: {} })).text()

    expect(text).toContain('reclaudeCellCapMissing')
  })

  it('用量到顶时标红', () => {
    const wrapper = render(account({
      extra: { reclaude_plan_tier: '20x', quota_daily_limit: 600, quota_daily_used: 600 }
    }))

    expect(wrapper.html()).toContain('text-red-600')
  })

  it('用量过八成时标黄', () => {
    const wrapper = render(account({
      extra: { reclaude_plan_tier: '20x', quota_daily_limit: 600, quota_daily_used: 500 }
    }))

    expect(wrapper.html()).toContain('text-amber-600')
  })

  it('未知档位时不显示档位标签，但限额照常显示', () => {
    // 历史账号可能没有档位键；限额才是生效的那个值。
    const text = render(account({
      extra: { quota_daily_limit: 600, quota_daily_used: 10 }
    })).text()

    expect(text).toContain('$600.00')
  })

  it('显示绑定邮箱与换号日期', () => {
    const text = render(account({
      extra: {
        quota_daily_limit: 600,
        reclaude_bound_email: 'owner@example.com',
        reclaude_last_switched_at: '2026-09-20T10:00:00Z'
      }
    })).text()

    expect(text).toContain('owner@example.com')
    expect(text).toContain('reclaudeCellSwitched')
  })
})
