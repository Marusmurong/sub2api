//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// 返现流水：写入 payment_orders 让客户能在 /orders 上查到自己的返现。
//
// 这些记录不是真实支付，两个刻意留空的字段是设计关键：
//   - paid_at 留 NULL → 不进收入看板、不占用户每日充值上限
//   - provider_instance_id 留 NULL → 不进渠道日额度统计

func newCashbackTestSvc(t *testing.T) (*PaymentService, *dbent.Client, *dbent.User) {
	t.Helper()
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)

	user, err := client.User.Create().
		SetEmail("vip@example.com").
		SetPasswordHash("hash").
		SetUsername("vip-user").
		Save(ctx)
	require.NoError(t, err)

	userRepo := &mockUserRepo{getByIDUser: &User{
		ID: user.ID, Email: user.Email, Username: user.Username,
	}}
	return &PaymentService{entClient: client, userRepo: userRepo}, client, user
}

func TestCreateCashbackOrderRecord_FieldsAndExclusions(t *testing.T) {
	ctx := context.Background()
	svc, client, user := newCashbackTestSvc(t)

	// 来源充值订单，带币种快照与 IP，用于验证继承
	src, err := client.PaymentOrder.Create().
		SetUserID(user.ID).SetUserEmail(user.Email).SetUserName(user.Username).
		SetAmount(100).SetPayAmount(100).SetFeeRate(0).
		SetRechargeCode("PAY-SRC").SetOutTradeNo("sub2_src_order").
		SetPaymentType(payment.TypeAlipay).SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now()).SetClientIP("203.0.113.9").SetSrcHost("pay.example.com").
		SetProviderInstanceID("inst-1").SetProviderKey("alipay").
		SetProviderSnapshot(map[string]any{"currency": "USD", "provider_key": "alipay"}).
		Save(ctx)
	require.NoError(t, err)

	order, created, err := svc.CreateCashbackOrderRecord(ctx, CreateCashbackOrderRecordRequest{
		UserID: user.ID, Amount: 30, SourceOrderID: src.ID,
		Reference: "cb_42", Notes: "cashback for order 42",
	})
	require.NoError(t, err)
	require.True(t, created)

	require.Equal(t, payment.TypeCashback, order.PaymentType)
	require.Equal(t, payment.OrderTypeBalance, order.OrderType)
	require.Equal(t, OrderStatusCompleted, order.Status)
	require.Equal(t, "cb_42", order.OutTradeNo)
	// amount 与 pay_amount 相等：/orders 用户视图只有「实付」一个金额列，
	// 相等可让列表单行干净显示返现金额
	require.Equal(t, 30.0, order.Amount)
	require.Equal(t, 30.0, order.PayAmount)

	// —— 两个排除性断言，本方案成立的前提 ——
	require.Nil(t, order.PaidAt, "paid_at 必须为 NULL：否则返现会被算成收入、并占用户每日充值额度")
	require.Nil(t, order.ProviderInstanceID, "provider_instance_id 必须为 NULL：否则会进渠道日额度统计")

	require.NotNil(t, order.CompletedAt)
	require.Equal(t, fmt.Sprintf("CB-%d", order.ID), order.RechargeCode)

	// 展示上下文继承自来源订单
	require.Equal(t, "203.0.113.9", order.ClientIP)
	require.Equal(t, "pay.example.com", order.SrcHost)
	require.Equal(t, map[string]any{"currency": "USD"}, order.ProviderSnapshot,
		"只继承币种，不得继承渠道身份")
}

func TestCreateCashbackOrderRecord_Idempotent(t *testing.T) {
	ctx := context.Background()
	svc, client, user := newCashbackTestSvc(t)

	first, created, err := svc.CreateCashbackOrderRecord(ctx, CreateCashbackOrderRecordRequest{
		UserID: user.ID, Amount: 30, Reference: "cb_7",
	})
	require.NoError(t, err)
	require.True(t, created)

	second, created, err := svc.CreateCashbackOrderRecord(ctx, CreateCashbackOrderRecordRequest{
		UserID: user.ID, Amount: 30, Reference: "cb_7",
	})
	require.NoError(t, err, "重复写入不得报错，否则调用方重试不安全")
	require.False(t, created)
	require.Equal(t, first.ID, second.ID)

	n, err := client.PaymentOrder.Query().Where(paymentorder.OutTradeNoEQ("cb_7")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n, "同一 reference 只应有一行")
}

