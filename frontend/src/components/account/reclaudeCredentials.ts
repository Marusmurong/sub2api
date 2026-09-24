/**
 * reclaude 设备凭据的表单模型、校验与提交构造。
 *
 * 为什么单独一个文件而不是塞进 CreateAccountModal：这些校验是**建号后不可逆**
 * 的约束（禁止二次 login、人设只有一次机会），录错了只能重新买一份订阅。
 * 它们值得被单测锁住，而 7000 行的模态框里没法单测。
 *
 * ⚠️ 这里是**第一道**闸，不是唯一一道。后端 ValidateReclaudeAccountInput 会
 * 重做同样的校验，两边必须保持一致；前端这层只是为了让人在点「创建」之前
 * 就看到错在哪。
 */

/** 网关节点硬白名单。与后端 ReclaudeAllowedGatewayHosts 一一对应。 */
export const RECLAUDE_GATEWAY_HOSTS = [
  // 🔴 这四个是真客户端的**候选表全集**（daemon 启动时对它们做 RTT 探测，
  // 取最快的一个）。必须与后端 ReclaudeAllowedGatewayHosts 逐字一致。
  'asia.route.reclaude.ai',
  'la.route.reclaude.ai',
  'misaka.route.reclaude.ai',
  // 真实节点名无 .route（2026-09-24 实测）
  'cloudfront.reclaude.ai'

  // 🔴 **www.reclaude.ai 已于 2026-09-24 移出白名单，不要加回来。**
  // 它不是候选节点，是「所有候选都探测失败」时的兜底值 —— 客户端自己的
  // i18n 写着 "auto-pick failed (no reachable candidates), falling back to %s"。
  // 正常工作的真客户端永远不会停在它上面。详见后端同名常量处的长注释。
] as const

/** SK 的固定前缀（设备页面上可见）。 */
export const RECLAUDE_SK_PREFIX = 'sk-rec-'

/** ed25519 seed 解码后必须恰好 32 字节。 */
const RECLAUDE_SEED_BYTES = 32

export interface ReclaudeFormValues {
  sk: string
  seed: string
  deviceId: string
  fingerprint: string
  gatewayUrl: string
  clientVersion: string
  clientPlatform: string
  deviceHostname: string
  claudeUserId: string
  accountUuid: string
  /** 组织 UUID（~/.claude.json 的 oauthAccount.organizationUuid）。遥测事件必需。 */
  organizationUuid: string
  timezone: string
  userEmail: string
  planTier: string
  proxyId: number | null
}

export type ReclaudeValidationError =
  | 'proxyRequired'
  | 'skInvalid'
  | 'seedInvalid'
  | 'deviceIdInvalid'
  | 'planTierRequired'
  | 'gatewayInvalid'
  | 'clientIdentityRequired'

const HOSTNAME_ILLEGAL = /[^a-z0-9]+/g

/**
 * 生成账号名：`rec/<hostname slug>-<device_id 后四位>`。
 *
 * 与后端 BuildReclaudeAccountName 同构。前端算一份只为**预览**——
 * 落库的名字以后端生成的为准，避免两边算法漂移时用户看到的和库里的不一致。
 *
 * 三段各有理由：`rec/` 前缀让号池列表一眼分得出自建号；hostname 能与 reclaude
 * 设备页面直接对账；device_id 后缀让同一台机器人设下建多个时不撞名。
 */
export function buildReclaudeAccountName(hostname: string, deviceId: string): string {
  const slug =
    hostname.trim().toLowerCase().replace(HOSTNAME_ILLEGAL, '-').replace(/^-+|-+$/g, '') || 'device'

  const digits = deviceId.trim()
  const suffix = digits.length > 4 ? digits.slice(-4) : digits
  return `rec/${slug}-${suffix}`
}

/** decodeSeedByteLength 返回 seed 解码后的字节数；无法解码时返回 -1。 */
function decodeSeedByteLength(encoded: string): number {
  const trimmed = encoded.trim()
  if (trimmed === '') return -1

  if (/^[0-9a-fA-F]+$/.test(trimmed) && trimmed.length % 2 === 0) {
    return trimmed.length / 2
  }

  try {
    const binary = atob(trimmed.replace(/-/g, '+').replace(/_/g, '/'))
    return binary.length
  } catch {
    return -1
  }
}

/**
 * validateReclaudeForm 返回第一条不通过的校验；全部通过时返回 null。
 *
 * 全部是**拒绝创建**而不是警告：这些约束错了之后没有修复路径。
 * 唯一的例外是指纹自校验（V-3），推导算法还没结案，后端按警告处理，前端不拦。
 */
