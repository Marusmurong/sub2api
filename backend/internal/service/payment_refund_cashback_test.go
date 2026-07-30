//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// 返现流水没有真实收款，两条退款路径都必须显式拒绝：
// 它是 balance + COMPLETED 且属于该用户，既有的类型/状态/归属检查全都拦不住。

func TestRefund_RejectsCashbackRecord(t *testing.T) {
	ctx := context.Background()
	svc, _, user := newCashbackTestSvc(t)

	record, _, err := svc.CreateCashbackOrderRecord(ctx, CreateCashbackOrderRecordRequest{
		UserID: user.ID, Amount: 30, Reference: "cb_refund",
	})
	require.NoError(t, err)
	require.Equal(t, payment.TypeCashback, record.PaymentType)

	t.Run("用户路径", func(t *testing.T) {
		_, err := svc.validateRefundRequest(ctx, record.ID, user.ID)
		require.Error(t, err)
		require.Contains(t, err.Error(), "cashback")
	})

	t.Run("管理员路径", func(t *testing.T) {
		_, _, err := svc.PrepareRefund(ctx, record.ID, 30, "test", true, true)
		require.Error(t, err)
		require.Contains(t, err.Error(), "cashback")
	})
}
