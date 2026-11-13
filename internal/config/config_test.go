package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"100MiB", 100 << 20, true},
		{"1GiB", 1 << 30, true},
		{"1.25TiB", int64(1.25 * float64(1<<40)), true},
		{"512MB", 512 * 1000 * 1000, true},
		{"100M", 100 << 20, true},
		{"1G", 1 << 30, true},
		{"bad", 0, false},
		{"-1MiB", 0, false},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if c.ok && err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s: expected error", c.in)
		}
		if c.ok && got != c.want {
			t.Fatalf("%s: got %d want %d", c.in, got, c.want)
		}
	}
}

func TestClientConfigLoads(t *testing.T) {
	t.Setenv("FT_SERVER_URL", "")

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[server]\n  url = \"http://ft-server:8080\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ccfg, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if ccfg.Server.URL != "http://ft-server:8080" {
		t.Fatalf("url = %q", ccfg.Server.URL)
	}
	if ccfg.Upload.WarnFiles != DefaultUploadWarnFiles || ccfg.Upload.WarnSize != DefaultUploadWarnSize {
		t.Fatalf("upload defaults = %+v", ccfg.Upload)
	}
	if ccfg.Download.NameWidth != DefaultDownloadNameWidth {
		t.Fatalf("download name width = %d", ccfg.Download.NameWidth)
	}
}

func TestClientDownloadNameWidthConfig(t *testing.T) {
	t.Setenv("FT_SERVER_URL", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[server]\n  url = \"http://ft-server:8080\"\n[download]\n  name_width = 60\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Download.NameWidth != 60 {
		t.Fatalf("download name width = %d", cfg.Download.NameWidth)
	}
}

func TestClientUploadWarningConfig(t *testing.T) {
	t.Setenv("FT_SERVER_URL", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[server]\n  url = \"http://ft-server:8080\"\n[upload]\n  warn_files = 5\n  warn_size = \"2G\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Upload.WarnFiles != 5 || cfg.Upload.WarnSize != "2G" {
		t.Fatalf("upload = %+v", cfg.Upload)
	}
}

func TestClientUploadWarningInvalidSize(t *testing.T) {
	t.Setenv("FT_SERVER_URL", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[server]\n  url = \"http://ft-server:8080\"\n[upload]\n  warn_size = \"bad\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClient(path); err == nil {
		t.Fatal("expected invalid warn_size error")
	}
}

func TestLoadAdminFromClientTOML(t *testing.T) {
	t.Setenv("FT_SERVER_URL", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[server]\n  url = \"http://ft-server:8080\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAdmin(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.URL != "http://ft-server:8080" {
		t.Fatalf("url = %q", cfg.Server.URL)
	}
}

func TestAdminEnvConfigLoads(t *testing.T) {
	t.Setenv("RUSTFS_ACCESS_KEY", "")
	t.Setenv("RUSTFS_SECRET_KEY", "")
	t.Setenv("FT_SERVER_URL", "")

	path := filepath.Join(t.TempDir(), "admin.toml")
	if err := os.WriteFile(path, []byte("[server]\n  url = \"http://ft-server:8080\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	acfg, err := LoadAdmin(path)
	if err != nil {
		t.Fatal(err)
	}
	if acfg.Server.URL != "http://ft-server:8080" {
		t.Fatalf("url = %q", acfg.Server.URL)
	}
	if acfg.S3.AccessKey != "" || acfg.S3.SecretKey != "" {
		t.Fatal("不应从 .conf 读取 S3 凭据")
	}
}

func TestDefaultServerConfig(t *testing.T) {
	p := DefaultServerConfig()
	if filepath.Base(p) != "ft-server.toml" {
		t.Fatalf("默认配置文件名不符: %s", p)
	}
	if !filepath.IsAbs(p) {
		t.Fatalf("默认配置应为可执行文件同目录的绝对路径: %s", p)
	}
}

func TestLoadServerDefaults(t *testing.T) {
	t.Setenv("FT_S3_ENDPOINT", "")
	t.Setenv("FT_BUCKET", "")
	t.Setenv("FT_TOKEN_SECRET", "")
	t.Setenv("FT_UPLOAD_SECRET", "")

	cfg, err := LoadServer(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3.Endpoint == "" || cfg.S3.Bucket == "" {
		t.Fatalf("默认 S3 配置不完整: %+v", cfg.S3)
	}
	if cfg.Auth.TokenSecret != DefaultTokenSecret || cfg.Auth.UploadSecret != DefaultUploadSecret {
		t.Fatalf("默认 auth 密钥不符: %+v", cfg.Auth)
	}
}

func TestEnsureAuthSecrets(t *testing.T) {
	cfg := DefaultServer()
	changed, err := cfg.EnsureAuthSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("默认密钥应被替换")
	}
	if cfg.Auth.TokenSecret == DefaultTokenSecret || cfg.Auth.UploadSecret == DefaultUploadSecret {
		t.Fatal("不应保留公开默认密钥")
	}
	if cfg.Auth.TokenSecret == cfg.Auth.UploadSecret {
		t.Fatal("token 与 upload 密钥必须不同")
	}
	if len(cfg.Auth.TokenSecret) < 32 || len(cfg.Auth.UploadSecret) < 32 {
		t.Fatalf("随机密钥长度不足: %d/%d", len(cfg.Auth.TokenSecret), len(cfg.Auth.UploadSecret))
	}
	changed, err = cfg.EnsureAuthSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("已生成的密钥不应再次变化")
	}

	custom := DefaultServer()
	custom.Auth.TokenSecret = "custom-token"
	custom.Auth.UploadSecret = "custom-upload"
	changed, err = custom.EnsureAuthSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("自定义密钥不应被替换")
	}
}

