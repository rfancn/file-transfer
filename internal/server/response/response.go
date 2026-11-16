// Package response 提供统一的 JSON 响应辅助函数。
package response

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/protocol"
)

func JSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}

func Err(c *gin.Context, status int, code, msg string) {
	c.JSON(status, protocol.ErrorResponse{Error: code, Message: msg})
}

// ErrBusy 返回 429，并告诉客户端多久后可以重试。
func ErrBusy(c *gin.Context, wait time.Duration) {
	secs := int(wait / time.Second)
	if secs < 1 {
		secs = 1
	}
	c.Header("Retry-After", strconv.Itoa(secs))
	Err(c, http.StatusTooManyRequests, "server_busy", "服务器繁忙，请稍后重试")
}
