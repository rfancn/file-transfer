package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInfoWritesRotatedLogFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ft-server.log")
	if err := Init(logPath, Rotate{
		MaxAgeDays: 1,
		MaxBackups: 2,
		MaxSizeMB:  1,
		Compress:   true,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	Info("upload", "username", "alice", "path", "outbound/a.txt", "status", 200)

	// SDK 的固定布局：<dir>/<文件名去扩展名>/<文件名>
	actual := filepath.Join(dir, "ft-server", "ft-server.log")
	data, err := os.ReadFile(actual)
	if err != nil {
		t.Fatalf("读取日志文件: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		`"message":"upload"`,
		`"username":"alice"`,
		`"path":"outbound/a.txt"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("日志缺少 %s: %s", want, content)
		}
	}

	info, err := os.Stat(actual)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("日志权限 = %o, want 600", perm)
	}
}
