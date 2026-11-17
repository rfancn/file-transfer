package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
)

var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

const (
	DefaultClientConfig = "/etc/file-transfer/ft-client.toml"
	DefaultAdminConfig  = DefaultClientConfig
	DefaultServerConfig = "/etc/file-transfer/ft-server.toml"

	// 示例配置中的默认值；启动时若仍为该值会输出 WARN。
	DefaultTokenSecret  = "ft-server-default-token-secret-change-me"
	DefaultUploadSecret = "ft-server-default-upload-secret-change-me"
)

// Credentials 是一组 RustFS S3 凭据。
type Credentials struct {
	AccessKey string `toml:"access_key" json:"access_key"`
	SecretKey string `toml:"secret_key" json:"secret_key"`
}

type S3Section struct {
	Endpoint     string   `toml:"endpoint"`
	HomeBucket   string   `toml:"home_bucket"`
	SharedBucket string   `toml:"shared_bucket"`
	SharedDirs   []string `toml:"shared_dirs"`
	Region       string   `toml:"region"`
}

type ServerSection struct {
	Listen  string `toml:"listen"`
	LogFile string `toml:"log_file"`
}

type AuthSection struct {
	TokenSecret  string `toml:"token_secret"`
	TokenTTL     string `toml:"token_ttl"`
	UploadSecret string `toml:"upload_secret"`
	MultipartTTL string `toml:"multipart_ttl"`
}

type MultipartTier struct {
	Max  string `toml:"max"`
	Part string `toml:"part"`
}

type MultipartSection struct {
	DirectMax string          `toml:"direct_max"`
	MaxPart   string          `toml:"max_part"`
	Tiers     []MultipartTier `toml:"tiers"`
}

// LimitsSection 是应用层 I/O 限速配置。
type LimitsSection struct {
	Enabled              bool   `toml:"enabled"`
	MaxConcurrent        int    `toml:"max_concurrent"`
	MaxConcurrentPerUser int    `toml:"max_concurrent_per_user"`
	GlobalReadBPS        string `toml:"global_read_bps"`
	GlobalWriteBPS       string `toml:"global_write_bps"`
	PerUserReadBPS       string `toml:"per_user_read_bps"`
	PerUserWriteBPS      string `toml:"per_user_write_bps"`
	QueueTimeout         string `toml:"queue_timeout"`
}

type LogSection struct {
	MaxAgeDays int  `toml:"max_age_days"`
	MaxBackups int  `toml:"max_backups"`
	MaxSizeMB  int  `toml:"max_size_mb"`
	Compress   bool `toml:"compress"`
}

type ServerConfig struct {
	Server    ServerSection    `toml:"server"`
	S3        S3Section        `toml:"s3"`
	Auth      AuthSection      `toml:"auth"`
	Multipart MultipartSection `toml:"multipart"`
	Limits    LimitsSection    `toml:"limits"`
	Retention RetentionSection `toml:"retention"`
	Log       LogSection       `toml:"log"`
}

type ClientSection struct {
	URL string `toml:"url"`
	// CACert 指定用于校验服务端证书的 PEM 文件；为空时使用系统根证书库。
	CACert string `toml:"ca_cert"`
	// InsecureSkipVerify 跳过服务端证书校验，仅用于自签名证书的调试场景。
	InsecureSkipVerify bool `toml:"insecure_skip_verify"`
}

// UploadSection 控制上传前的数据量提示阈值；0 表示关闭对应提示。
type UploadSection struct {
	WarnFiles int    `toml:"warn_files"`
	WarnSize  string `toml:"warn_size"`
}

// DownloadSection 控制下载确认中的展示选项。
type DownloadSection struct {
	// NameWidth 是文件名显示的终端列数，默认 80。
	NameWidth int `toml:"name_width"`
}

type ClientConfig struct {
	Server   ClientSection   `toml:"server"`
	Upload   UploadSection   `toml:"upload"`
	Download DownloadSection `toml:"download"`
}

const (
	DefaultUploadWarnFiles   = 20
	DefaultUploadWarnSize    = "100MiB"
	DefaultDownloadNameWidth = 80
)

