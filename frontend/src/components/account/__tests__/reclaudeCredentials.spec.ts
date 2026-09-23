import { describe, expect, it } from 'vitest'

import {
  buildReclaudeAccountName,
  buildReclaudeCredentials,
  buildReclaudeExtra,
  validateReclaudeForm,
  RECLAUDE_PLAN_TIERS,
  RECLAUDE_PLAN_TIER_EXTRA_KEY,
  type ReclaudeFormValues
} from '../reclaudeCredentials'

function form(overrides: Partial<ReclaudeFormValues> = {}): ReclaudeFormValues {
  return {
    sk: 'sk-rec-abcdef123456',
    seed: 'a'.repeat(64),
    deviceId: '43448',
    fingerprint: '0123456789abcdef',
    gatewayUrl: 'https://la.route.reclaude.ai',
    clientVersion: 'v1.4.0',
    clientPlatform: 'linux/amd64',
    deviceHostname: 'MBP-Dev',
    claudeUserId: 'c'.repeat(64),
    accountUuid: '9c67eb02-4001-4cde-a6e2-e40f1a71649e',
    timezone: 'America/Los_Angeles',
    userEmail: 'owner@example.com',
    planTier: '20x',
    proxyId: 7,
    ...overrides
  }
}

describe('buildReclaudeAccountName', () => {
  it('生成 rec/ 前缀 + hostname slug + device_id 后四位', () => {
    expect(buildReclaudeAccountName('MBP-Dev', '43448')).toBe('rec/mbp-dev-3448')
  })

  it('device_id 不足四位时原样用完', () => {
    expect(buildReclaudeAccountName('box', '77')).toBe('rec/box-77')
  })

  it('hostname 含非法字符时归一成连字符', () => {
    expect(buildReclaudeAccountName('张三的 Mac_Book!!', '12345')).toBe('rec/mac-book-2345')
  })

  it('hostname 为空时回落到 device', () => {
    expect(buildReclaudeAccountName('', '12345')).toBe('rec/device-2345')
  })
})

describe('validateReclaudeForm', () => {
  it('填全时通过', () => {
    expect(validateReclaudeForm(form())).toBeNull()
  })

  it('V-1 代理不得为空', () => {
    // 无代理 = 直接用机房 IP，出口 IP 稳定性是隐蔽性参数之一。
    expect(validateReclaudeForm(form({ proxyId: null }))).toBe('proxyRequired')
  })

  it('V-2 seed 解码后必须 32 字节', () => {
    expect(validateReclaudeForm(form({ seed: 'abcd' }))).toBe('seedInvalid')
  })

  it('接受 base64 形式的 seed', () => {
    const base64Seed = Buffer.from(new Uint8Array(32).fill(7)).toString('base64')
    expect(validateReclaudeForm(form({ seed: base64Seed }))).toBeNull()
  })

  it('sk 必须是 sk-rec- 前缀', () => {
    expect(validateReclaudeForm(form({ sk: 'sk-ant-xxx' }))).toBe('skInvalid')
  })

  it('V-4 device_id 必须为正整数', () => {
    expect(validateReclaudeForm(form({ deviceId: '0' }))).toBe('deviceIdInvalid')
    expect(validateReclaudeForm(form({ deviceId: 'abc' }))).toBe('deviceIdInvalid')
  })

  it('V-5 必须选定套餐档位', () => {
    // 包络未标定就售卖 = 超卖。
    expect(validateReclaudeForm(form({ planTier: '' }))).toBe('planTierRequired')
  })

  it('V-9 网关必须是已知节点', () => {
    expect(validateReclaudeForm(form({ gatewayUrl: 'https://evil.example.com' }))).toBe('gatewayInvalid')
    expect(validateReclaudeForm(form({ gatewayUrl: 'http://la.route.reclaude.ai' }))).toBe('gatewayInvalid')
  })

  it('客户端身份两项必填', () => {
    expect(validateReclaudeForm(form({ clientVersion: '' }))).toBe('clientIdentityRequired')
    expect(validateReclaudeForm(form({ clientPlatform: '  ' }))).toBe('clientIdentityRequired')
  })
})

describe('buildReclaudeCredentials', () => {
  it('device_id 以数字提交，其余字段原样带上', () => {
    const credentials = buildReclaudeCredentials(form())

    expect(credentials.reclaude_device_id).toBe(43448)
    expect(credentials.reclaude_sk).toBe('sk-rec-abcdef123456')
    expect(credentials.reclaude_gateway_url).toBe('https://la.route.reclaude.ai')
    expect(credentials.reclaude_device_hostname).toBe('MBP-Dev')
    expect(credentials.reclaude_timezone).toBe('America/Los_Angeles')
  })

  it('可选的邮箱留空时不写入该键', () => {
    expect(buildReclaudeCredentials(form({ userEmail: '   ' }))).not.toHaveProperty(
      'reclaude_user_email'
    )
  })

  it('不带 synthetic uuid —— 那是后端生成的，前端塞进来会被当成人工指定', () => {
    expect(buildReclaudeCredentials(form())).not.toHaveProperty(
      'reclaude_synthetic_account_uuid'
    )
  })
})

// 套餐档位取代了手填的日 token 上限。
//
// 🔴 换算关系错了就是超卖，而超卖在这个模型里没有补救手段 ——
// 拼车 N 人就是把一份 20X 切 N 份，5X 是 20X 的**一半**（不是四分之一）。
describe('reclaude 套餐档位', () => {
  it('档位表与后端一致：拼车按份数切、5X 是 20X 的一半', () => {
    const tierOf = (id: string) => RECLAUDE_PLAN_TIERS.find((tier) => tier.id === id)!
    const base = tierOf('20x').dailyLimitUsd

    expect(tierOf('20x-carpool-2').dailyLimitUsd).toBe(base / 2)
    expect(tierOf('20x-carpool-4').dailyLimitUsd).toBe(base / 4)
    expect(tierOf('5x').dailyLimitUsd).toBe(base / 2)
  })

  it('没选档位时校验不通过', () => {
    // 放行的话账号拿不到日限额，调度端会判「未标定」直接不可调度 ——
    // 表面是建号成功，实际是个永远不会被调度的死号。
    const values = { ...form(), planTier: '' }

    expect(validateReclaudeForm(values)).toBe('planTierRequired')
  })

  it('未知档位时校验不通过', () => {
    const values = { ...form(), planTier: '50x' }

    expect(validateReclaudeForm(values)).toBe('planTierRequired')
  })

  it('extra 里带的是档位 id，不是手填的数字', () => {
    // 限额由后端按档位换算，前端不参与计算 —— 两边各算一次必然漂移。
    const extra = buildReclaudeExtra({ ...form(), planTier: '20x-carpool-4' })

    expect(extra[RECLAUDE_PLAN_TIER_EXTRA_KEY]).toBe('20x-carpool-4')
  })
})
