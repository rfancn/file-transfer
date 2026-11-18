package middleware

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/logging"
)

// 审计上下文键，handler 通过它们在请求中补充审计动作和目标。
const (
	ContextAuditAction = "audit_action"
	ContextAuditTarget = "audit_target"
)

// auditActions 把路由映射为语义化审计动作。
var auditActions = map[string]string{
	"POST /v1/users":                                         "user.register",
	"POST /v1/sessions":                                      "user.login",
	"GET /v1/files":                                          "file.list",
	"GET /v1/files/*path":                                    "file.download",
	"PUT /v1/files/*path":                                    "file.upload",
	"POST /v1/uploads":                                       "upload.init",
	"PUT /v1/uploads/:upload_id/parts/:part":                 "upload.part",
	"GET /v1/uploads/:upload_id/parts":                       "upload.parts",
	"PUT /v1/uploads/:upload_id":                             "upload.complete",
	"DELETE /v1/uploads/:upload_id":                          "upload.abort",
	"POST /v1/admin/login":                                   "admin.login",
	"POST /v1/admin/bootstrap":                               "admin.init",
	"GET /v1/admin/users":                                    "admin.user.list",
	"POST /v1/admin/users":                                   "admin.user.add",
	"PUT /v1/admin/users/:username/password":                 "admin.user.reset_password",
	"DELETE /v1/admin/users/:username":                       "admin.user.delete",
	"DELETE /v1/admin/data/:dir":                             "admin.data.clean",
	"GET /v1/admin/policies/:name/sids":                      "admin.policy.sids",
	"GET /v1/admin/policies/:name/statements/:sid/ip-ranges": "admin.policy.ip.list",
	"PUT /v1/admin/policies/:name/statements/:sid/ip-ranges": "admin.policy.ip.set",
}

// auditAction 返回路由对应的语义化动作；未匹配时返回空字符串。
func auditAction(method, fullPath string) string {
	return auditActions[method+" "+fullPath]
}

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
		action := c.GetString(ContextAuditAction)
		if action == "" {
			action = auditAction(c.Request.Method, c.FullPath())
		}
		if action == "" {
			action = c.Request.Method + " " + c.Request.URL.Path
		}
		target := c.GetString(ContextAuditTarget)
		if target == "" {
			target = c.Request.URL.RequestURI()
		}
		logging.Audit(logging.AuditEvent{
			Action:     action,
			Actor:      actor,
			ActorType:  actorType,
			Target:     target,
			Result:     strconv.Itoa(c.Writer.Status()),
			RequestID:  c.GetString("request_id"),
			RemoteIP:   c.ClientIP(),
			ClientUser: c.GetHeader("X-Client-User"),
			ClientHost: c.GetHeader("X-Client-Host"),
		})
	}
}
