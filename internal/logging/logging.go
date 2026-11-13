package logging

import (
	"fmt"
	"log"
	"path/filepath"
	"syscall"

	"github.com/hdget/sdk"
	"github.com/hdget/sdk/common/provider"
)

// 固定写死的日志配置；可变项见 config.LogSection。
const (
	level   = "info"
	appName = "ft-server"
)

// Rotate 日志轮转参数。
type Rotate struct {
	MaxAgeDays int
	MaxBackups int
	MaxSizeMB  int
	Compress   bool
}

// Init 初始化全局 SDK logger，运行日志写入轮转文件。
// 注意：SDK 的固定布局是 <dir>/<文件名去扩展名>/<文件名>。
func Init(logPath string, r Rotate) error {
	// SDK 依赖的 lumberjack 以 0644 创建日志文件，收紧 umask 保证 0600。
	_ = syscall.Umask(0o077)

	content := fmt.Sprintf(`[sdk.log]
level = %q
dir = %q
filename = %q

[sdk.log.rotate]
max_age = %d
max_backup = %d
max_size = %d
compress = %t
`, level, filepath.Dir(logPath), filepath.Base(logPath),
		r.MaxAgeDays, r.MaxBackups, r.MaxSizeMB, r.Compress)
	return sdk.New(appName, sdk.WithConfigContent([]byte(content))).Initialize()
}

// logger 在 SDK 未初始化时返回 nil，避免调用方在初始化前 panic。
func logger() provider.Logger {
	if !sdk.HasInitialized() {
		return nil
	}
	return sdk.Logger()
}

// AuditEvent 是一条审计事件；不得包含密码、token 或密钥。
type AuditEvent struct {
	Action     string
	Actor      string
	ActorType  string
	Target     string
	Result     string
	RequestID  string
	RemoteIP   string
	ClientUser string
	ClientHost string
}

// Audit 把审计事件写入运行日志，事件标记为 event=audit。
func Audit(e AuditEvent) {
	if l := logger(); l != nil {
		fields := []any{
			"event", "audit",
			"action", e.Action,
			"actor", e.Actor,
			"actor_type", e.ActorType,
			"target", e.Target,
			"result", e.Result,
			"request_id", e.RequestID,
			"remote_ip", e.RemoteIP,
		}
		if e.ClientUser != "" {
			fields = append(fields, "client_user", e.ClientUser)
		}
		if e.ClientHost != "" {
			fields = append(fields, "client_host", e.ClientHost)
		}
		l.Info("audit", fields...)
	}
}

func Info(msg string, keyvals ...any) {
	if l := logger(); l != nil {
		l.Info(msg, keyvals...)
	}
}

func Warn(msg string, keyvals ...any) {
	if l := logger(); l != nil {
		l.Warn(msg, keyvals...)
	}
}

func Error(msg string, keyvals ...any) {
	if l := logger(); l != nil {
		l.Error(msg, keyvals...)
	}
}

func Fatal(msg string, keyvals ...any) {
	if l := logger(); l != nil {
		l.Fatal(msg, keyvals...)
		return
	}
	log.Fatal(msg)
}
