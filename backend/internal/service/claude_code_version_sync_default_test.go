package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeVersionSyncSettingRepo struct {
	SettingRepository
	value string
	err   error
}

func (f *fakeVersionSyncSettingRepo) GetValue(context.Context, string) (string, error) {
	return f.value, f.err
}

// 🔴 版本自动同步默认**关闭**（上游默认开启）。
//
// 同步服务只推进版本号，不动 X-Stainless-Package-Version / Runtime-Version /
// beta 模板 / TLS profile。拉到一个我们没抓过包的版本时，发出的是
// 「新版本号 + 旧 SDK + 旧 beta」——真实世界不存在的组合，比版本号落后严重得多。
//
// claude.CalibratedCLIVersions 已经挡住了未标定版本，这里的默认关闭是第二道：
// 即便将来标定集合放宽，也不该由一个后台定时任务替我们决定出站身份。
func TestClaudeCodeVersionAutoSyncDefaultsOff(t *testing.T) {
	newService := func(repo SettingRepository) *ClaudeCodeVersionSyncService {
		return &ClaudeCodeVersionSyncService{settingRepo: repo}
	}

	t.Run("未配置时关闭", func(t *testing.T) {
		svc := newService(&fakeVersionSyncSettingRepo{value: ""})

		require.False(t, svc.autoSyncEnabled(context.Background()),
			"未配置应关闭 —— 出站身份不该由后台定时任务默认接管")
	})

	t.Run("读取失败时关闭", func(t *testing.T) {
		// 上游是「读取失败保持开启，避免数据库抖动停掉版本跟随」。
		// 对我们相反：失败时宁可不跟随，也不要在不确定状态下改出站身份。
		svc := newService(&fakeVersionSyncSettingRepo{err: context.DeadlineExceeded})

		require.False(t, svc.autoSyncEnabled(context.Background()))
	})

	t.Run("显式开启时才开启", func(t *testing.T) {
		svc := newService(&fakeVersionSyncSettingRepo{value: "true"})

		require.True(t, svc.autoSyncEnabled(context.Background()))
	})
}
