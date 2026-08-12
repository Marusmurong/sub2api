//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 账号所有者未接受新版消费者条款时，Anthropic 对该账号的每个请求回 400。
// 这与 400 的常规语义相反：不是请求有问题，而是账号不可用——换个号就成功。
// 2026-08-12 账号 165 因此成为黑洞，98 分钟内 2175 次全败却从未被移出轮转。

func consumerTerms400Body(msg string) []byte {
	body, err := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": "invalid_request_error", "message": msg},
	})
	if err != nil {
		panic(err)
	}
	return body
}

const consumerTermsUpstreamMessage = "We've updated our Consumer Terms and Privacy Policy. " +
	"You'll need to accept them in claude.ai with the email in /status to continue."

// 捕获 until/reason —— errorPolicyRepoStub 只记调用次数，断不出冷却时长与载荷。
type consumerTermsRepoStub struct {
	errorPolicyRepoStub
	tempUntil  time.Time
	tempReason string
	tempCount  int
}

func (r *consumerTermsRepoStub) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, reason string) error {
	r.tempCount++
	r.tempUntil = until
	r.tempReason = reason
	return nil
}

func newConsumerTermsSvc() (*RateLimitService, *consumerTermsRepoStub) {
	repo := &consumerTermsRepoStub{}
	return NewRateLimitService(repo, nil, &config.Config{}, nil, nil), repo
}

func TestHandleUpstreamError_ConsumerTermsBlockParksAccountAndFailsOver(t *testing.T) {
	svc, repo := newConsumerTermsSvc()
	account := &Account{ID: 165, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, 400,
		http.Header{}, consumerTerms400Body(consumerTermsUpstreamMessage))

	// true 的语义是"这次别再用这个号"，调用方据此换号重试——在原账号上重试永远失败，
	// 而别的账号能成功，所以客户端不该收到这个 400。
	require.True(t, shouldDisable, "必须换号，否则 400 直接打回客户端")
	require.Equal(t, 1, repo.tempCount, "必须移出轮转，否则继续喷这个黑洞")
	require.Zero(t, repo.setErrCalls, "不得永久禁用：号主接受条款后账号立即自愈")
}

// 冷却到期后自动重试：条款已接受则请求成功、账号自然回到轮转，无需人工干预。
func TestConsumerTermsBlock_UsesBoundedCooldown(t *testing.T) {
	svc, repo := newConsumerTermsSvc()
	account := &Account{ID: 165, Platform: PlatformAnthropic}

	before := time.Now().UTC()
	svc.HandleUpstreamError(context.Background(), account, 400,
		http.Header{}, consumerTerms400Body(consumerTermsUpstreamMessage))

	cooldown := repo.tempUntil.Sub(before)
	require.Greater(t, cooldown, 10*time.Minute, "太短会反复重开风暴，每轮白扔一个客户端请求")
	require.LessOrEqual(t, cooldown, time.Hour, "太长会让已接受条款的账号长时间闲置")
}

// reason 存的是 JSON 载荷（parseTempUnschedReasonPayload 要解），塞裸字符串会污染
// 后台展示与 temp-unsched 缓存。且必须写清"要去 claude.ai 接受条款"——这条阻断
// 只能由人解除，运维看不懂就永远不会去点。
func TestConsumerTermsBlock_ReasonIsStructuredAndActionable(t *testing.T) {
	svc, repo := newConsumerTermsSvc()
	account := &Account{ID: 165, Platform: PlatformAnthropic}

	svc.HandleUpstreamError(context.Background(), account, 400,
		http.Header{}, consumerTerms400Body(consumerTermsUpstreamMessage))

	payload, ok := parseTempUnschedReasonPayload(repo.tempReason)
	require.True(t, ok, "reason 必须是可解析的 JSON 载荷")
	require.Equal(t, AnthropicConsumerTermsReasonSource, payload.Source)
	require.Contains(t, payload.ErrorMessage, "claude.ai", "运维需要知道去哪里解除")
}

// 上游若微调措辞，任一特征串命中即可。
func TestConsumerTermsBlock_MatchesWordingVariants(t *testing.T) {
	for _, msg := range []string{
		consumerTermsUpstreamMessage,
		"We've updated our CONSUMER TERMS and Privacy Policy.",
		"You'll need to accept them in claude.ai with the email in /status to continue.",
	} {
		t.Run(msg[:24], func(t *testing.T) {
			svc, repo := newConsumerTermsSvc()
			account := &Account{ID: 165, Platform: PlatformAnthropic}

			require.True(t, svc.HandleUpstreamError(context.Background(), account, 400,
				http.Header{}, consumerTerms400Body(msg)))
			require.Equal(t, 1, repo.tempCount)
		})
	}
}

// 普通 400 是客户端请求本身的问题，换号同样失败——不得误伤，否则一次超长 prompt
// 就会把整个账号池挨个停掉。
func TestConsumerTermsBlock_LeavesOrdinary400Untouched(t *testing.T) {
	for _, msg := range []string{
		"prompt is too long: 1200000 tokens > 200000 maximum",
		"messages: Unexpected role \"tool\". Allowed roles are \"user\" or \"assistant\".",
		"Invalid request parameters",
	} {
		t.Run(msg[:20], func(t *testing.T) {
			svc, repo := newConsumerTermsSvc()
			account := &Account{ID: 165, Platform: PlatformAnthropic}

			shouldDisable := svc.HandleUpstreamError(context.Background(), account, 400,
				http.Header{}, consumerTerms400Body(msg))

			require.False(t, shouldDisable, "普通 400 换号也失败，不该触发故障转移")
			require.Zero(t, repo.tempCount, "不得停调度")
			require.Zero(t, repo.setErrCalls)
		})
	}
}

// 该文案是 Anthropic 专属，别的平台有各自语义，不能被这条子串误伤。
func TestConsumerTermsBlock_ScopedToAnthropic(t *testing.T) {
	svc, repo := newConsumerTermsSvc()
	account := &Account{ID: 165, Platform: PlatformGemini}

	svc.HandleUpstreamError(context.Background(), account, 400,
		http.Header{}, consumerTerms400Body(consumerTermsUpstreamMessage))

	require.Zero(t, repo.tempCount, "非 Anthropic 平台不应命中该分支")
}
