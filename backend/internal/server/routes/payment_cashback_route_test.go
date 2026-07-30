package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 返现流水写入端点是给充返插件用的内部接口，必须走 admin 鉴权。
//
// 顺带守住一个注册期风险：静态段 /orders/cashback 与通配 /orders/:id 同层，
// gin 对这类冲突是**注册时 panic**，所以本测试跑通即证明两者共存。
func newCashbackRouteTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()

	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			servermiddleware.AbortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization required")
			return
		}
		servermiddleware.AbortWithError(c, http.StatusForbidden, "FORBIDDEN", "Admin access required")
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	jwtAuth := servermiddleware.JWTAuthMiddleware(func(c *gin.Context) { c.Next() })

	RegisterPaymentRoutes(
		router.Group("/api/v1"),
		handler.NewPaymentHandler(nil, nil),
		&handler.PaymentWebhookHandler{},
		adminhandler.NewPaymentHandler(nil, nil),
		jwtAuth, adminAuth, auditLog, nil, nil,
	)
	return router
}

func TestCashbackOrderRouteRequiresAdmin(t *testing.T) {
	router := newCashbackRouteTestRouter(t)

	for _, tc := range []struct {
		name       string
		auth       string
		wantStatus int
	}{
		{name: "unauthenticated", wantStatus: http.StatusUnauthorized},
		{name: "non-admin", auth: "Bearer user-token", wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/payment/orders/cashback", nil)
			if tc.auth != "" {
				request.Header.Set("Authorization", tc.auth)
			}
			router.ServeHTTP(recorder, request)
			require.Equal(t, tc.wantStatus, recorder.Code)
		})
	}
}

// 新静态路由不得顶掉既有的 /orders/:id 系列。
func TestCashbackOrderRouteCoexistsWithOrderIDRoutes(t *testing.T) {
	router := newCashbackRouteTestRouter(t)

	registered := make(map[string]bool)
	for _, r := range router.Routes() {
		registered[r.Method+" "+r.Path] = true
	}

	for _, want := range []string{
		"POST /api/v1/admin/payment/orders/cashback",
		"GET /api/v1/admin/payment/orders/:id",
		"POST /api/v1/admin/payment/orders/:id/refund",
	} {
		require.True(t, registered[want], "route missing: %s", want)
	}
}
