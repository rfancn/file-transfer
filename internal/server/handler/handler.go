// Package handler 实现 public、client 和 admin 三组 HTTP 接口。
package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/server/app"
)

const (
	maxJSONBody     = 4 << 10
	userPasswordLen = 8
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
