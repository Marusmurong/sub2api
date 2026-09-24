import { describe, expect, it } from 'vitest'
import { accountDisplayEmail } from '../accountDisplayEmail'

// 🔴 reclaude 的邮箱有三个不同的键，含义各不相同：
//   reclaude_claude_email —— **底层 Claude 账号**的邮箱（真机 ~/.claude.json 的
//                            oauthAccount.emailAddress）。运营要看的是这个：
//                            它标识「这台设备当前挂在谁的 Claude 账号上」。
//   reclaude_bound_email  —— 心跳从 /client/account 读到的值。✅ 2026-09-25 实测
//                            它是 **reclaude 订阅账户邮箱**，一个订阅下所有设备
//                            都相同，对「挂在哪个号上」零信息量。
//   reclaude_user_email   —— 建号时填的 rec 订阅账号，恒定不变。
//
// ⚠️ 此前的实现把 reclaude_bound_email 当成底层账号邮箱排在最前，
// 于是所有设备都显示同一个订阅邮箱。
describe('accountDisplayEmail — reclaude', () => {
  it('优先显示底层 Claude 账号邮箱', () => {
    const row = {
      type: 'reclaude',
      extra: { reclaude_bound_email: 'subscription@example.com' },
      credentials: {
        reclaude_claude_email: 'claude-account@example.com',
        reclaude_user_email: 'sub@example.com'
      }
    }

    expect(accountDisplayEmail(row)).toBe('claude-account@example.com')
  })

  it('老账号没采集 claude_email 时回落到订阅邮箱', () => {
    // 显示订阅邮箱好过空白，但它不是首选。
    const row = {
      type: 'reclaude',
      extra: { reclaude_bound_email: 'subscription@example.com' },
      credentials: { reclaude_user_email: 'sub@example.com' }
    }

    expect(accountDisplayEmail(row)).toBe('subscription@example.com')
  })

  it('只有建号邮箱时用它', () => {
    const row = {
      type: 'reclaude',
      extra: {},
      credentials: { reclaude_user_email: 'sub@example.com' }
    }

    expect(accountDisplayEmail(row)).toBe('sub@example.com')
  })

  it('都没有时返回空串', () => {
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
