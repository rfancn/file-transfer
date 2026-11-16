// Package handler 实现 public、client 和 admin 三组 HTTP 接口。
package handler

import (
	"errors"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/limiter"
	"github.com/rfancn/file-transfer/internal/logging"
	"github.com/rfancn/file-transfer/internal/server/app"
	"github.com/rfancn/file-transfer/internal/server/response"
)

const (
	maxJSONBody     = 4 << 10
	userPasswordLen = 8
	copyBufferSize  = 1 << 20
)

type Handler struct {
	*app.App
}

func New(a *app.App) *Handler {
	return &Handler{App: a}
}

func currentUser(c *gin.Context) string {
	return c.GetString("username")
}

func wildcardPath(c *gin.Context) string {
	return strings.TrimPrefix(c.Param("path"), "/")
}

// acquireTransfer 获取全局和单用户传输槽。返回 false 时调用方不得再写响应：
// ErrBusy 已写 429，请求取消等错误未写响应。
func (h *Handler) acquireTransfer(c *gin.Context, username string) (func(), bool) {
	if h.Limits == nil {
		return func() {}, true
	}
	start := time.Now()
	release, err := h.Limits.AcquireTransfer(c.Request.Context(), username)
	waitMs := time.Since(start).Milliseconds()
	if err != nil {
		if errors.Is(err, limiter.ErrBusy) {
			logging.Warn("limit busy",
				"request_id", c.GetString("request_id"),
				"username", username,
				"class", "transfer",
				"wait_ms", waitMs,
				"reason", "queue_timeout")
			response.ErrBusy(c, h.RT.Limits.QueueTimeout)
		} else {
			logging.Info("limit canceled",
				"request_id", c.GetString("request_id"),
				"username", username,
				"class", "transfer",
				"wait_ms", waitMs,
				"reason", "context_canceled")
		}
		return nil, false
	}
	if waitMs >= 1000 {
		logging.Info("limit wait",
			"request_id", c.GetString("request_id"),
			"username", username,
			"class", "transfer",
			"wait_ms", waitMs)
	}
	return release, true
}

// acquireMetadata 获取元数据槽。返回 false 时调用方不得再写响应：
// ErrBusy 已写 429，请求取消等错误未写响应。
func (h *Handler) acquireMetadata(c *gin.Context) (func(), bool) {
	if h.Limits == nil {
		return func() {}, true
	}
	username := currentUser(c)
	start := time.Now()
	release, err := h.Limits.AcquireMetadata(c.Request.Context())
	waitMs := time.Since(start).Milliseconds()
	if err != nil {
		if errors.Is(err, limiter.ErrBusy) {
			logging.Warn("limit busy",
				"request_id", c.GetString("request_id"),
				"username", username,
				"class", "metadata",
				"wait_ms", waitMs,
				"reason", "queue_timeout")
			response.ErrBusy(c, h.RT.Limits.QueueTimeout)
		} else {
			logging.Info("limit canceled",
				"request_id", c.GetString("request_id"),
				"username", username,
				"class", "metadata",
				"wait_ms", waitMs,
				"reason", "context_canceled")
		}
		return nil, false
	}
	if waitMs >= 1000 {
		logging.Info("limit wait",
			"request_id", c.GetString("request_id"),
			"username", username,
			"class", "metadata",
			"wait_ms", waitMs)
	}
	return release, true
}

func (h *Handler) wrapUpload(c *gin.Context, r io.Reader, username string) io.Reader {
	if h.Limits == nil {
		return r
	}
	return h.Limits.WrapUpload(c.Request.Context(), r, username)
}

func (h *Handler) wrapDownload(c *gin.Context, r io.Reader, username string) io.Reader {
	if h.Limits == nil {
		return r
	}
	return h.Limits.WrapDownload(c.Request.Context(), r, username)
}
