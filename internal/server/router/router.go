// Package router 按 public、client、admin 三组注册 Gin 路由。
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/logging"
	"github.com/rfancn/file-transfer/internal/server/app"
	"github.com/rfancn/file-transfer/internal/server/handler"
	"github.com/rfancn/file-transfer/internal/server/middleware"
	"github.com/rfancn/file-transfer/internal/server/response"
)

func New(a *app.App) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.Audit(), gin.CustomRecovery(func(c *gin.Context, v any) {
		logging.Error("panic", "request_id", c.GetString("request_id"), "panic", v)
		response.Err(c, http.StatusInternalServerError, "internal_error", "服务内部错误")
	}))
	_ = r.SetTrustedProxies(nil)
	r.HandleMethodNotAllowed = true
	r.NoRoute(func(c *gin.Context) {
		response.Err(c, http.StatusNotFound, "not_found", "接口不存在")
	})
	r.NoMethod(func(c *gin.Context) {
		response.Err(c, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不允许")
	})

	h := handler.New(a)

	// public：自助注册和登录，不需要 token。
	v1 := r.Group("/v1")
	v1.POST("/users", middleware.RequireReady(a), h.Register)
	v1.POST("/sessions", middleware.RequireReady(a), h.Session)

	// client：ft-client 使用的接口，统一校验用户 session token。
	client := v1.Group("", middleware.RequireReady(a), middleware.UserAuth(a))
	client.GET("/files", h.List)
	client.GET("/files/*path", h.Download)
	client.PUT("/files/*path", h.Upload)
	client.POST("/uploads", h.MultipartInit)
	client.PUT("/uploads/:upload_id/parts/:part", h.MultipartPart)
	client.GET("/uploads/:upload_id/parts", h.MultipartParts)
	client.PUT("/uploads/:upload_id", h.MultipartComplete)
	client.DELETE("/uploads/:upload_id", h.MultipartAbort)

	// admin：登录接口公开，其余接口统一校验管理员 token。
	admin := r.Group("/v1/admin")
	admin.POST("/login", h.AdminLogin)
	protected := admin.Group("", middleware.AdminAuth(a))
	protected.POST("/bootstrap", h.AdminInit)
	protected.GET("/users", middleware.RequireReady(a), h.AdminListUsers)
	protected.POST("/users", middleware.RequireReady(a), h.AdminAddUser)
	protected.PUT("/users/:username/password", middleware.RequireReady(a), h.AdminResetPassword)
	protected.DELETE("/users/:username", middleware.RequireReady(a), h.AdminDelete)
	protected.DELETE("/data/:dir", middleware.RequireReady(a), h.AdminClean)
	return r
}