// DefaultClient 返回带默认上传提示阈值的客户端配置。
func DefaultClient() ClientConfig {
	return ClientConfig{
		Upload: UploadSection{
			WarnFiles: DefaultUploadWarnFiles,
			WarnSize:  DefaultUploadWarnSize,
		},
		Download: DownloadSection{
			NameWidth: DefaultDownloadNameWidth,
		},
	}
}

// RetentionSection 是清理任务的默认保留天数；0 表示不自动清理。
type RetentionSection struct {
	InboundDays    int `toml:"inbound_days"`
	OutboundDays   int `toml:"outbound_days"`
	UploadDays     int `toml:"upload_days"`
	IncompleteDays int `toml:"incomplete_days"`
}

// AdminS3Section 只保存 ft-admin init 需要的 RustFS root 凭据。
// 管理端不直连 RustFS，endpoint/bucket 由 ft-server 负责。
type AdminS3Section struct {
	AccessKey string `toml:"-"`
	SecretKey string `toml:"-"`
}

type AdminConfig struct {
	S3     AdminS3Section `toml:"s3"`
	Server ClientSection  `toml:"server"`
}

type Runtime struct {
	TokenTTL     time.Duration
	MultipartTTL time.Duration
	DirectMax    int64
	MaxPart      int64
	Tiers        []Tier
	Limits       Limits
	ConfigPath   string
	Config       ServerConfig
}

// Limits 是解析后的应用层限速配置。
type Limits struct {
	Enabled              bool
	MaxConcurrent        int
	MaxConcurrentPerUser int
	GlobalReadBPS        int64
	GlobalWriteBPS       int64
	PerUserReadBPS       int64
	PerUserWriteBPS      int64
	QueueTimeout         time.Duration
}

type Tier struct {
	Max  int64
	Part int64
}

func DefaultServer() ServerConfig {
	return ServerConfig{
		Server: ServerSection{
			Listen:  ":8080",
			LogFile: "/var/log/file-transfer/ft-server.log",
		},
		S3: S3Section{
			Endpoint:     "http://127.0.0.1:9000",
			HomeBucket:   "home",
			SharedBucket: "shared",
			SharedDirs:   []string{"software", "document"},
			Region:       "us-east-1",
		},
		Auth: AuthSection{
			TokenSecret:  DefaultTokenSecret,
			UploadSecret: DefaultUploadSecret,
			TokenTTL:     "24h",
			MultipartTTL: "168h",
		},
		Multipart: MultipartSection{
			DirectMax: "100MiB",
			MaxPart:   "512MiB",
			Tiers: []MultipartTier{
				{Max: "5GiB", Part: "16MiB"},
				{Max: "50GiB", Part: "32MiB"},
				{Max: "625GiB", Part: "64MiB"},
				{Max: "1.25TiB", Part: "128MiB"},
			},
		},
		Limits: LimitsSection{
			Enabled:              true,
			MaxConcurrent:        4,
			MaxConcurrentPerUser: 1,
			GlobalReadBPS:        "0",
			GlobalWriteBPS:       "0",
			PerUserReadBPS:       "0",
			PerUserWriteBPS:      "0",
			QueueTimeout:         "10s",
		},
		Retention: RetentionSection{
			InboundDays: 3, OutboundDays: 0, UploadDays: 3, IncompleteDays: 3,
		},
		Log: LogSection{
			MaxAgeDays: 30,
			MaxBackups: 30,
			MaxSizeMB:  100,
			Compress:   true,
		},
	}
}

// EnsureAuthSecrets 把空的或公开默认值的 token/upload 密钥替换为随机密钥。
// 返回配置是否发生变化，调用方应把变化后的配置写回文件。
func (c *ServerConfig) EnsureAuthSecrets() (bool, error) {
	changed := false
	if c.Auth.TokenSecret == "" || c.Auth.TokenSecret == DefaultTokenSecret {
		secret, err := randomSecret()
		if err != nil {
			return false, err
		}
		c.Auth.TokenSecret = secret
		changed = true
	}
	if c.Auth.UploadSecret == "" || c.Auth.UploadSecret == DefaultUploadSecret {
		secret, err := randomSecret()
		if err != nil {
			return false, err
		}
		c.Auth.UploadSecret = secret
		changed = true
	}
	return changed, nil
}

