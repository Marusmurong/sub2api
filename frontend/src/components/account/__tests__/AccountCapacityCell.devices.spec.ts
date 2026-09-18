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

function makeAccount(overrides: Partial<Account>): Account {
  return {
    id: 1,
    name: 'account',
    platform: 'anthropic',
    type: 'oauth',
    proxy_id: null,
    concurrency: 5,
    priority: 1,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: true,
    created_at: '2026-09-17T00:00:00Z',
    updated_at: '2026-09-17T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides
  } as Account
}

const devicesBadge = (wrapper: ReturnType<typeof mount>) => wrapper.find('[data-testid="capacity-devices"]')

describe('AccountCapacityCell device limit badge', () => {
  it('hides the badge when max_devices is unset (no limit)', () => {
    const wrapper = mount(AccountCapacityCell, { props: { account: makeAccount({}) } })
    expect(devicesBadge(wrapper).exists()).toBe(false)
  })

  it('hides the badge when max_devices is 0', () => {
    const wrapper = mount(AccountCapacityCell, { props: { account: makeAccount({ max_devices: 0, active_devices: 0 }) } })
    expect(devicesBadge(wrapper).exists()).toBe(false)
  })

  it('hides the badge for non OAuth/SetupToken accounts even when configured', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: makeAccount({ type: 'apikey', max_devices: 1, active_devices: 1 }) }
    })
    expect(devicesBadge(wrapper).exists()).toBe(false)
  })

  it('shows registered/max with the configured window in the tooltip', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: makeAccount({ max_devices: 3, device_window_minutes: 60, active_devices: 1 }) }
    })
    const badge = devicesBadge(wrapper)
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toContain('1')
    expect(badge.text()).toContain('3')
    expect(badge.attributes('title')).toBe('admin.accounts.capacity.devices.normal:{"window":60}')
    expect(badge.classes()).toContain('bg-emerald-100')
  })

  it('defaults active_devices to 0 and window to 360 when the backend omits them', () => {
    const wrapper = mount(AccountCapacityCell, { props: { account: makeAccount({ max_devices: 1 }) } })
    const badge = devicesBadge(wrapper)
    expect(badge.text()).toContain('0')
    expect(badge.attributes('title')).toBe('admin.accounts.capacity.devices.normal:{"window":360}')
  })

  it('turns red with the full tooltip once registered devices reach the limit', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: makeAccount({ type: 'setup-token', max_devices: 1, device_window_minutes: 360, active_devices: 1 }) }
    })
    const badge = devicesBadge(wrapper)
    expect(badge.classes()).toContain('bg-red-100')
    expect(badge.attributes('title')).toBe('admin.accounts.capacity.devices.full:{"window":360}')
  })

  it('turns yellow at 80% of the limit', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: makeAccount({ max_devices: 5, active_devices: 4 }) }
    })
    expect(devicesBadge(wrapper).classes()).toContain('bg-yellow-100')
  })

  it('shows the 24h usage as a suffix and in the tooltip when both layers are set', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: makeAccount({ max_devices: 1, device_window_minutes: 180, active_devices: 0, max_devices_daily: 3, active_devices_daily: 2 }) }
    })
    const badge = devicesBadge(wrapper)
    expect(badge.text()).toContain('[24h 2/3]')
    expect(badge.attributes('title')).toBe(
      'admin.accounts.capacity.devices.normal:{"window":180}\nadmin.accounts.capacity.devices.daily:{"used":2,"max":3}'
    )
    expect(badge.classes()).toContain('bg-emerald-100')
  })

  it('turns red when the 24h quota is full even if concurrent slots are free', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: makeAccount({ max_devices: 1, active_devices: 0, max_devices_daily: 3, active_devices_daily: 3 }) }
    })
    const badge = devicesBadge(wrapper)
    expect(badge.classes()).toContain('bg-red-100')
    expect(badge.attributes('title')).toContain('admin.accounts.capacity.devices.dailyFull:{"used":3,"max":3}')
  })

  it('shows only the 24h layer when max_devices is unset', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: makeAccount({ max_devices_daily: 3, active_devices_daily: 1 }) }
    })
    const badge = devicesBadge(wrapper)
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toContain('1')
    expect(badge.text()).toContain('3')
    expect(badge.text()).toContain('[24h]')
    expect(badge.attributes('title')).toBe('admin.accounts.capacity.devices.daily:{"used":1,"max":3}')
  })
})
