/**
 * accountDisplayEmail 决定账号名称下方显示哪个邮箱。
 *
 * 独立成文件是为了可测：它原本内联在 7000 行的 AccountsView 里，
 * 而取值顺序错了会让运营看到过期的绑定关系。
 */
export function accountDisplayEmail(row: any): string {
  // 🔴 reclaude 显示**底层 Claude 账号**的邮箱，不是 reclaude 订阅账户邮箱。
  //
  // 两者是不同的东西，运营关心的是前者（它标识「这台设备当前挂在谁的 Claude
  // 账号上」）：
  //   - reclaude_claude_email —— 建号时从真机 ~/.claude.json 的
  //     oauthAccount.emailAddress 采集，例：costenivesunieroi457@gmail.com
  //   - reclaude_bound_email —— 心跳从 /client/account 读到的值，实测是
  //     **reclaude 订阅账户邮箱**（例：jimudubai@gmail.com），一个订阅下的
  //     所有设备都一样，对「这台设备挂在哪个号上」零信息量
  //
  // ⚠️ 2026-09-25 修正：此前把 reclaude_bound_email 当成底层账号邮箱排在最前，
  // 于是所有设备都显示同一个订阅邮箱。
  const claudeEmail = row?.credentials?.reclaude_claude_email
  if (claudeEmail) return String(claudeEmail)

  // 订阅账户邮箱作为兜底：老账号没采集 claude_email，显示它好过空白。
  const reclaudeBound = row?.extra?.reclaude_bound_email
  if (reclaudeBound) return String(reclaudeBound)

  const reclaudeUser = row?.credentials?.reclaude_user_email
  if (reclaudeUser) return String(reclaudeUser)

  return row?.extra?.email_address || row?.extra?.email || row?.credentials?.email || row?.parent_email || ''
}
