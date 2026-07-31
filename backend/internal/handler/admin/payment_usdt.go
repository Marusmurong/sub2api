package admin

import (
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// adminOperator identifies who performed an action, for the audit trail.
// A manual refund settlement moves real money on somebody's say-so, so the
// "somebody" has to be recorded.
func adminOperator(c *gin.Context) string {
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		return fmt.Sprintf("admin:%d", subject.UserID)
	}
	return "admin"
}

// ListUSDTDeposits returns the on-chain deposit ledger.
// GET /api/v1/admin/payment/usdt/deposits
func (h *PaymentHandler) ListUSDTDeposits(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	deposits, total, err := h.paymentService.ListUSDTDeposits(c.Request.Context(), service.USDTDepositListParams{
		Status:   c.Query("status"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	items := make([]service.USDTDepositView, 0, len(deposits))
	for _, deposit := range deposits {
		items = append(items, service.NewUSDTDepositView(deposit))
	}
	response.Success(c, gin.H{"items": items, "total": total, "network": service.USDTNetworkLabel()})
}

type bindUSDTDepositRequest struct {
	OrderID int64 `json:"order_id" binding:"required"`
	// Force settles a deposit whose amount does not match the order. It exists
	// for real situations (a customer sent the wrong amount, or paid late) but
	// has to be an explicit decision, which is why it is not the default.
	Force bool `json:"force"`
}

// BindUSDTDeposit settles an unmatched deposit against an order by hand.
// POST /api/v1/admin/payment/usdt/deposits/:id/bind
func (h *PaymentHandler) BindUSDTDeposit(c *gin.Context) {
	depositID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid deposit id")
		return
	}
	var req bindUSDTDepositRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	if err := h.paymentService.BindUSDTDepositToOrder(c.Request.Context(), depositID, req.OrderID, req.Force); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"bound": true})
}

type ignoreUSDTDepositRequest struct {
	Notes string `json:"notes"`
}

// IgnoreUSDTDeposit marks a deposit as reviewed and needing no action.
// POST /api/v1/admin/payment/usdt/deposits/:id/ignore
func (h *PaymentHandler) IgnoreUSDTDeposit(c *gin.Context) {
	depositID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid deposit id")
		return
	}
	var req ignoreUSDTDepositRequest
	_ = c.ShouldBindJSON(&req)

	if err := h.paymentService.IgnoreUSDTDeposit(c.Request.Context(), depositID, req.Notes); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ignored": true})
}

// PreviewUSDTRate quotes the current rate for a channel.
// GET /api/v1/admin/payment/usdt/rate?instance_id=7
//
// Exists so an operator can compare our quoted rate against real OTC prices:
// CoinGecko tracks the official USD/CNY reference, which sits a few percent
// below what USDT actually costs, and the premium has to be calibrated for that.
func (h *PaymentHandler) PreviewUSDTRate(c *gin.Context) {
	instanceID, err := strconv.ParseInt(c.Query("instance_id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "instance_id is required")
		return
	}
	preview, err := h.paymentService.PreviewUSDTRate(c.Request.Context(), instanceID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, preview)
}

type settleUSDTRefundRequest struct {
	TxHash     string `json:"tx_hash" binding:"required"`
	AmountUSDT string `json:"amount_usdt"`
}

// SettleUSDTRefund closes a refund after an operator paid the customer on-chain.
// POST /api/v1/admin/payment/orders/:id/usdt/refund-settle
func (h *PaymentHandler) SettleUSDTRefund(c *gin.Context) {
	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid order id")
		return
	}
	var req settleUSDTRefundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "tx_hash is required")
		return
	}

	result, err := h.paymentService.SettleUSDTRefundManually(c.Request.Context(), service.USDTManualRefundSettlement{
		OrderID:    orderID,
		TxHash:     req.TxHash,
		AmountUSDT: req.AmountUSDT,
		Operator:   adminOperator(c),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