func TestServiceSecretsRoundTrip(t *testing.T) {
	t.Setenv("FT_TOKEN_SECRET", "")
	t.Setenv("FT_UPLOAD_SECRET", "")

	cfg := DefaultServer()
	data := Credentials{AccessKey: "ft-data", SecretKey: "data-secret"}
	admin := Credentials{AccessKey: "ft-admin", SecretKey: "admin-secret"}

	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "ft-server.toml")
	if err := SaveServerConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := SaveServiceSecrets(data, admin); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "data-secret") || strings.Contains(string(raw), "admin-secret") {
		t.Fatal("ft-server.toml 不应包含服务凭据密码")
	}

	secretFile := filepath.Join(dir, "secrets", "s3_credentials.json")
	fi, err := os.Stat(secretFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("s3_credentials.json 权限 = %o, want 600", perm)
	}
	raw, err = os.ReadFile(secretFile)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Data  Credentials `json:"ft-data"`
		Admin Credentials `json:"ft-admin"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Data != data || saved.Admin != admin {
		t.Fatalf("保存的凭据不符: %+v %+v", saved.Data, saved.Admin)
	}

	loadedData, loadedAdmin, err := LoadServiceSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if loadedData != data || loadedAdmin != admin {
		t.Fatalf("服务凭据未正确加载: %+v %+v", loadedData, loadedAdmin)
	}
}

func TestServerRetentionDefaults(t *testing.T) {
	r := DefaultServer().Retention
	if r.InboundDays != 3 || r.UploadDays != 3 || r.OutboundDays != 0 || r.IncompleteDays != 3 {
		t.Fatalf("retention defaults = %+v", r)
	}
}

func TestLoadAdminAllowsMissingServerURL(t *testing.T) {
	t.Setenv("FT_SERVER_URL", "")

	path := filepath.Join(t.TempDir(), "admin.conf")
	if err := os.WriteFile(path, []byte("# empty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAdmin(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.URL != "" {
		t.Fatalf("url = %q, want empty", cfg.Server.URL)
	}

	t.Setenv("FT_SERVER_URL", "http://127.0.0.1:8080")
	cfg, err = LoadAdmin(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.URL != "http://127.0.0.1:8080" {
		t.Fatalf("url = %q", cfg.Server.URL)
	}
}

func TestLoadServerRejectsEmptySecrets(t *testing.T) {
	t.Setenv("FT_TOKEN_SECRET", "")
	t.Setenv("FT_UPLOAD_SECRET", "")

	path := filepath.Join(t.TempDir(), "ft-server.toml")
	content := "[s3]\nendpoint = \"http://127.0.0.1:9000\"\nbucket = \"file-transfer\"\n[auth]\ntoken_secret = \"\"\nupload_secret = \"\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServer(path); err == nil {
		t.Fatal("空的 auth 密钥应导致加载失败")
	}
}

func TestLogConfigDefaults(t *testing.T) {
	cfg := DefaultServer()
	if cfg.Log.MaxAgeDays != 30 || cfg.Log.MaxBackups != 30 || cfg.Log.MaxSizeMB != 100 || !cfg.Log.Compress {
		t.Fatalf("默认日志配置不符: %+v", cfg.Log)
	}

	path := filepath.Join(t.TempDir(), "ft-server.toml")
	if err := SaveServerConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("配置权限 = %o, want 600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"[log]", "max_age_days = 30", "max_backups = 30", "max_size_mb = 100", "compress = true", "  listen = ", "  log_file = ", "[retention]", "inbound_days = 3"} {
		if !strings.Contains(content, want) {
			t.Fatalf("生成的配置缺少 %q: %s", want, content)
		}
	}

	t.Setenv("FT_TOKEN_SECRET", "")
	t.Setenv("FT_UPLOAD_SECRET", "")
	t.Setenv("FT_S3_ENDPOINT", "")
	t.Setenv("FT_BUCKET", "")
	loaded, err := LoadServer(path)
	if err != nil {
		t.Fatalf("生成的 TOML 无法解析: %v", err)
	}
	if loaded.Log != cfg.Log {
		t.Fatalf("往返后日志配置不一致: %+v != %+v", loaded.Log, cfg.Log)
	}
}

func TestPartSizeDefaults(t *testing.T) {
	cfg := DefaultServer()
	cfg.Auth.TokenSecret = "x"
	cfg.Auth.UploadSecret = "y"
	cfg.S3.Endpoint = "http://127.0.0.1:9000"
	rt, err := cfg.Runtime("test.toml")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		size int64
		want int64
	}{
		{1 << 20, 0},
		{100 << 20, 16 << 20},
		{5 << 30, 32 << 20},
		{50 << 30, 64 << 20},
		{625 << 30, 128 << 20},
		{2 << 40, 512 << 20},
	}
	for _, c := range cases {
		got, err := rt.PartSize(c.size)
		if err != nil {
			t.Fatalf("size %d: %v", c.size, err)
		}
		if got != c.want {
			t.Fatalf("size %d: got %d want %d", c.size, got, c.want)
		}
	}
}
