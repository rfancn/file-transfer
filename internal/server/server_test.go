package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePolicyDir(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy")
	if err := os.Mkdir(policy, 0o755); err != nil {
		t.Fatal(err)
	}

	// 工作目录不存在 policy 时，回退到配置文件所在目录。
	if got := resolvePolicyDir(filepath.Join(dir, "ft-server.toml")); got != policy {
		t.Fatalf("resolvePolicyDir = %q, want %q", got, policy)
	}

	// 找不到时保留默认值，交由策略加载报错。
	if got := resolvePolicyDir(""); got != "policy" {
		t.Fatalf("missing resolvePolicyDir = %q", got)
	}
}
