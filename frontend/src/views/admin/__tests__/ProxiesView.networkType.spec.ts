import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ProxiesView from '../ProxiesView.vue'
import type { Proxy } from '@/types'

const { listProxies, getAllWithCount } = vi.hoisted(() => ({
  listProxies: vi.fn(),
  getAllWithCount: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { proxies: { list: listProxies, getAllWithCount } }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

// DataTable 的真实实现不参与本用例，用一个只渲染 cell-name 插槽的桩，
// 直接拿到名称单元格的产物。
const mountView = () =>
  mount(ProxiesView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="table" /></div>' },
        Pagination: true,
        Select: true,
        Icon: true,
        DataTable: {
          props: ['data'],
          template: `<div>
            <div v-for="row in data" :key="row.id" data-test="name-cell">
              <slot name="cell-name" :row="row" :value="row.name" />
            </div>
          </div>`
        }
      }
    }
  })

const proxyRow = (over: Partial<Proxy>): Proxy =>
  ({
    id: 1,
    name: 'US-20',
    protocol: 'http',
    host: '207.228.201.171',
    port: 6014,
    username: null,
    status: 'active',
    expires_at: null,
    fallback_mode: 'none',
    expiry_warn_days: 0,
    created_at: '2026-09-06T00:00:00Z',
    updated_at: '2026-09-06T00:00:00Z',
    ...over
  }) as Proxy

let wrapper: ReturnType<typeof mountView>

const renderWith = async (rows: Proxy[]) => {
  listProxies.mockResolvedValue({ items: rows, total: rows.length })
  getAllWithCount.mockResolvedValue(rows)
  wrapper = mountView()
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  wrapper?.unmount()
})

describe('ProxiesView 出口 IP 类型标注', () => {
  it('机房 IP 在名称下方显示红色标签', async () => {
    await renderWith([
      proxyRow({
        id: 104,
        name: '[机房勿用]UK-2',
        network_type: 'hosting',
        isp: 'Cogent Communications',
        asn: 'AS174',
        network_type_source: 'proxycheck+ipdata'
      })
    ])

    const cell = wrapper.find('[data-test="name-cell"]')
    expect(cell.text()).toContain('[机房勿用]UK-2')

    const badge = cell.find('span.badge')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('admin.proxies.networkTypeHosting')
    expect(badge.classes()).toContain('badge-danger')
    // tooltip 要给出判定依据，判错时能看出是哪家给的结论
    expect(badge.attributes('title')).toBe('Cogent Communications · AS174 · proxycheck+ipdata')
  })

  it('住宅 IP 显示绿色标签', async () => {
    await renderWith([proxyRow({ network_type: 'residential', isp: 'MxFiber LLC' })])

    const badge = wrapper.find('[data-test="name-cell"] span.badge')
    expect(badge.text()).toBe('admin.proxies.networkTypeResidential')
    expect(badge.classes()).toContain('badge-success')
  })

  it('商业专线显示黄色标签', async () => {
    await renderWith([proxyRow({ network_type: 'business' })])

    const badge = wrapper.find('[data-test="name-cell"] span.badge')
    expect(badge.classes()).toContain('badge-warning')
  })

  // 没检测过、或检测时数据源不可用时不显示标签——空标签比错标签更诚实。
  it('未检测时不渲染标签', async () => {
    await renderWith([proxyRow({})])

    const cell = wrapper.find('[data-test="name-cell"]')
    expect(cell.text()).toContain('US-20')
    expect(cell.find('span.badge').exists()).toBe(false)
  })
})
