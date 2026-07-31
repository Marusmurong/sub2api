import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const pollOrderStatus = vi.hoisted(() => vi.fn())
const cancelOrder = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())
const toCanvas = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key,
    }),
  }
})

vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => ({ pollOrderStatus }),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError }),
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: { cancelOrder },
}))

vi.mock('qrcode', () => ({
  default: { toCanvas },
}))

import USDTPaymentPanel from '../USDTPaymentPanel.vue'
import type { USDTPaymentInfo } from '@/types/payment'

const baseUSDT: USDTPaymentInfo = {
  address: 'TJmmqjb1DK9TTZbQXzRQ2AuA94z4gKAPFh',
  network: 'TRC20',
  token_contract: 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t',
  amount_usdt: '13.4837',
  rate: '7.4213',
  rate_source: 'coingecko',
  rate_quoted_at: '2026-07-31T10:32:15Z',
  expires_at: '2099-01-01T00:00:00Z',
}

function mountPanel(overrides: Partial<USDTPaymentInfo> = {}) {
  return mount(USDTPaymentPanel, {
    props: {
      orderId: 42,
      payAmount: 100,
      expiresAt: '2099-01-01T00:00:00Z',
      usdt: { ...baseUSDT, ...overrides },
      orderType: 'balance',
    },
    global: {
      stubs: { Icon: true },
    },
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  pollOrderStatus.mockResolvedValue(null)
  toCanvas.mockResolvedValue(undefined)
})

describe('USDTPaymentPanel', () => {
  it('shows the receiving address the customer must transfer to', () => {
    const wrapper = mountPanel()
    expect(wrapper.get('[data-test="usdt-address"]').text()).toBe(baseUSDT.address)
  })

  // The last two decimals are this order's reconciliation tag, so the figure is
  // rendered verbatim. Reformatting or trimming it would leave the customer
  // sending an amount the backend cannot match to their order.
  it('shows the exact four-decimal amount without reformatting it', () => {
    const wrapper = mountPanel()
    expect(wrapper.get('[data-test="usdt-amount"]').text()).toBe('13.4837')
  })

  it('keeps trailing zeros in the amount', () => {
    const wrapper = mountPanel({ amount_usdt: '13.4800' })
    expect(wrapper.get('[data-test="usdt-amount"]').text()).toBe('13.4800')
  })

  it('shows the rate the order was priced at', () => {
    const wrapper = mountPanel()
    expect(wrapper.get('[data-test="usdt-rate"]').text()).toContain('7.4213')
  })

  // Both warnings describe losses that cannot be undone, so they are always on
  // screen rather than behind a tooltip or a collapsed section.
  it('always warns about the exact amount and the network', () => {
    const wrapper = mountPanel()

    const exact = wrapper.get('[data-test="usdt-exact-warning"]')
    expect(exact.text()).toContain('13.4837')

    const network = wrapper.get('[data-test="usdt-network-warning"]')
    expect(network.text()).toContain('TRC20')
  })

  // The QR payload is the bare address, not a tron: payment URI — wallet
  // support for those URIs is inconsistent, while every wallet can scan an
  // address.
  it('renders the bare address into the QR code', async () => {
    mountPanel()
    await flushPromises()

    expect(toCanvas).toHaveBeenCalled()
    expect(toCanvas.mock.calls[0][1]).toBe(baseUSDT.address)
  })
})