func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机密钥: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func DefaultAdmin() AdminConfig {
	return AdminConfig{
		Server: ClientSection{},
	}
}

func LoadServer(path string) (ServerConfig, error) {
	explicit := path != ""
	if path == "" {
		path = DefaultServerConfig
	}
	cfg := DefaultServer()
	if _, err := os.Stat(path); err == nil {
		md, err := toml.DecodeFile(path, &cfg)
		if err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
		if md.IsDefined("s3", "bucket") {
			return cfg, errors.New("s3.bucket 已移除，请改为 s3.home_bucket；升级时可将原值填入 home_bucket")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	} else if explicit {
		return cfg, fmt.Errorf("配置文件不存在: %s", path)
	}
	if v := os.Getenv("FT_S3_ENDPOINT"); v != "" {
		cfg.S3.Endpoint = v
	}
	if err := cfg.validateS3(); err != nil {
		return cfg, err
	}
	if cfg.Auth.TokenSecret == "" {
		return cfg, errors.New("auth.token_secret 不能为空")
	}
	if cfg.Auth.UploadSecret == "" {
		return cfg, errors.New("auth.upload_secret 不能为空")
	}
	if cfg.Log.MaxAgeDays < 0 {
		return cfg, errors.New("log.max_age_days 不能为负")
	}
	if cfg.Log.MaxBackups < 0 {
		return cfg, errors.New("log.max_backups 不能为负")
	}
	if cfg.Log.MaxSizeMB <= 0 {
		return cfg, errors.New("log.max_size_mb 必须大于 0")
	}
	if cfg.Retention.InboundDays < 0 || cfg.Retention.OutboundDays < 0 || cfg.Retention.UploadDays < 0 || cfg.Retention.IncompleteDays < 0 {
		return cfg, errors.New("retention 保留天数不能为负")
	}
	return cfg, nil
}

func validateBucketName(field, name string) error {
	if name == "" {
		return fmt.Errorf("%s 不能为空", field)
	}
	if !bucketNameRe.MatchString(name) {
		return fmt.Errorf("%s 必须为 3-63 位小写字母、数字、点或连字符，且首尾为字母或数字", field)
	}
	if strings.Contains(name, "..") || strings.Contains(name, ".-") || strings.Contains(name, "-.") {
		return fmt.Errorf("%s 不能包含相邻的点或连字符", field)
	}
	if net.ParseIP(name) != nil {
		return fmt.Errorf("%s 不能使用 IP 地址格式", field)
	}
	return nil
}

func validateSharedDir(dir string) error {
	if dir == "" {
		return errors.New("s3.shared_dirs 不能包含空目录名")
	}
	if dir == "." || dir == ".." {
		return fmt.Errorf("s3.shared_dirs 不能包含目录 %q", dir)
	}
	if err := validateNameChars(dir); err != nil {
		return fmt.Errorf("s3.shared_dirs 目录 %q %w", dir, err)
	}
	return nil
}

func validateNameChars(name string) error {
	for _, r := range name {
		if unicode.IsSpace(r) || r < 0x20 || r == 0x7f || r == '/' || r == '\\' || r == ':' || r == '\'' || r == '"' || r == '*' || r == '?' || r == '{' || r == '}' {
			return errors.New("不能包含空白、控制字符、斜杠、冒号、引号、通配符或花括号")
		}
	}
	return nil
}

func (c ServerConfig) validateS3() error {
	if c.S3.Endpoint == "" {
		return errors.New("s3.endpoint 不能为空")
	}
	if err := validateBucketName("s3.home_bucket", c.S3.HomeBucket); err != nil {
		return err
	}
	if err := validateBucketName("s3.shared_bucket", c.S3.SharedBucket); err != nil {
		return err
	}
	if c.S3.HomeBucket == c.S3.SharedBucket {
		return errors.New("s3.home_bucket 与 s3.shared_bucket 不能相同")
	}
	if len(c.S3.SharedDirs) == 0 {
		return errors.New("s3.shared_dirs 至少包含一个目录")
	}
	seenDirs := make(map[string]struct{}, len(c.S3.SharedDirs))
	for _, dir := range c.S3.SharedDirs {
		if err := validateSharedDir(dir); err != nil {
			return err
		}
		if _, ok := seenDirs[dir]; ok {
			return fmt.Errorf("s3.shared_dirs 包含重复目录: %s", dir)
		}
		seenDirs[dir] = struct{}{}
	}
	return nil
}

func (c ServerConfig) Runtime(path string) (Runtime, error) {
	rt := Runtime{ConfigPath: path, Config: c}
	var err error
	rt.Limits = Limits{
		Enabled:              c.Limits.Enabled,
		MaxConcurrent:        c.Limits.MaxConcurrent,
		MaxConcurrentPerUser: c.Limits.MaxConcurrentPerUser,
	}
	if rt.Limits.MaxConcurrent < 1 {
		return rt, errors.New("limits.max_concurrent 必须大于 0")
	}
	if rt.Limits.MaxConcurrentPerUser < 1 || rt.Limits.MaxConcurrentPerUser > rt.Limits.MaxConcurrent {
		return rt, errors.New("limits.max_concurrent_per_user 必须在 1 和 max_concurrent 之间")
	}
	rt.Limits.GlobalReadBPS, err = parseBPS(c.Limits.GlobalReadBPS, "limits.global_read_bps")
	if err != nil {
		return rt, err
	}
	rt.Limits.GlobalWriteBPS, err = parseBPS(c.Limits.GlobalWriteBPS, "limits.global_write_bps")
	if err != nil {
		return rt, err
	}
	rt.Limits.PerUserReadBPS, err = parseBPS(c.Limits.PerUserReadBPS, "limits.per_user_read_bps")
	if err != nil {
		return rt, err
	}
	rt.Limits.PerUserWriteBPS, err = parseBPS(c.Limits.PerUserWriteBPS, "limits.per_user_write_bps")
	if err != nil {
		return rt, err
	}
	rt.Limits.QueueTimeout, err = time.ParseDuration(c.Limits.QueueTimeout)
	if err != nil {
		return rt, fmt.Errorf("limits.queue_timeout: %w", err)
	}
	if rt.Limits.QueueTimeout < time.Second || rt.Limits.QueueTimeout > time.Minute {
		return rt, errors.New("limits.queue_timeout 必须在 1s～60s 之间")
	}
	rt.TokenTTL, err = time.ParseDuration(c.Auth.TokenTTL)
	if err != nil {
		return rt, fmt.Errorf("auth.token_ttl: %w", err)
	}
	rt.MultipartTTL, err = time.ParseDuration(c.Auth.MultipartTTL)
	if err != nil {
		return rt, fmt.Errorf("auth.multipart_ttl: %w", err)
	}
	rt.DirectMax, err = ParseSize(c.Multipart.DirectMax)
	if err != nil {
		return rt, fmt.Errorf("multipart.direct_max: %w", err)
	}
	rt.MaxPart, err = ParseSize(c.Multipart.MaxPart)
	if err != nil {
		return rt, fmt.Errorf("multipart.max_part: %w", err)
	}
	for i, t := range c.Multipart.Tiers {
		max, err := ParseSize(t.Max)
		if err != nil {
			return rt, fmt.Errorf("multipart.tiers[%d].max: %w", i, err)
		}
		part, err := ParseSize(t.Part)
		if err != nil {
			return rt, fmt.Errorf("multipart.tiers[%d].part: %w", i, err)
		}
		rt.Tiers = append(rt.Tiers, Tier{Max: max, Part: part})
	}
	if rt.MaxPart < 5<<20 {
		return rt, errors.New("multipart.max_part 不能小于 5MiB")
	}
	return rt, nil
}

func (rt Runtime) PartSize(size int64) (int64, error) {
	if size < 0 {
		return 0, errors.New("文件大小无效")
	}
	if size < rt.DirectMax {
		return 0, nil
	}
	part := rt.MaxPart
	for _, t := range rt.Tiers {
		if size < t.Max {
			part = t.Part
			break
		}
	}
	if part < 5<<20 {
		part = 5 << 20
	}
	if part > 5<<30 {
		part = 5 << 30
	}
	if size > 5<<40 {
		return 0, errors.New("文件超过 5TiB 上限")
	}
	// 保证不超过 10000 个分片。
	if need := (size + 9999) / 10000; need > part {
		part = ((need + (1 << 20) - 1) / (1 << 20)) << 20
	}
	if part > 5<<30 {
		part = 5 << 30
	}
	return part, nil
}

func parseBPS(s, field string) (int64, error) {
	v, err := ParseSize(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	if v != 0 && v < 64<<10 {
		return 0, fmt.Errorf("%s 必须为 0 或至少 64KiB", field)
	}
	return v, nil
}

func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("空大小")
	}
	units := []struct {
		suffix string
		mult   int64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"TB", 1000 * 1000 * 1000 * 1000}, {"GB", 1000 * 1000 * 1000}, {"MB", 1000 * 1000}, {"KB", 1000},
		{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			num := strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, fmt.Errorf("无效大小 %q", s)
			}
			if f < 0 {
				return 0, fmt.Errorf("大小不能为负: %q", s)
			}
			return int64(f * float64(u.mult)), nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("无效大小 %q", s)
	}
	return int64(f), nil
}

