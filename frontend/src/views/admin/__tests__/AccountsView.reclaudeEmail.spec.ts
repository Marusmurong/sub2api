import { describe, expect, it } from 'vitest'
import { accountDisplayEmail } from '../accountDisplayEmail'

// 🔴 reclaude 的邮箱存在两个不同的键，且含义不同：
//   reclaude_bound_email —— 当前**实际绑定**的底层 Claude 账号（对方会静默换号，
//                            换了就变；这是运营最该看到的值）
//   reclaude_user_email  —— 建号时填的 rec 订阅账号，恒定不变
// 优先显示前者：它变了说明对方换号了，而那是供给稳定性的直接信号。
describe('accountDisplayEmail — reclaude', () => {
  it('优先显示实际绑定的邮箱', () => {
    const row = {
      type: 'reclaude',
      extra: { reclaude_bound_email: 'bound@example.com' },
      credentials: { reclaude_user_email: 'sub@example.com' }
    }

    expect(accountDisplayEmail(row)).toBe('bound@example.com')
  })

  it('尚未换过号时回落到订阅邮箱', () => {
    const row = {
      type: 'reclaude',
      extra: {},
      credentials: { reclaude_user_email: 'sub@example.com' }
    }

    expect(accountDisplayEmail(row)).toBe('sub@example.com')
  })

  it('两者都没有时返回空串', () => {
    expect(accountDisplayEmail({ type: 'reclaude', extra: {}, credentials: {} })).toBe('')
  })

  it('不影响其它账号类型的既有取值顺序', () => {
    expect(accountDisplayEmail({ extra: { email_address: 'a@x.com', email: 'b@x.com' } })).toBe('a@x.com')
    expect(accountDisplayEmail({ extra: { email: 'b@x.com' } })).toBe('b@x.com')
    expect(accountDisplayEmail({ credentials: { email: 'c@x.com' } })).toBe('c@x.com')
    expect(accountDisplayEmail({ parent_email: 'p@x.com' })).toBe('p@x.com')
    expect(accountDisplayEmail({})).toBe('')
  })
})
