package claude

import "testing"

// 🔴 版本号不是一个孤立的数字，它是一组**同时变化**的出站字段中的一个：
//
//	claude-cli/2.1.280                      ← 自动同步只推进这个
//	X-Stainless-Package-Version: 0.112.1    ← 不动
//	X-Stainless-Runtime-Version: v26.3.0    ← 不动
//	anthropic-beta: <三族模板>               ← 不动
//	TLS ClientHello profile 6               ← 不动
//
// 上游 v0.2.8 新增了「每小时从 GitHub Releases 拉最新版」的同步服务。若放任它
// 推进，同步到 2.2.0 时我们会发出「claude-cli/2.2.0 + SDK 0.112.1 + 那套 beta」——
// 一个**真实世界不存在的组合**。版本号落后只是「旧客户端」，字段组合错位是
// 「伪造客户端」，后者严重得多，且失败是静默的：同步成功、日志正常、无报错，
// 直到账号开始被封才知道。
//
// 所以生效版本必须落在我们**实际抓包标定过**的集合内。
func TestOnlyCalibratedVersionsAreSupported(t *testing.T) {
	t.Run("已标定版本通过", func(t *testing.T) {
		for _, version := range CalibratedCLIVersions {
			if !IsSupportedCLIVersion(version) {
				t.Fatalf("已标定版本 %q 被拒", version)
			}
		}
	})

	t.Run("内置基线必须在标定集合里", func(t *testing.T) {
		// 否则进程起来就处于「当前版本不被自己承认」的状态。
		found := false
		for _, version := range CalibratedCLIVersions {
			if version == CLICurrentVersion {
				found = true
			}
		}
		if !found {
			t.Fatalf("内置基线 %q 不在标定集合 %v 中", CLICurrentVersion, CalibratedCLIVersions)
		}
	})

	t.Run("未标定的新版本被拒", func(t *testing.T) {
		// 这正是自动同步会拉到的东西：一个格式合法、版本更高、但我们没抓过包的值。
		for _, version := range []string{"2.9.0", "3.0.0", "2.1.999"} {
			if IsSupportedCLIVersion(version) {
				t.Fatalf("未标定版本 %q 被接受 —— 会发出不存在的字段组合", version)
			}
		}
	})

	t.Run("低于基线的旧版本仍然被拒", func(t *testing.T) {
		// 既有语义不能因为加了标定闸而退化。
		if IsSupportedCLIVersion("2.1.100") {
			t.Fatal("低于内置基线的版本不该通过")
		}
	})

	t.Run("格式非法的值仍然被拒", func(t *testing.T) {
		for _, version := range []string{"", "2.1", "2.1.280-dev", "v2.1.280", "latest"} {
			if IsSupportedCLIVersion(version) {
				t.Fatalf("非法格式 %q 被接受", version)
			}
		}
	})
}

// 标定集合本身要能被审查：每个值都必须是真实抓过包的。
func TestCalibratedVersionsAreWellFormed(t *testing.T) {
	if len(CalibratedCLIVersions) == 0 {
		t.Fatal("标定集合为空 —— 那样任何版本都不可用")
	}
	seen := map[string]bool{}
	for _, version := range CalibratedCLIVersions {
		if seen[version] {
			t.Fatalf("重复项 %q", version)
		}
		seen[version] = true
	}
}
