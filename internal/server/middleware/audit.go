package middleware

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/logging"
)

// Audit 把 /v1 请求及结果写入运行日志，事件标记为 event=audit。
func Audit() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if !strings.HasPrefix(c.Request.URL.Path, "/v1/") {
			return
		}
		actor := c.GetString("admin")
		actorType := "admin"
		if actor == "" {
			actor = c.GetString("username")
			actorType = "user"
		}
		if actor == "" {
			actor = "anonymous"
			actorType = "anonymous"
		}
		logging.Audit(logging.AuditEvent{
			Action:     c.Request.Method,
			Actor:      actor,
			ActorType:  actorType,
			Target:     c.Request.URL.RequestURI(),
			Result:     strconv.Itoa(c.Writer.Status()),
			RequestID:  c.GetString("request_id"),
			RemoteIP:   c.ClientIP(),
			ClientUser: c.GetHeader("X-Client-User"),
			ClientHost: c.GetHeader("X-Client-Host"),
		})
	}
}
