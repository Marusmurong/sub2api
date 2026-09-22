package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// stripReclaudeExportSecrets 从导出数据里剥掉 reclaude 的设备凭据。
//
// 导出通道刻意返回 credentials 原文（见 DataAccount 的注释，那是"管理员备份"
// 这一显式行为的一部分）。这里**只**对 reclaude 开口子，其它账号类型的导出
// 行为一个字不变 —— 那是现有备份/恢复流程的契约。
//
// 为什么 reclaude 要例外：
//   - 导出去也没用：数据导入通道不接受 reclaude 类型（V-10），重建不了
//   - 落库虽是密文，但密文 + 泄漏的加密密钥 = 订阅被白嫖 + 设备被顶掉，
//     而凭据终身不换（禁止二次 login），泄漏没有补救手段
//   - 真正的离线备份按设计是**明文、手工、带外**保管的，不依赖这条通道
func stripReclaudeExportSecrets(accountType string, credentials map[string]any) map[string]any {
	if accountType != service.AccountTypeReclaude || credentials == nil {
		return credentials
	}

	out := make(map[string]any, len(credentials))
	for key, value := range credentials {
		if key == service.CredKeyReclaudeSK || key == service.CredKeyReclaudeSeed {
			continue
		}
		out[key] = value
	}
	return out
}