func LoadClient(path string) (ClientConfig, error) {
	explicit := path != ""
	if path == "" {
		path = DefaultClientConfig
	}
	cfg := DefaultClient()
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	} else if explicit {
		return cfg, fmt.Errorf("配置文件不存在: %s", path)
	}
	if cfg.Upload.WarnFiles < 0 {
		return cfg, errors.New("upload.warn_files 不能为负")
	}
	if _, err := ParseSize(cfg.Upload.WarnSize); err != nil {
		return cfg, fmt.Errorf("upload.warn_size: %w", err)
	}
	if cfg.Download.NameWidth <= 0 {
		cfg.Download.NameWidth = DefaultDownloadNameWidth
	}
	return cfg, nil
}

func LoadAdmin(path string) (AdminConfig, error) {
	explicit := path != ""
	if path == "" {
		path = DefaultAdminConfig
	}
	cfg := DefaultAdmin()
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	} else if explicit {
		return cfg, fmt.Errorf("配置文件不存在: %s", path)
	}
	return cfg, nil
}

func formatStringList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = strconv.Quote(item)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// WriteServerConfig 写入带默认值的 ft-server.toml；已存在时不覆盖。
func WriteServerConfig(path string, cfg ServerConfig) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	return true, SaveServerConfig(path, cfg)
}

