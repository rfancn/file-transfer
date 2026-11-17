package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestAdminConfigLoads(t *testing.T) {
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
	p := DefaultServerConfig
	if p != "/etc/file-transfer/ft-server.toml" {
		t.Fatalf("默认配置路径不符: %s", p)
	}
}

func TestLoadServerDefaults(t *testing.T) {
	t.Setenv("FT_S3_ENDPOINT", "")

	cfg := DefaultServer()
	if cfg.S3.HomeBucket != "home" || cfg.S3.SharedBucket != "shared" {
		t.Fatalf("默认桶名不符: %+v", cfg.S3)
	}
	if len(cfg.S3.SharedDirs) != 2 || cfg.S3.SharedDirs[0] != "software" || cfg.S3.SharedDirs[1] != "document" {
		t.Fatalf("默认共享目录不符: %+v", cfg.S3.SharedDirs)
	}
	if cfg.Auth.TokenSecret != DefaultTokenSecret || cfg.Auth.UploadSecret != DefaultUploadSecret {
		t.Fatalf("默认 auth 密钥不符: %+v", cfg.Auth)
	}
}

func TestLoadExplicitMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.toml")
	if _, err := LoadServer(path); err == nil || !strings.Contains(err.Error(), "配置文件不存在") {
		t.Fatalf("LoadServer 显式路径缺失应报错，得到 %v", err)
	}
	if _, err := LoadClient(path); err == nil || !strings.Contains(err.Error(), "配置文件不存在") {
		t.Fatalf("LoadClient 显式路径缺失应报错，得到 %v", err)
	}
	if _, err := LoadAdmin(path); err == nil || !strings.Contains(err.Error(), "配置文件不存在") {
		t.Fatalf("LoadAdmin 显式路径缺失应报错，得到 %v", err)
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

func TestServiceSecretsKeepSpecialCharactersReadable(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := Credentials{AccessKey: "ft-data", SecretKey: "data&secret"}
	admin := Credentials{AccessKey: "ft-admin", SecretKey: "admin&secret"}
	if err := SaveServiceSecrets(data, admin); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "secrets", "s3_credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "data&secret") || !strings.Contains(string(raw), "admin&secret") {
		t.Fatalf("凭据内容不符: %s", raw)
	}
}

func TestServerRetentionDefaults(t *testing.T) {
	r := DefaultServer().Retention
	if r.InboundDays != 3 || r.UploadDays != 3 || r.OutboundDays != 0 || r.IncompleteDays != 3 {
		t.Fatalf("retention defaults = %+v", r)
	}
}

func TestLoadAdminAllowsMissingServerURL(t *testing.T) {
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
}

func TestLoadServerRejectsEmptySecrets(t *testing.T) {

	path := filepath.Join(t.TempDir(), "ft-server.toml")
	content := "[s3]\nendpoint = \"http://127.0.0.1:9000\"\nhome_bucket = \"file-transfer\"\n[auth]\ntoken_secret = \"\"\nupload_secret = \"\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServer(path); err == nil {
		t.Fatal("空的 auth 密钥应导致加载失败")
	}
}

func TestLoadServerRejectsLegacyBucket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ft-server.toml")
	content := "[s3]\nendpoint = \"http://127.0.0.1:9000\"\nbucket = \"file-transfer\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadServer(path)
	if err == nil || !strings.Contains(err.Error(), "s3.home_bucket") {
		t.Fatalf("旧 s3.bucket 应报错，得到 %v", err)
	}
}

