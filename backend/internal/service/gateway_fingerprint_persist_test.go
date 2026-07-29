package service

import (
	"context"
	"testing"
)

// fakeExtraRepo 只实现本测试用到的 UpdateExtra，其余方法由内嵌接口占位。
type fakeExtraRepo struct {
	AccountRepository
	calls []map[string]any
	ids   []int64
	err   error
}

func (r *fakeExtraRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	if r.err != nil {
		return r.err
	}
	r.ids = append(r.ids, id)
	r.calls = append(r.calls, updates)
	return nil
}

func fingerprintOf(t *testing.T, updates map[string]any) map[string]any {
	t.Helper()
	fp, ok := updates["fingerprint"].(map[string]any)
	if !ok {
		t.Fatalf("updates 里没有 fingerprint 对象: %#v", updates)
	}
	return fp
}

func TestPersistFingerprintClientIDWritesOnce(t *testing.T) {
	repo := &fakeExtraRepo{}
	svc := &GatewayService{accountRepo: repo}

	// 启用了 TLS 指纹 → 走强制路径，但 extra 里还没有 client_id
	account := &Account{ID: 36, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true}}
	fp := &Fingerprint{ClientID: "a83160bc0665977eea483cb9a91a36a5a7f215587b72c9150aeac87e8517b364"}

	svc.persistFingerprintClientID(t.Context(), account, fp)

	if len(repo.calls) != 1 {
		t.Fatalf("应写入一次，实际 %d 次", len(repo.calls))
	}
	if repo.ids[0] != 36 {
		t.Errorf("写入的账号 id = %d, want 36", repo.ids[0])
	}
	if got := fingerprintOf(t, repo.calls[0])["client_id"]; got != fp.ClientID {
		t.Errorf("client_id = %v, want %s", got, fp.ClientID)
	}

	// 已经一致时不应重复写
	account.Extra["fingerprint"] = map[string]any{"client_id": fp.ClientID}
	svc.persistFingerprintClientID(t.Context(), account, fp)
	if len(repo.calls) != 1 {
		t.Errorf("值已一致不应再写，实际累计 %d 次", len(repo.calls))
	}
}

// 已有的 fingerprint 字段（os/arch/cli_version 等，可能是管理员手工设定的）
// 必须保留——UpdateExtra 是顶层合并，整块传入会覆盖。
func TestPersistFingerprintClientIDPreservesExistingFields(t *testing.T) {
	repo := &fakeExtraRepo{}
	svc := &GatewayService{accountRepo: repo}

	account := &Account{
		ID: 35,
		Extra: map[string]any{
			"enable_tls_fingerprint": true,
			"fingerprint": map[string]any{
				"os":          "MacOS",
				"arch":        "arm64",
				"cli_version": "2.1.220",
				"source":      "tls_profile_align",
			},
		},
	}
	svc.persistFingerprintClientID(t.Context(), account, &Fingerprint{ClientID: "deadbeef"})

	if len(repo.calls) != 1 {
		t.Fatalf("应写入一次，实际 %d 次", len(repo.calls))
	}
	got := fingerprintOf(t, repo.calls[0])
	for k, want := range map[string]string{
		"os": "MacOS", "arch": "arm64", "cli_version": "2.1.220", "source": "tls_profile_align",
	} {
		if got[k] != want {
			t.Errorf("原有字段 %s 丢失或被改：got %v, want %s", k, got[k], want)
		}
	}
	if got["client_id"] != "deadbeef" {
		t.Errorf("client_id 未写入: %v", got["client_id"])
	}
}

// legacy 路径（既无 extra.fingerprint 也没开 TLS 指纹）不写：
// 凭空造出 extra.fingerprint 会让 HasForcedFingerprint() 翻真，
// 把「按客户端头派生」改成「锁定身份」，那是行为变更而非持久化。
func TestPersistFingerprintClientIDSkipsLegacyPath(t *testing.T) {
	repo := &fakeExtraRepo{}
	svc := &GatewayService{accountRepo: repo}

	account := &Account{ID: 5, Extra: map[string]any{"base_rpm": 10}}
	svc.persistFingerprintClientID(t.Context(), account, &Fingerprint{ClientID: "deadbeef"})

	if len(repo.calls) != 0 {
		t.Errorf("legacy 路径不应写入，实际 %d 次", len(repo.calls))
	}
}

func TestPersistFingerprintClientIDIsSafeOnEdgeCases(t *testing.T) {
	repo := &fakeExtraRepo{}
	svc := &GatewayService{accountRepo: repo}
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true}}

	// 空 ClientID / nil 入参 / 无 repo 都不得 panic，也不得写入
	svc.persistFingerprintClientID(t.Context(), account, &Fingerprint{ClientID: "   "})
	svc.persistFingerprintClientID(t.Context(), account, nil)
	svc.persistFingerprintClientID(t.Context(), nil, &Fingerprint{ClientID: "x"})
	(&GatewayService{}).persistFingerprintClientID(t.Context(), account, &Fingerprint{ClientID: "x"})

	if len(repo.calls) != 0 {
		t.Errorf("边界情况不应写入，实际 %d 次", len(repo.calls))
	}
}

// 写库失败只告警，不得影响请求。
func TestPersistFingerprintClientIDSwallowsRepoError(t *testing.T) {
	repo := &fakeExtraRepo{err: context.DeadlineExceeded}
	svc := &GatewayService{accountRepo: repo}
	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true}}

	svc.persistFingerprintClientID(t.Context(), account, &Fingerprint{ClientID: "abc"})
	// 不 panic、不返回错误即可
}