func TestCreateCashbackOrderRecord_MissingSourceOrderStillWrites(t *testing.T) {
	ctx := context.Background()
	svc, _, user := newCashbackTestSvc(t)

	// 来源订单查不到不算失败：返现该照样有记录，只是少了 IP / 币种快照
	order, created, err := svc.CreateCashbackOrderRecord(ctx, CreateCashbackOrderRecordRequest{
		UserID: user.ID, Amount: 5, SourceOrderID: 999999, Reference: "cb_999999",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "cashback", order.ClientIP)
	require.Nil(t, order.PaidAt)
}

func TestCreateCashbackOrderRecord_Validation(t *testing.T) {
	ctx := context.Background()
	svc, _, user := newCashbackTestSvc(t)

	for name, req := range map[string]CreateCashbackOrderRecordRequest{
		"空 reference": {UserID: user.ID, Amount: 30, Reference: "   "},
		"金额为 0":       {UserID: user.ID, Amount: 0, Reference: "cb_a"},
		"金额为负":        {UserID: user.ID, Amount: -1, Reference: "cb_b"},
		"user_id 非法":  {UserID: 0, Amount: 30, Reference: "cb_c"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := svc.CreateCashbackOrderRecord(ctx, req)
			require.Error(t, err)
		})
	}

	// 用户不存在 —— mockUserRepo 忽略 ID，用 getByIDErr 模拟查不到
	svc.userRepo = &mockUserRepo{getByIDErr: errors.New("user not found")}
	_, _, err := svc.CreateCashbackOrderRecord(ctx, CreateCashbackOrderRecordRequest{
		UserID: 987654, Amount: 30, Reference: "cb_nouser",
	})
	require.Error(t, err, "用户不存在应报错")
}

// 收入口径回归：返现流水不得进入 payment_stats 的任何聚合。
// 那些聚合只按 status + paid_at 过滤、不看 payment_type，paid_at 留 NULL 是唯一屏障。
func TestCreateCashbackOrderRecord_ExcludedFromRevenueQueries(t *testing.T) {
	ctx := context.Background()
	svc, client, user := newCashbackTestSvc(t)

	// 一笔真实已付订单
	_, err := client.PaymentOrder.Create().
		SetUserID(user.ID).SetUserEmail(user.Email).SetUserName(user.Username).
		SetAmount(100).SetPayAmount(100).SetFeeRate(0).
		SetRechargeCode("PAY-REAL").SetOutTradeNo("sub2_real").
		SetPaymentType(payment.TypeAlipay).SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now()).SetClientIP("1.1.1.1").SetSrcHost("h").
		SetPaidAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)

	_, _, err = svc.CreateCashbackOrderRecord(ctx, CreateCashbackOrderRecordRequest{
		UserID: user.ID, Amount: 40, Reference: "cb_rev",
	})
	require.NoError(t, err)

	// payment_stats 与 checkDailyLimit 共同的谓词形态：status 命中 + paid_at 非空
	revenueRows, err := client.PaymentOrder.Query().
		Where(
			paymentorder.StatusIn(OrderStatusCompleted, OrderStatusPaid, OrderStatusRecharging),
			paymentorder.PaidAtNotNil(),
		).All(ctx)
	require.NoError(t, err)
	require.Len(t, revenueRows, 1, "收入口径应只命中真实订单")
	require.Equal(t, payment.TypeAlipay, revenueRows[0].PaymentType)

	// 但它在用户的订单列表里可见 —— 这正是本功能的目的
	visible, err := client.PaymentOrder.Query().
		Where(paymentorder.UserIDEQ(user.ID)).All(ctx)
	require.NoError(t, err)
	require.Len(t, visible, 2)
}