func TestLoadServerValidatesBucketsAndDirs(t *testing.T) {
	t.Setenv("FT_S3_ENDPOINT", "")
	base := DefaultServer()
	base.Auth.TokenSecret = "token"
	base.Auth.UploadSecret = "upload"

	cases := []struct {
		name   string
		mutate func(*ServerConfig)
		want   string
	}{
		{"empty home", func(c *ServerConfig) { c.S3.HomeBucket = "" }, "s3.home_bucket"},
		{"empty shared", func(c *ServerConfig) { c.S3.SharedBucket = "" }, "s3.shared_bucket"},
		{"same bucket", func(c *ServerConfig) { c.S3.SharedBucket = c.S3.HomeBucket }, "不能相同"},
		{"empty dirs", func(c *ServerConfig) { c.S3.SharedDirs = nil }, "至少包含一个目录"},
		{"bad bucket char", func(c *ServerConfig) { c.S3.HomeBucket = "bad:bucket" }, "s3.home_bucket"},
		{"uppercase bucket", func(c *ServerConfig) { c.S3.HomeBucket = "MyBucket" }, "s3.home_bucket"},
		{"ip bucket", func(c *ServerConfig) { c.S3.HomeBucket = "192.168.1.1" }, "s3.home_bucket"},
		{"bad dir", func(c *ServerConfig) { c.S3.SharedDirs = []string{"software/v1"} }, "s3.shared_dirs"},
		{"dot dir", func(c *ServerConfig) { c.S3.SharedDirs = []string{".."} }, "s3.shared_dirs"},
		{"duplicate dir", func(c *ServerConfig) { c.S3.SharedDirs = []string{"software", "software"} }, "重复目录"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := base
			c.mutate(&cfg)
			if err := cfg.validateS3(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("期望错误包含 %q，得到 %v", c.want, err)
			}
		})
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
	for _, want := range []string{"[log]", "max_age_days = 30", "max_backups = 30", "max_size_mb = 100", "compress = true", "  listen = ", "  log_file = ", "[retention]", "inbound_days = 3", "[limits]", "max_concurrent = 4", "queue_timeout = \"10s\"", "home_bucket = \"home\"", "shared_bucket = \"shared\"", "shared_dirs = [\"software\", \"document\"]"} {
		if !strings.Contains(content, want) {
			t.Fatalf("生成的配置缺少 %q: %s", want, content)
		}
	}

	t.Setenv("FT_S3_ENDPOINT", "")
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

func TestLimitsDefaults(t *testing.T) {
	cfg := DefaultServer()
	if !cfg.Limits.Enabled {
		t.Fatal("limits.enabled 默认应为 true")
	}
	if cfg.Limits.MaxConcurrent != 4 || cfg.Limits.MaxConcurrentPerUser != 1 {
		t.Fatalf("并发默认值 = %+v", cfg.Limits)
	}

	cfg.Auth.TokenSecret = "x"
	cfg.Auth.UploadSecret = "y"
	cfg.S3.Endpoint = "http://127.0.0.1:9000"
	rt, err := cfg.Runtime("test.toml")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Limits.GlobalReadBPS != 0 || rt.Limits.GlobalWriteBPS != 0 ||
		rt.Limits.PerUserReadBPS != 0 || rt.Limits.PerUserWriteBPS != 0 {
		t.Fatalf("bps 默认值应为 0: %+v", rt.Limits)
	}
	if rt.Limits.QueueTimeout != 10*time.Second {
		t.Fatalf("queue_timeout = %v，期望 10s", rt.Limits.QueueTimeout)
	}
}

func TestLimitsValidation(t *testing.T) {
	base := DefaultServer()
	base.Auth.TokenSecret = "x"
	base.Auth.UploadSecret = "y"
	base.S3.Endpoint = "http://127.0.0.1:9000"

	cases := []struct {
		name   string
		mutate func(*ServerConfig)
	}{
		{"全局并发为 0", func(c *ServerConfig) { c.Limits.MaxConcurrent = 0 }},
		{"单用户并发为 0", func(c *ServerConfig) { c.Limits.MaxConcurrentPerUser = 0 }},
		{"单用户并发大于全局", func(c *ServerConfig) { c.Limits.MaxConcurrentPerUser = 5 }},
		{"读带宽太小", func(c *ServerConfig) { c.Limits.GlobalReadBPS = "32KiB" }},
		{"写带宽为负", func(c *ServerConfig) { c.Limits.GlobalWriteBPS = "-1" }},
		{"排队超时太短", func(c *ServerConfig) { c.Limits.QueueTimeout = "500ms" }},
		{"排队超时太长", func(c *ServerConfig) { c.Limits.QueueTimeout = "2m" }},
		{"排队超时格式错误", func(c *ServerConfig) { c.Limits.QueueTimeout = "abc" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			if _, err := cfg.Runtime("test.toml"); err == nil {
				t.Fatal("期望配置校验失败")
			}
		})
	}
}

func TestExampleServerConfigHasLimits(t *testing.T) {
	data, err := os.ReadFile("../../config/ft-server.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[limits]") {
		t.Fatal("示例配置缺少 [limits] 段")
	}

	cfg, err := LoadServer("../../config/ft-server.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Limits.Enabled {
		t.Fatal("示例配置的 limits.enabled 应为 true")
	}
	if cfg.Limits.MaxConcurrent != 4 {
		t.Fatalf("max_concurrent = %d，期望 4", cfg.Limits.MaxConcurrent)
	}
	if _, err := cfg.Runtime("../../config/ft-server.example.toml"); err != nil {
		t.Fatalf("示例配置无法通过 Runtime 校验: %v", err)
	}
}
