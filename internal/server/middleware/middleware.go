// Package middleware 提供请求 ID、初始化检查和用户/管理员鉴权中间件。
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/server/app"
	"github.com/rfancn/file-transfer/internal/server/response"
)

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := requestID()
		c.Header("X-Request-Id", rid)
		c.Request.Header.Set("X-Request-Id", rid)
		c.Set("request_id", rid)
		c.Next()
	}
}

// RequireReady 在系统尚未初始化时返回 503。
func RequireReady(a *app.App) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !a.S3Ready() {
			response.Err(c, http.StatusServiceUnavailable, "not_initialized", "系统尚未初始化，请先执行 ft-admin init")
			c.Abort()
			return
		}
		c.Next()
	}
}

// UserAuth 校验 ft-client 的用户 session token。
func UserAuth(a *app.App) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearer(c.Request)
		if token == "" {
			response.Err(c, http.StatusUnauthorized, "token_invalid", "缺少 token")
			c.Abort()
			return
		}
		var claims auth.SessionClaims
		if err := auth.Verify(a.Cfg.Auth.TokenSecret, token, &claims); err != nil {
			response.Err(c, http.StatusUnauthorized, "token_invalid", "token 无效")
			c.Abort()
			return
		}
		if claims.Kind != "session" || auth.Expired(claims.Exp) {
			response.Err(c, http.StatusUnauthorized, "token_expired", "token 已过期，请重新登录")
			c.Abort()
			return
		}
		c.Set("username", claims.Sub)
		c.Next()
	}
}

// AdminAuth 校验 ft-admin 的管理员 token。
func AdminAuth(a *app.App) gin.HandlerFunc {
	return func(c *gin.Context) {
		var claims auth.AdminClaims
		if err := auth.Verify(a.Cfg.Auth.TokenSecret, bearer(c.Request), &claims); err != nil {
			response.Err(c, http.StatusUnauthorized, "token_invalid", "admin token 无效")
			c.Abort()
			return
		}
		if claims.Kind != "admin" || auth.Expired(claims.Exp) {
			response.Err(c, http.StatusUnauthorized, "token_expired", "admin token 已过期，请重新登录")
			c.Abort()
			return
		}
		c.Set("admin", claims.Sub)
		c.Next()
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

func requestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
