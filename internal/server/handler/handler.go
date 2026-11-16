// Package handler 实现 public、client 和 admin 三组 HTTP 接口。
package handler

import (
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/limiter"
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

// acquireTransfer 获取全局和单用户传输槽。返回 false 时已写入错误响应。
func (h *Handler) acquireTransfer(c *gin.Context, username string) (func(), bool) {
	if h.Limits == nil {
		return func() {}, true
	}
	release, err := h.Limits.AcquireTransfer(c.Request.Context(), username)
	if err != nil {
		if errors.Is(err, limiter.ErrBusy) {
			response.ErrBusy(c, h.RT.Limits.QueueTimeout)
		}
		return nil, false
	}
	return release, true
}

// acquireMetadata 获取元数据槽。返回 false 时已写入错误响应。
func (h *Handler) acquireMetadata(c *gin.Context) (func(), bool) {
	if h.Limits == nil {
		return func() {}, true
	}
	release, err := h.Limits.AcquireMetadata(c.Request.Context())
	if err != nil {
		if errors.Is(err, limiter.ErrBusy) {
			response.ErrBusy(c, h.RT.Limits.QueueTimeout)
		}
		return nil, false
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
