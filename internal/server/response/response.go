// Package response 提供统一的 JSON 响应辅助函数。
package response

import (
	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/protocol"
)

func JSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}

func Err(c *gin.Context, status int, code, msg string) {
	c.JSON(status, protocol.ErrorResponse{Error: code, Message: msg})
}
