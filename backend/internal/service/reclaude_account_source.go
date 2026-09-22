package service

import (
	"context"
	"time"
)

// ReclaudeAccountSource 是适配器需要的**仓储子集**。
//
// 定义成小接口而不是直接吃 AccountRepository：那个接口有七十多个方法，
// 测试为此搭一整套替身，收益为零。AccountRepository 结构性满足本接口。
type ReclaudeAccountSource interface {
	ListAllWithFilters(ctx context.Context, platform, accountType, status, search string, groupID int64, privacyMode string) ([]Account, error)
	GetByIDs(ctx context.Context, ids []int64) ([]*Account, error)
	GetByID(ctx context.Context, id int64) (*Account, error)
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
	SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error
	SetError(ctx context.Context, id int64, errorMsg string) error
	ClearError(ctx context.Context, id int64) error
	SetSchedulable(ctx context.Context, id int64, schedulable bool) error
}

// ReclaudeAccountRepoAdapter 把仓储接到 reclaude 的两个小接口上
// （ReclaudeAccountLister 给节拍器，ReclaudeAccountStore 给执行器）。
//
// 方法名刻意不同（GetAccount / UpdateAccountExtra vs GetByID / UpdateExtra）：
// 那两个小接口是按 reclaude 的语义定义的，不应该为了省一层适配去迁就仓储的命名。
type ReclaudeAccountRepoAdapter struct {
	source ReclaudeAccountSource
}

// NewReclaudeAccountRepoAdapter 构造适配器。
func NewReclaudeAccountRepoAdapter(source ReclaudeAccountSource) *ReclaudeAccountRepoAdapter {
	return &ReclaudeAccountRepoAdapter{source: source}
}

// ListReclaudeAccounts 返回所有需要维持存在感的 reclaude 账号。
//
// 🔴 两处刻意的取舍：
//
//  1. **不传 status 过滤**。仓储的 StatusActive 分支会顺带排掉 temp_unschedulable
//     与限流冷却中的账号。而这两种账号恰恰必须继续心跳 —— 真客户端限流时也不会
//     退出，一批设备同时静默是最该避免的批量特征。是否该「合盖」由
//     ReclaudeScheduler.tickAccount 按各自作息判断，不在这里。
//
//  2. **二次走 GetByIDs**。列表查询不预载 Proxy，而 probe 在 Proxy 为空时拒发
//     （宁可不心跳也不从机房 IP 出去）。直接返回列表结果 = 每台设备心跳全失败。
func (a *ReclaudeAccountRepoAdapter) ListReclaudeAccounts(ctx context.Context) ([]*Account, error) {
	if a == nil || a.source == nil {
		return nil, nil
	}

	rows, err := a.source.ListAllWithFilters(ctx, "", AccountTypeReclaude, "", "", 0, "")
	if err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(rows))
	for i := range rows {
		if rows[i].IsActive() {
			ids = append(ids, rows[i].ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	return a.source.GetByIDs(ctx, ids)
}

// SetTempUnschedulable 实现 ReclaudeAccountStore。
func (a *ReclaudeAccountRepoAdapter) SetTempUnschedulable(
	ctx context.Context, accountID int64, until time.Time, reason string,
) error {
	return a.source.SetTempUnschedulable(ctx, accountID, until, reason)
}

// UpdateAccountExtra 实现 ReclaudeAccountStore。
func (a *ReclaudeAccountRepoAdapter) UpdateAccountExtra(
	ctx context.Context, accountID int64, updates map[string]any,
) error {
	return a.source.UpdateExtra(ctx, accountID, updates)
}

// SetError 实现 ReclaudeAccountStore。仓储侧同时写 status=error 与 schedulable=false。
func (a *ReclaudeAccountRepoAdapter) SetError(
	ctx context.Context, accountID int64, message string,
) error {
	return a.source.SetError(ctx, accountID, message)
}

// ClearAccountError 实现 ReclaudeSelfCheckStore：清错误并置回 active。
func (a *ReclaudeAccountRepoAdapter) ClearAccountError(ctx context.Context, accountID int64) error {
	return a.source.ClearError(ctx, accountID)
}

// SetAccountSchedulable 实现 ReclaudeSelfCheckStore。
//
// 与 ClearAccountError 分两步：status 与 schedulable 在仓储里是两个字段，
// 只清 status 不开 schedulable 的话，自检显示「通过」而账号依然不参与调度。
func (a *ReclaudeAccountRepoAdapter) SetAccountSchedulable(
	ctx context.Context, accountID int64, schedulable bool,
) error {
	return a.source.SetSchedulable(ctx, accountID, schedulable)
}

// GetAccount 实现 ReclaudeAccountStore。
func (a *ReclaudeAccountRepoAdapter) GetAccount(ctx context.Context, accountID int64) (*Account, error) {
	return a.source.GetByID(ctx, accountID)
}
