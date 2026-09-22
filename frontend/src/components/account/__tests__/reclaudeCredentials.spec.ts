import { describe, expect, it } from 'vitest'

import {
  buildReclaudeAccountName,
  buildReclaudeCredentials,
  validateReclaudeForm,
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
    timezone: 'America/Los_Angeles',
    userEmail: 'owner@example.com',
    dailyTokenCap: '2000000',
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

  it('V-5 日 token 上限必须 > 0', () => {
    // 包络未标定就售卖 = 超卖。
    expect(validateReclaudeForm(form({ dailyTokenCap: '0' }))).toBe('dailyCapRequired')
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