export function validateReclaudeForm(values: ReclaudeFormValues): ReclaudeValidationError | null {
  // V-1：无代理 = 直接用机房 IP。
  if (!values.proxyId || values.proxyId <= 0) return 'proxyRequired'

  if (!values.sk.trim().startsWith(RECLAUDE_SK_PREFIX)) return 'skInvalid'

  // V-2：长度不对说明从 keychain / device.key 里抠错了东西，签名必然全挂。
  if (decodeSeedByteLength(values.seed) !== RECLAUDE_SEED_BYTES) return 'seedInvalid'

  // V-4 的应用层部分；全局唯一性由 DB partial unique index 兜底。
  const deviceId = Number(values.deviceId.trim())
  if (!Number.isInteger(deviceId) || deviceId <= 0) return 'deviceIdInvalid'

  // V-5：包络未标定就售卖 = 超卖。
  if (!findReclaudePlanTier(values.planTier)) return 'planTierRequired'

  // V-9：https + 四节点硬白名单。默认配置下后端 SSRF 校验不生效，这是唯一防线。
  if (!isAllowedReclaudeGateway(values.gatewayUrl)) return 'gatewayInvalid'

  if (values.clientVersion.trim() === '' || values.clientPlatform.trim() === '') {
    return 'clientIdentityRequired'
  }

  return null
}

/** isAllowedReclaudeGateway 判断网关地址是否是白名单里的 https 节点。 */
export function isAllowedReclaudeGateway(rawUrl: string): boolean {
  let parsed: URL
  try {
    parsed = new URL(rawUrl.trim())
  } catch {
    return false
  }
  if (parsed.protocol !== 'https:') return false
  return (RECLAUDE_GATEWAY_HOSTS as readonly string[]).includes(parsed.hostname)
}

/**
 * buildReclaudeCredentials 构造提交给后端的 credentials。
 *
 * 🔴 不生成 `reclaude_synthetic_account_uuid`：那个值由后端建号时随机生成一次、
 * 永不变。前端塞一个进来会被当成人工指定，而它是上游眼里「这台设备是谁」的唯一锚点。
 *
 * sk 与 seed 以**明文**提交，由后端加密落库（V-8 会先确认加密密钥已显式配置，
 * 否则拒绝创建 —— 密钥未配置时每次启动随机生成，重启即导致凭据永久不可解密）。
 */
export function buildReclaudeCredentials(values: ReclaudeFormValues): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    reclaude_sk: values.sk.trim(),
    reclaude_ed25519_seed: values.seed.trim(),
    reclaude_device_id: Number(values.deviceId.trim()),
    reclaude_fingerprint: values.fingerprint.trim(),
    reclaude_gateway_url: values.gatewayUrl.trim(),
    reclaude_client_version: values.clientVersion.trim(),
    reclaude_client_platform: values.clientPlatform.trim(),
    reclaude_device_hostname: values.deviceHostname.trim(),
    reclaude_claude_user_id: values.claudeUserId.trim(),
    reclaude_account_uuid: values.accountUuid.trim()
  }

  // 组织 UUID：遥测事件的 auth 块必带。
  // 🔴 缺它时后端**整批不发遥测**（不编造），所以留空是安全的降级，
  // 而不是错误 —— 见后端 BuildReclaudeEventBatch。
  if (values.organizationUuid.trim() !== '') {
    credentials.reclaude_organization_uuid = values.organizationUuid.trim()
  }

  // 时区决定「模拟关机」作息的生成，留空则后端按默认时区生成。
  if (values.timezone.trim() !== '') {
    credentials.reclaude_timezone = values.timezone.trim()
  }
  if (values.userEmail.trim() !== '') {
    credentials.reclaude_user_email = values.userEmail.trim()
  }

  return credentials
}

/** RECLAUDE_PLAN_TIER_EXTRA_KEY 是套餐档位在 Account.Extra 里的键。 */
export const RECLAUDE_PLAN_TIER_EXTRA_KEY = 'reclaude_plan_tier'

/**
 * RECLAUDE_PLAN_TIERS 是建号时可选的套餐档位。
 *
 * 🔴 必须与后端 internal/service/reclaude_plan_tier.go 保持一致。
 * dailyLimitUsd 在这里只用于**展示**——真正落库的限额由后端按档位 id 换算，
 * 前端不参与计算，否则两边各算一次必然漂移。
 *
 * 基准 $600 的来历：2026-09-22 从号池里三个官方 oauth 号反推的日峰值消费
 * （$592 / $396 / $356），且期间没有任何一个打到限流 —— 所以它是**下限**
 * 而不是天花板。跑出真实数据后要回来调。
 */
export const RECLAUDE_PLAN_TIERS = [
  { id: '20x', label: '20X', dailyLimitUsd: 600 },
  { id: '20x-carpool-2', label: '20X 拼车-2', dailyLimitUsd: 300 },
  { id: '20x-carpool-4', label: '20X 拼车-4', dailyLimitUsd: 150 },
  { id: '5x', label: '5X', dailyLimitUsd: 300 }
] as const

export type ReclaudePlanTier = (typeof RECLAUDE_PLAN_TIERS)[number]

/** findReclaudePlanTier 按 id 查档位；未知 id 返回 undefined。 */
export function findReclaudePlanTier(id: string): ReclaudePlanTier | undefined {
  return RECLAUDE_PLAN_TIERS.find((tier) => tier.id === id.trim())
}

