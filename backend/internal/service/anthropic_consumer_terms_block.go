package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// Anthropic 在账号所有者尚未接受新版消费者条款时，对该账号的每个请求返回 400：
//
//	"We've updated our Consumer Terms and Privacy Policy. You'll need to accept
//	 them in claude.ai with the email in /status to continue."
//
// 这与 400 的常规语义（客户端请求本身有问题、换号也没用）正好相反：它是账号级
// 阻断，换个账号立刻成功，而在原账号上重试永远失败。
//
// 2026-08-12 账号 165 因此变成黑洞：07:32–09:10 共 2175 次请求全部失败，其中
// 1388 个 400 直接打回客户端（同期全站成功仅 1692），速率一路爬到 60/分钟，而它
// 一次都没有被移出轮转——400 既不触发换号，也不属于任何已识别的账号级阻断。
//
// 不能照搬同族的"组织禁用 / 余额耗尽 / 需实名"那样永久禁用：号主接受条款后账号
// 立即自愈（165 在 09:20 恢复并正常服务，无需任何人工操作）。永久禁用会把一个
// 只差点一下的账号锁死，只能靠人工捞回。
//
// 因此这里用临时不可调度 + 冷却，到期自动重试：
//   - 条款已接受 → 请求成功，账号自然回到轮转
//   - 条款未接受 → 再次 400，再冷却一轮
//
// 代价是每个冷却周期损失一个客户端请求（98 分钟窗口约 3 次），相比 1388 次可接受。
const anthropicConsumerTermsCooldown = 30 * time.Minute

// AnthropicConsumerTermsReasonSource 标识这次停调度的来源，写进 temp-unsched 载荷，
// 使后台能把"等号主接受条款"与限流、阈值暂停等其它停调度原因区分开。
const AnthropicConsumerTermsReasonSource = "anthropic_consumer_terms_unaccepted"

// 这条阻断只能由人解除，运维看不懂就永远不会去点，所以文案直接写清动作与位置。
const anthropicConsumerTermsErrorMessage = "账号需登录 claude.ai 接受新版消费者条款后才能继续使用"

// 取子串而非全等：上游改标点或大小写都不该失配。两条特征串取或——前者是条款名，
// 后者是动作指引，任一出现都足以判定。
var anthropicConsumerTermsMarkers = []string{
	"consumer terms",
	"accept them in claude.ai",
}

func isAnthropicConsumerTermsBlock(upstreamMsg string) bool {
	msg := strings.ToLower(upstreamMsg)
	for _, marker := range anthropicConsumerTermsMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// persistAnthropicConsumerTermsBlock 把账号停到冷却结束。
//
// 即使落库失败也照常让调用方换号：这个账号对当前请求确定不可用，客户端不该为
// 我们的存储故障买单。代价是账号没被停下、下一个请求会再撞一次——比直接把 400
// 打回客户端轻。
func (s *RateLimitService) persistAnthropicConsumerTermsBlock(ctx context.Context, account *Account, upstreamMsg string) {
	if s == nil || s.accountRepo == nil || account == nil || account.ID <= 0 {
		return
	}

	until := time.Now().UTC().Add(anthropicConsumerTermsCooldown)
	reason := BuildTempUnschedReasonPayload(AnthropicConsumerTermsReasonSource, anthropicConsumerTermsErrorMessage)

	account.TempUnschedulableUntil = cloneTimePtr(&until)
	account.TempUnschedulableReason = reason
	s.notifyAccountSchedulingBlocked(account, until, AnthropicConsumerTermsReasonSource)

	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("anthropic_consumer_terms_set_temp_unsched_failed",
			"account_id", account.ID, "until", until, "error", err)
		return
	}

	if s.tempUnschedCache != nil {
		if state := tempUnschedStateFromStoredReason(reason, until.Unix()); state != nil {
			if err := s.tempUnschedCache.SetTempUnsched(ctx, account.ID, state); err != nil {
				slog.Warn("anthropic_consumer_terms_cache_set_failed",
					"account_id", account.ID, "error", err)
			}
		}
	}

	slog.Warn("anthropic_consumer_terms_unaccepted",
		"account_id", account.ID,
		"until", until,
		"cooldown", anthropicConsumerTermsCooldown,
		"upstream_message", upstreamMsg)
}
