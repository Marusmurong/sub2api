package service

import (
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

// ErrReclaudeImmutableCredential 表示改动了建号后不可变的凭据字段。
var ErrReclaudeImmutableCredential = errors.New("reclaude credential is immutable after creation")

// reclaudeImmutableCredentialKeys 是建号后一律不可改的键。
//
// device_id：设备身份本身。改它等于把这行账号指向另一台设备 —— 而 V-4 的唯一
// 索引只拦「两行同号」，拦不住「一行换号」。
//
// sk / seed：R-4 禁止二次 login ⇒ 凭据终身不可更换。能改就意味着能把一行账号
// 悄悄换成另一份订阅，账号名、设备页面对账、日闸水位会全部对不上。
var reclaudeImmutableCredentialKeys = []string{
	CredKeyReclaudeDeviceID,
	CredKeyReclaudeSK,
	CredKeyReclaudeSeed,
}

// ValidateReclaudeCredentialUpdate 校验一次编辑后的 credentials。
//
// merged 是 MergePreservingSensitiveCreds 之后的结果：敏感键在前端没回传时
// 会被原样合并回来，那不算「改」，所以这里比的是**值**而不是「有没有出现」。
//
// 可改的只有运维字段：网关节点、客户端版本/平台、时区、展示邮箱。
func ValidateReclaudeCredentialUpdate(existing, merged map[string]any) error {
	for _, key := range reclaudeImmutableCredentialKeys {
		before, hadBefore := existing[key]
		after, hasAfter := merged[key]
		if !hadBefore && !hasAfter {
			continue
		}
		if fmt.Sprint(before) != fmt.Sprint(after) {
			return fmt.Errorf("%w: %s", ErrReclaudeImmutableCredential, key)
		}
	}

	// V-9 在编辑路径同样成立。转发层出站前还会再校验一次（credentials 可能被
	// 数据导入改动），但那是最后一道 —— 管理端不该让一个非法值先落库。
	gateway, _ := merged[CredKeyReclaudeGateway].(string)
	if _, err := urlvalidator.ValidateHTTPSURL(gateway, urlvalidator.ValidationOptions{
		AllowedHosts:     ReclaudeAllowedGatewayHosts,
		RequireAllowlist: true,
	}); err != nil {
		return fmt.Errorf("%w: %s", ErrReclaudeGatewayNotAllowed, err.Error())
	}

	return nil
}
