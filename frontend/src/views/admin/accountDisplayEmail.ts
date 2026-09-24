/**
 * accountDisplayEmail 决定账号名称下方显示哪个邮箱。
 *
 * 独立成文件是为了可测：它原本内联在 7000 行的 AccountsView 里，
 * 而取值顺序错了会让运营看到过期的绑定关系。
 */
export function accountDisplayEmail(row: any): string {
  // reclaude 优先显示**当前实际绑定**的底层账号邮箱。
  // 对方会静默换号，换了这个值就变 —— 那是供给稳定性的直接信号，
  // 比建号时填的订阅邮箱（恒定不变）有用得多。
  const reclaudeBound = row?.extra?.reclaude_bound_email
  if (reclaudeBound) return String(reclaudeBound)

  const reclaudeUser = row?.credentials?.reclaude_user_email
  if (reclaudeUser) return String(reclaudeUser)

  return row?.extra?.email_address || row?.extra?.email || row?.credentials?.email || row?.parent_email || ''
}