// SaveServerConfig 强制写入 ft-server.toml。
func SaveServerConfig(path string, cfg ServerConfig) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# ft-server.toml\n")
	fmt.Fprintf(&b, "[server]\n  listen = %q\n  log_file = %q\n\n", cfg.Server.Listen, cfg.Server.LogFile)
	fmt.Fprintf(&b, "[s3]\n  endpoint = %q\n  home_bucket = %q\n  shared_bucket = %q\n  shared_dirs = %s\n  region = %q\n  # 服务身份凭据见同目录 secrets/，不在本文件中配置\n\n", cfg.S3.Endpoint, cfg.S3.HomeBucket, cfg.S3.SharedBucket, formatStringList(cfg.S3.SharedDirs), cfg.S3.Region)
	fmt.Fprintf(&b, "[auth]\n  token_secret = %q\n  token_ttl = %q\n  upload_secret = %q\n  multipart_ttl = %q\n\n", cfg.Auth.TokenSecret, cfg.Auth.TokenTTL, cfg.Auth.UploadSecret, cfg.Auth.MultipartTTL)
	fmt.Fprintf(&b, "[log]\n  max_age_days = %d\n  max_backups = %d\n  max_size_mb = %d\n  compress = %t\n\n", cfg.Log.MaxAgeDays, cfg.Log.MaxBackups, cfg.Log.MaxSizeMB, cfg.Log.Compress)
	fmt.Fprintf(&b, "[retention]\n  inbound_days = %d\n  outbound_days = %d\n  upload_days = %d\n  incomplete_days = %d\n\n", cfg.Retention.InboundDays, cfg.Retention.OutboundDays, cfg.Retention.UploadDays, cfg.Retention.IncompleteDays)
	fmt.Fprintf(&b, "[multipart]\n  direct_max = %q\n  max_part = %q\n", cfg.Multipart.DirectMax, cfg.Multipart.MaxPart)
	for _, t := range cfg.Multipart.Tiers {
		fmt.Fprintf(&b, "\n  [[multipart.tiers]]\n    max = %q\n    part = %q\n", t.Max, t.Part)
	}
	fmt.Fprintf(&b, "\n[limits]\n  enabled = %t\n  max_concurrent = %d\n  max_concurrent_per_user = %d\n  global_read_bps = %q\n  global_write_bps = %q\n  per_user_read_bps = %q\n  per_user_write_bps = %q\n  queue_timeout = %q\n",
		cfg.Limits.Enabled, cfg.Limits.MaxConcurrent, cfg.Limits.MaxConcurrentPerUser,
		cfg.Limits.GlobalReadBPS, cfg.Limits.GlobalWriteBPS,
		cfg.Limits.PerUserReadBPS, cfg.Limits.PerUserWriteBPS, cfg.Limits.QueueTimeout)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return err
	}
	// 已有文件可能保留了旧权限，显式收紧。
	return os.Chmod(path, 0o600)
}

