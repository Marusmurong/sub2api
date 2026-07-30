package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 返现流水：把充返插件发放的每一笔返现落成一条 payment_orders 记录，
// 让客户能在 /orders 上查到（支付方式列显示"充值返现"）。
//
// 这类记录**不是**真实支付。刻意留空的两个字段是本设计的关键：
//
//	paid_at              留 NULL —— 收入看板（payment_stats.go 的 total/daily/
//	                     method/topUsers 四项聚合）与每用户每日充值上限
//	                     （checkDailyLimit）都只按 status + paid_at 过滤，不看
//	                     payment_type。留空即天然排除：返现是费用不是收入，
//	                     也不该占用户的充值额度。
//	provider_instance_id 留 NULL —— 同理排除 payment/load_balancer.go 的渠道
//	                     日额度统计。
//
// out_trade_no 由调用方给出（充返插件用 cb_{订单ID}），依赖表上的 partial unique
// index 作幂等锚点：重复写入返回既有记录而非报错，调用方重试因此安全。
// 前缀刻意不是 sub2_ —— payment_fulfillment.go 会拒绝非 sub2_ 前缀的网关回调
// order id，这让合成记录天然进不了回调链路。

// CreateCashbackOrderRecordRequest 描述一笔要落账的返现流水。
type CreateCashbackOrderRecordRequest struct {
	UserID int64
	// Amount 是返现金额。amount 与 pay_amount 都设为它：/orders 的用户视图只有
	// 「实付」一个金额列，两者相等可让列表单行干净显示返现金额，而不是渲染出
	// 「￥0.00 + 到账金额」两行。
	Amount float64
	// SourceOrderID 是产生这笔返现的充值订单，用于继承 client_ip / src_host /
	// 币种快照，让合成记录与来源订单在展示上一致。
	SourceOrderID int64
	// Reference 是幂等键，直接作为 out_trade_no。
	Reference string
	Notes     string
}

// CreateCashbackOrderRecord 落一条返现流水，幂等。
//
// 已存在同 Reference 的记录时返回既有记录、created=false，不报错。
func (s *PaymentService) CreateCashbackOrderRecord(
	ctx context.Context, req CreateCashbackOrderRecordRequest,
) (order *dbent.PaymentOrder, created bool, err error) {
	reference := strings.TrimSpace(req.Reference)
	if reference == "" {
		return nil, false, infraerrors.BadRequest("INVALID_REFERENCE", "reference must not be empty")
	}
	if req.UserID <= 0 {
		return nil, false, infraerrors.BadRequest("INVALID_USER", "user_id must be positive")
	}
	if req.Amount <= 0 {
		return nil, false, infraerrors.BadRequest("INVALID_AMOUNT", "amount must be positive")
	}

	// 幂等前置检查：命中直接返回，省掉后续的用户/订单查询。
	if existing, lookupErr := s.entClient.PaymentOrder.Query().
		Where(paymentorder.OutTradeNoEQ(reference)).Only(ctx); lookupErr == nil {
		return existing, false, nil
	} else if !dbent.IsNotFound(lookupErr) {
		return nil, false, fmt.Errorf("lookup existing cashback record: %w", lookupErr)
	}

	user, err := s.userRepo.GetByID(ctx, req.UserID)
	if err != nil {
		return nil, false, infraerrors.NotFound("USER_NOT_FOUND", "user not found")
	}

	// 继承来源订单的展示上下文。取不到不算失败：返现该照样有记录，
	// 只是少了 IP / 币种快照。
	clientIP, srcHost := "cashback", "cashback"
	var providerSnapshot map[string]any
	if req.SourceOrderID > 0 {
		if src, srcErr := s.entClient.PaymentOrder.Get(ctx, req.SourceOrderID); srcErr == nil {
			if src.ClientIP != "" {
				clientIP = src.ClientIP
			}
			if src.SrcHost != "" {
				srcHost = src.SrcHost
			}
			if len(src.ProviderSnapshot) > 0 {
				// 只继承币种，不继承渠道身份 —— 渠道字段必须留空以避开日额度统计。
				if cur, ok := src.ProviderSnapshot["currency"]; ok {
					providerSnapshot = map[string]any{"currency": cur}
				}
			}
		}
	}

	now := time.Now()
	b := s.entClient.PaymentOrder.Create().
		SetUserID(req.UserID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(req.Amount).
		SetPayAmount(req.Amount).
		SetFeeRate(0).
		SetRechargeCode("").
		SetOutTradeNo(reference).
		SetPaymentType(payment.TypeCashback).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		// expires_at 是 NOT NULL 且无默认；返现记录没有"待支付"阶段，
		// 设为当下即可，语义上它一创建就已终态。
		SetExpiresAt(now).
		SetCompletedAt(now).
		SetClientIP(clientIP).
		SetSrcHost(srcHost)
	if providerSnapshot != nil {
		b.SetProviderSnapshot(providerSnapshot)
	}
	if notes := strings.TrimSpace(req.Notes); notes != "" {
		b.SetUserNotes(notes)
	}

	order, err = b.Save(ctx)
	if err != nil {
		// 并发下另一个请求可能刚插入同一 reference；唯一索引冲突时回读。
		if existing, lookupErr := s.entClient.PaymentOrder.Query().
			Where(paymentorder.OutTradeNoEQ(reference)).Only(ctx); lookupErr == nil {
			return existing, false, nil
		}
		return nil, false, fmt.Errorf("create cashback order record: %w", err)
	}

	// 与真实下单一致地补 recharge_code，便于运维检索。
	if updated, updErr := s.entClient.PaymentOrder.UpdateOneID(order.ID).
		SetRechargeCode(fmt.Sprintf("CB-%d", order.ID)).Save(ctx); updErr == nil {
		order = updated
	}
	return order, true, nil
}
