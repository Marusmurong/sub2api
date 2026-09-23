import { describe, expect, it } from 'vitest'

import { parseReclaudeBundle } from '../reclaudeCredentials'

const validBundle = {
  reclaude_sk: 'sk-rec-abcdef123456',
  reclaude_ed25519_seed: 'a'.repeat(64),
  reclaude_device_id: 43448,
  reclaude_fingerprint: '0123456789abcdef',
  reclaude_gateway_url: 'https://la.route.reclaude.ai',
  reclaude_client_version: 'v1.4.0',
  reclaude_client_platform: 'linux/arm64',
    reclaude_claude_user_id: 'c'.repeat(64),
    reclaude_account_uuid: '9c67eb02-4001-4cde-a6e2-e40f1a71649e',
  reclaude_device_hostname: 'sunqi-dev',
  reclaude_timezone: 'America/New_York',
  reclaude_user_email: 'owner@example.com'
}

describe('parseReclaudeBundle', () => {
  it('解析控制台生成的完整 JSON', () => {
    const result = parseReclaudeBundle(JSON.stringify(validBundle))

    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.values.sk).toBe('sk-rec-abcdef123456')
    expect(result.values.deviceId).toBe('43448')
    expect(result.values.gatewayUrl).toBe('https://la.route.reclaude.ai')
    expect(result.values.deviceHostname).toBe('sunqi-dev')
  })

  it('device_id 是数字，转成表单用的字符串', () => {
    const result = parseReclaudeBundle(JSON.stringify(validBundle))

    expect(result.ok).toBe(true)
    if (!result.ok) return
    // 表单字段是 text 输入，数字直接塞进去会在校验时变成 "43448" 以外的东西
    expect(typeof result.values.deviceId).toBe('string')
  })

  it('不是 JSON 时给出可读错误', () => {
    const result = parseReclaudeBundle('这不是 json')

    expect(result.ok).toBe(false)
    if (result.ok) return
    expect(result.error).toBe('invalidJson')
  })

  it('缺必填字段时明确指出缺哪个', () => {
    const { reclaude_sk, ...withoutSK } = validBundle
    void reclaude_sk

    const result = parseReclaudeBundle(JSON.stringify(withoutSK))

    expect(result.ok).toBe(false)
    if (result.ok) return
    expect(result.error).toBe('missingFields')
    expect(result.missing).toContain('reclaude_sk')
  })

  it('🔴 拒绝原始 device.json —— 它的 gateway_url 是默认值不是 route 节点', () => {
    // device.json 里是 https://www.reclaude.ai，daemon 会 auto-pick；
    // 直接拿来建号会让账号指向一个我们没选定的节点。
    const raw = {
      sk: 'sk-rec-xxx',
      device_id: 43448,
      gateway_url: 'https://www.reclaude.ai',
      public_key_fingerprint: '0123456789abcdef'
    }

    const result = parseReclaudeBundle(JSON.stringify(raw))

    expect(result.ok).toBe(false)
    if (result.ok) return
    expect(result.error).toBe('rawDeviceJson')
  })

  it('空 seed 视为缺字段而不是静默通过', () => {
    const result = parseReclaudeBundle(
      JSON.stringify({ ...validBundle, reclaude_ed25519_seed: '   ' })
    )

    expect(result.ok).toBe(false)
    if (result.ok) return
    expect(result.missing).toContain('reclaude_ed25519_seed')
  })

  it('可选字段缺失不阻塞', () => {
    const { reclaude_user_email, ...withoutEmail } = validBundle
    void reclaude_user_email

    expect(parseReclaudeBundle(JSON.stringify(withoutEmail)).ok).toBe(true)
  })
})

// 🔴 device_id / account_uuid 是 rec 识别设备的依据，缺任一个账号必然跑不通
// （2026-09-24 定位：缺它们就是 bad_envelope / reclaude state mismatch，
// 而那句错误完全看不出是身份字段缺失）。所以设为必填，建号时就拦住。
describe('parseReclaudeBundle — 设备身份字段', () => {
  const full = {
    reclaude_sk: 'sk-rec-abcdef123456',
    reclaude_ed25519_seed: 'a'.repeat(64),
    reclaude_device_id: 44186,
    reclaude_fingerprint: '161b2c4f8d006878',
    reclaude_gateway_url: 'https://www.reclaude.ai',
    reclaude_client_version: 'v1.4.0',
    reclaude_client_platform: 'linux/amd64',
    reclaude_claude_user_id: 'c'.repeat(64),
    reclaude_account_uuid: '9c67eb02-4001-4cde-a6e2-e40f1a71649e',
    reclaude_device_hostname: 'ip-172-31-65-127',
    reclaude_claude_user_id: 'c'.repeat(64),
    reclaude_account_uuid: '9c67eb02-4001-4cde-a6e2-e40f1a71649e'
  }

  it('解析出这两个字段', () => {
    const result = parseReclaudeBundle(JSON.stringify(full))

    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.values.claudeUserId).toBe('c'.repeat(64))
    expect(result.values.accountUuid).toBe('9c67eb02-4001-4cde-a6e2-e40f1a71649e')
  })

  it('缺 claude_user_id 时拒绝', () => {
    const { reclaude_claude_user_id: _omit, ...rest } = full
    const result = parseReclaudeBundle(JSON.stringify(rest))

    expect(result.ok).toBe(false)
    if (result.ok) return
    expect(result.error).toBe('missingFields')
    expect(result.missing).toContain('reclaude_claude_user_id')
  })

  it('缺 account_uuid 时拒绝', () => {
    const { reclaude_account_uuid: _omit, ...rest } = full
    const result = parseReclaudeBundle(JSON.stringify(rest))

    expect(result.ok).toBe(false)
    if (result.ok) return
    expect(result.missing).toContain('reclaude_account_uuid')
  })
})