const (
	secretsDirName      = "secrets"
	credentialsFileName = "s3_credentials.json"
)

// serviceSecrets 保存 ft-server 使用的两组 RustFS 服务身份凭据。
// JSON key 直接使用 IAM 用户名，便于和 RustFS Console 中的账号对应。
type serviceSecrets struct {
	// Data 是 ft-data 的凭据，用于代理用户的文件列表、下载和上传等数据面操作。
	Data Credentials `json:"ft-data"`
	// Admin 是 ft-admin 的凭据，用于创建/删除用户、绑定策略/组和清理数据。
	Admin Credentials `json:"ft-admin"`
}

// ServiceSecretsPath 返回当前运行目录下的服务凭据文件路径。
func ServiceSecretsPath() string {
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, secretsDirName, credentialsFileName)
	}
	return filepath.Join(secretsDirName, credentialsFileName)
}

// SaveServiceSecrets 把两组服务身份凭据以 JSON 格式写入 secrets/s3_credentials.json。
// 文件包含 "ft-data" 和 "ft-admin" 两个对象。
func SaveServiceSecrets(data, admin Credentials) error {
	var b bytes.Buffer
	// 关闭 HTML 转义，避免密码中的 & < > 被写成 \uXXXX。
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(serviceSecrets{Data: data, Admin: admin}); err != nil {
		return err
	}
	body := b.Bytes()
	p := ServiceSecretsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, body, 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// LoadServiceSecrets 读取 secrets/s3_credentials.json。
// 文件必须包含 "ft-data" 和 "ft-admin" 两组凭据。
func LoadServiceSecrets() (Credentials, Credentials, error) {
	path := ServiceSecretsPath()
	body, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, Credentials{}, err
	}
	var s serviceSecrets
	if err := json.Unmarshal(body, &s); err != nil {
		return Credentials{}, Credentials{}, fmt.Errorf("解析 %s: %w", path, err)
	}
	if s.Data.AccessKey == "" || s.Data.SecretKey == "" || s.Admin.AccessKey == "" || s.Admin.SecretKey == "" {
		return Credentials{}, Credentials{}, errors.New("s3_credentials.json 缺少 ft-data 或 ft-admin 凭据")
	}
	return s.Data, s.Admin, nil
}