/**
 * buildReclaudeExtra 构造账号级 extra。
 *
 * 只带档位 id：日限额由后端换算后写进 quota_daily_limit，走既有的美元配额闸。
 */
export function buildReclaudeExtra(values: ReclaudeFormValues): Record<string, unknown> {
  return { [RECLAUDE_PLAN_TIER_EXTRA_KEY]: values.planTier.trim() }
}

/**
 * 控制台导出的建号凭据。字段名与后端 credentials key 一一对应 ——
 * 两边共用同一个契约（见 reclaude-lab/console/export.go 的 CredentialBundle）。
 */
export interface ReclaudeBundle {
  reclaude_sk: string
  reclaude_ed25519_seed: string
  reclaude_device_id: number | string
  reclaude_fingerprint: string
  reclaude_gateway_url: string
  reclaude_client_version: string
  reclaude_client_platform: string
  reclaude_device_hostname: string
  // 🔴 rec 服务端按这两个值识别设备（进信封内层 body 的 metadata.user_id）。
  // 来源都是 login 那台机器的 ~/.claude.json：userID 与 account uuid。
  // 缺任一个 → bad_envelope / reclaude state mismatch，而那句错误完全看不出
  // 是身份字段缺失（2026-09-24 花了一整天才定位到）。
  //
  // ⚠️ reclaude_claude_user_id 与 reclaude_device_id 同名不同物：
  // 后者是设备号（44186，发在 X-Reclaude-Device-Id 头），前者是 Claude Code
  // 的 userID（64 位 hex，发在 body 里）。
  reclaude_claude_user_id: string
  reclaude_account_uuid: string
  /** 组织 UUID。缺失时后端不发遥测事件（不编造），因此不列入必填。 */
  reclaude_organization_uuid?: string
  reclaude_timezone?: string
  reclaude_user_email?: string
}

export type ReclaudeParseResult =
  | { ok: true; values: Partial<ReclaudeFormValues> }
  | { ok: false; error: 'invalidJson' | 'rawDeviceJson' | 'missingFields'; missing?: string[] }

/** 必填字段。缺任何一个都不该静默通过 —— 建号后才发现就晚了。 */
const REQUIRED_BUNDLE_KEYS: (keyof ReclaudeBundle)[] = [
  'reclaude_sk',
  'reclaude_ed25519_seed',
  'reclaude_device_id',
  'reclaude_fingerprint',
  'reclaude_gateway_url',
  'reclaude_client_version',
  'reclaude_client_platform',
  // 设备身份：缺了账号建得出来但一定跑不通，必须在建号时就拦住。
  'reclaude_claude_user_id',
  'reclaude_account_uuid'
]

/**
 * parseReclaudeBundle 解析粘贴进来的建号 JSON。
 *
 * 🔴 拒绝原始 `device.json`：它的 `gateway_url` 是默认值
 * （`https://www.reclaude.ai`，daemon 会 auto-pick），而我们**不做 auto-pick**，
 * 必须固定一个 route 节点。直接拿原始文件建号，账号会指向一个我们没选定的节点。
 * 正确做法是用本地控制台的「生成建号 JSON」，那里会带上选定的节点。
 */
export function parseReclaudeBundle(raw: string): ReclaudeParseResult {
  let parsed: Record<string, unknown>
  try {
    parsed = JSON.parse(raw.trim()) as Record<string, unknown>
  } catch {
    return { ok: false, error: 'invalidJson' }
  }
  if (!parsed || typeof parsed !== 'object') return { ok: false, error: 'invalidJson' }

  // 原始 device.json 的特征：有 sk/device_id 但没有 reclaude_ 前缀的键。
  if (parsed.sk !== undefined && parsed.reclaude_sk === undefined) {
    return { ok: false, error: 'rawDeviceJson' }
  }

  const text = (key: string): string => {
    const value = parsed[key]
    if (typeof value === 'number') return String(value)
    return typeof value === 'string' ? value.trim() : ''
  }

  const missing = REQUIRED_BUNDLE_KEYS.filter(key => text(key) === '')
  if (missing.length > 0) return { ok: false, error: 'missingFields', missing }

  return {
    ok: true,
    values: {
      sk: text('reclaude_sk'),
      seed: text('reclaude_ed25519_seed'),
      deviceId: text('reclaude_device_id'),
      fingerprint: text('reclaude_fingerprint'),
      gatewayUrl: text('reclaude_gateway_url'),
      clientVersion: text('reclaude_client_version'),
      clientPlatform: text('reclaude_client_platform'),
      deviceHostname: text('reclaude_device_hostname'),
      claudeUserId: text('reclaude_claude_user_id'),
      accountUuid: text('reclaude_account_uuid'),
      organizationUuid: text('reclaude_organization_uuid'),
      timezone: text('reclaude_timezone'),
      userEmail: text('reclaude_user_email')
    }
  }
}
