package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// GetOrderUSDTInfo returns the USDT receiving details for one of the caller's
// own orders.
// GET /api/v1/payment/orders/:id/usdt
//
// The create-order response already carries this, but the payment page has to
// survive a refresh or being reopened from the order list — a customer who
// loses the address and amount mid-transfer has no way to complete the payment.
func (h *PaymentHandler) GetOrderUSDTInfo(c *gin.Context) {
	subject, ok := requireAuth(c)
	if !ok {
		return
	}
	orderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid order id")
		return
	}

	info, err := h.paymentService.GetUSDTPaymentInfo(c.Request.Context(), orderID, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, info)
}
