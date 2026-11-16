# ft-server 应用层 I/O 限速实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `ft-server` 应用层限制 `ft-client` 的上传、下载和分片上传，避免 5～20 个并发客户端打满 RustFS 的 RAID6 存储。

**Architecture:** 全局并发槽 + 单用户并发槽保护 HDD 寻道；每用户、全局两级令牌桶限制业务字节速率；槽位排队超时返回 429 + `Retry-After`；`ft-client` 自动退避重试。系统层 cgroup/blkio 和 md 调优不在本计划范围。

**Tech Stack:** Go 1.26、Gin、minio-go v7、`golang.org/x/time/rate`。

**设计文档：** `doc/specs/2026-11-16-rustfs-io-throttling-design.md`

**范围边界：**

- 只实现应用层；Console/Cyberduck 直连和 RustFS 容器的 cgroup 由运维侧处理。
- `ft-admin clean` 的限速属于设计文档中的二期，本计划不实现。
- 默认 `bps = 0`，只开并发闸门；带宽值等 warp 基线后填入。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/config/config.go` | 新增 `[limits]` 配置、运行时解析和校验 |
| `internal/limiter/limiter.go` | 全局/单用户并发槽、每用户状态、元数据槽、GC |
| `internal/limiter/reader.go` | 两级令牌桶 reader |
| `internal/server/app/app.go` | `App` 持有 `*limiter.Limiter` |
| `internal/server/server.go` | 创建 limiter，关闭时停止 GC |
| `internal/server/response/response.go` | 429 + `Retry-After` 响应 |
| `internal/server/handler/handler.go` | 获取槽位、包装 reader 的公共 helper |
| `internal/server/handler/file.go` | List/Download/Upload 接入 |
| `internal/server/handler/multipart.go` | 分片上传接入 |
| `internal/client/api.go` | 429/503 重试与 `Retry-After` 解析 |
| `config/ft-server.example.toml` | `[limits]` 示例 |
| `README.md` | 配置、行为、测量说明 |

---

## Task 0: 添加 x/time 依赖

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: 拉取依赖**

```bash
GOPROXY=https://goproxy.cn,direct go get golang.org/x/time@v0.16.0
```

- [ ] **Step 2: 确认 go.mod 中出现直接依赖**

```bash
rg -n "golang.org/x/time" go.mod
```

Expected: `golang.org/x/time v0.16.0`

- [ ] **Step 3: 提交**

```bash
git add go.mod go.sum
git commit -m "chore: add x/time dependency"
```

---

## Task 1: 增加 `[limits]` 配置

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写失败的配置测试**

在 `internal/config/config_test.go` 的导入中加入 `"time"`，然后在文件末尾追加：

```go
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
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/config -run 'TestLimits' -v
```

Expected: 编译失败，提示 `cfg.Limits` 不存在。

- [ ] **Step 3: 增加配置结构**

在 `internal/config/config.go` 的 `MultipartSection` 之后增加：

```go
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
```

在 `ServerConfig` 中增加字段：

```go
Limits    LimitsSection    `toml:"limits"`
```

在 `Runtime` 结构体中增加：

```go
Limits Limits
```

在 `Runtime` 结构体之后增加运行时结构：

```go
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
```

- [ ] **Step 4: 增加默认值**

在 `DefaultServer()` 返回的 `ServerConfig` 中增加：

```go
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
```

- [ ] **Step 5: 在 `Runtime()` 中解析和校验**

在 `func (c ServerConfig) Runtime(path string) (Runtime, error)` 的 `var err error` 之后、`rt.TokenTTL` 之前插入：

```go
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
```

在 `ParseSize` 附近增加：

```go
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
```

- [ ] **Step 6: 写回配置**

在 `SaveServerConfig` 的 `[multipart]` 输出之后追加：

```go
fmt.Fprintf(&b, "\n[limits]\n  enabled = %t\n  max_concurrent = %d\n  max_concurrent_per_user = %d\n  global_read_bps = %q\n  global_write_bps = %q\n  per_user_read_bps = %q\n  per_user_write_bps = %q\n  queue_timeout = %q\n",
	cfg.Limits.Enabled, cfg.Limits.MaxConcurrent, cfg.Limits.MaxConcurrentPerUser,
	cfg.Limits.GlobalReadBPS, cfg.Limits.GlobalWriteBPS,
	cfg.Limits.PerUserReadBPS, cfg.Limits.PerUserWriteBPS, cfg.Limits.QueueTimeout)
```

- [ ] **Step 7: 更新配置写回测试**

在 `internal/config/config_test.go` 中检查 `SaveServerConfig` 的断言列表里加入：

```go
"[limits]", "max_concurrent = 4", "queue_timeout = \"10s\"",
```

- [ ] **Step 8: 运行配置测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/config -v
```

Expected: 全部 PASS。

- [ ] **Step 9: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add app-level I/O limits section"
```

---

## Task 2: 实现 limiter

**Files:**
- Create: `internal/limiter/limiter.go`
- Create: `internal/limiter/reader.go`
- Test: `internal/limiter/limiter_test.go`

- [ ] **Step 1: 写失败的 limiter 测试**

创建 `internal/limiter/limiter_test.go`：

```go
package limiter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rfancn/file-transfer/internal/config"
)

func testConfig() config.Limits {
	return config.Limits{
		Enabled:              true,
		MaxConcurrent:        2,
		MaxConcurrentPerUser: 1,
		QueueTimeout:         50 * time.Millisecond,
	}
}

func TestAcquireTransferGlobalLimit(t *testing.T) {
	l := New(testConfig())
	defer l.Close()
	ctx := context.Background()

	r1, err := l.AcquireTransfer(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := l.AcquireTransfer(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.AcquireTransfer(ctx, "c"); !errors.Is(err, ErrBusy) {
		t.Fatalf("期望 ErrBusy，得到 %v", err)
	}
	r1()
	r2()

	r3, err := l.AcquireTransfer(ctx, "c")
	if err != nil {
		t.Fatalf("释放后应能获取槽位: %v", err)
	}
	r3()
}

func TestAcquireTransferPerUserLimit(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrent = 4
	l := New(cfg)
	defer l.Close()
	ctx := context.Background()

	r1, err := l.AcquireTransfer(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.AcquireTransfer(ctx, "a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("同一用户第二个请求应排队超时，得到 %v", err)
	}
	r2, err := l.AcquireTransfer(ctx, "b")
	if err != nil {
		t.Fatalf("其他用户不应被阻塞: %v", err)
	}
	r1()
	r2()
}

func TestAcquireMetadataIndependent(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrent = 1
	l := New(cfg)
	defer l.Close()
	ctx := context.Background()

	r1, err := l.AcquireTransfer(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := l.AcquireMetadata(ctx)
	if err != nil {
		t.Fatalf("元数据池应独立: %v", err)
	}
	r1()
	r2()
}

func TestDisabledLimiter(t *testing.T) {
	l := New(config.Limits{Enabled: false})
	defer l.Close()

	release, err := l.AcquireTransfer(context.Background(), "a")
	if err != nil || release == nil {
		t.Fatalf("disabled: release=%v err=%v", release, err)
	}
	release()

	r := l.WrapUpload(context.Background(), bytes.NewReader([]byte("x")), "a")
	if r == nil {
		t.Fatal("WrapUpload 不应返回 nil")
	}
}

func TestRateLimitUpload(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrent = 4
	cfg.MaxConcurrentPerUser = 2
	cfg.GlobalWriteBPS = 1 << 20
	l := New(cfg)
	defer l.Close()

	data := bytes.Repeat([]byte("x"), (1<<20)+(256<<10))
	start := time.Now()
	_, err := io.ReadAll(l.WrapUpload(context.Background(), bytes.NewReader(data), "a"))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed < 200*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("1.25MiB @ 1MiB/s 耗时 %v，期望约 250ms", elapsed)
	}
}

func TestRateLimitContextCancel(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrent = 4
	cfg.GlobalWriteBPS = 64 << 10
	l := New(cfg)
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	data := bytes.Repeat([]byte("x"), (1<<20)+(64<<10))
	if _, err := io.ReadAll(l.WrapUpload(ctx, bytes.NewReader(data), "a")); err == nil {
		t.Fatal("期望 context 取消错误")
	}
}

func TestUserGCRemovesIdleUsers(t *testing.T) {
	l := New(testConfig())
	defer l.Close()

	l.WrapUpload(context.Background(), bytes.NewReader(nil), "a")
	l.mu.Lock()
	for _, u := range l.users {
		u.lastUsed = time.Now().Add(-2 * userStateTTL)
	}
	l.gcLocked(time.Now())
	_, ok := l.users["a"]
	l.mu.Unlock()
	if ok {
		t.Fatal("空闲用户应被清理")
	}
}

func TestUserGCKeepsActiveUsers(t *testing.T) {
	l := New(testConfig())
	defer l.Close()

	release, err := l.AcquireTransfer(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	l.mu.Lock()
	l.users["a"].lastUsed = time.Now().Add(-2 * userStateTTL)
	l.gcLocked(time.Now())
	_, ok := l.users["a"]
	l.mu.Unlock()
	if !ok {
		t.Fatal("活跃用户不应被清理")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/limiter -v
```

Expected: 编译失败，提示 `New`、`ErrBusy` 等未定义。

- [ ] **Step 3: 实现 `internal/limiter/limiter.go`**

```go
// Package limiter 提供 ft-server 应用层的并发与带宽限速。
package limiter

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/rfancn/file-transfer/internal/config"
	"golang.org/x/time/rate"
)

const (
	// metadataConcurrency 是 List/Stat 等元数据操作的固定并发上限。
	metadataConcurrency = 8
	// userStateTTL 是无活动用户限速器的保留时间。
	userStateTTL = 10 * time.Minute
	// userStateMax 是触发立即清理的用户表阈值。
	userStateMax = 256
	// readBufferSize 是限速 reader 的单次读取上限。
	readBufferSize = 1 << 20
	// maxBurst 限制令牌桶的最大突发量。
	maxBurst = 4 << 20
)

// ErrBusy 表示排队超时。
var ErrBusy = errors.New("limiter: busy")

// Limiter 负责全局/单用户并发和两级令牌桶。
type Limiter struct {
	cfg      config.Limits
	global   chan struct{}
	meta     chan struct{}
	globalUp *rate.Limiter
	globalDn *rate.Limiter

	mu       sync.Mutex
	users    map[string]*userState
	stop     chan struct{}
	done     chan struct{}
	closeOne sync.Once
}

type userState struct {
	slots    chan struct{}
	up       *rate.Limiter
	down     *rate.Limiter
	lastUsed time.Time
}

// New 根据配置创建 Limiter。Enabled 为 false 时所有方法都是空操作。
func New(cfg config.Limits) *Limiter {
	l := &Limiter{
		cfg:      cfg,
		users:    make(map[string]*userState),
		globalUp: newRateLimiter(cfg.GlobalWriteBPS),
		globalDn: newRateLimiter(cfg.GlobalReadBPS),
	}
	if !cfg.Enabled {
		return l
	}
	l.global = make(chan struct{}, cfg.MaxConcurrent)
	l.meta = make(chan struct{}, metadataConcurrency)
	l.stop = make(chan struct{})
	l.done = make(chan struct{})
	go l.gcLoop()
	return l
}

// Close 停止用户表清理协程。
func (l *Limiter) Close() {
	if l.stop == nil {
		return
	}
	l.closeOne.Do(func() { close(l.stop) })
	<-l.done
}

// AcquireTransfer 同时获取全局和单用户的传输槽。
func (l *Limiter) AcquireTransfer(ctx context.Context, username string) (func(), error) {
	if !l.cfg.Enabled {
		return func() {}, nil
	}
	u := l.user(username)
	waitCtx, cancel := context.WithTimeout(ctx, l.cfg.QueueTimeout)
	defer cancel()

	if err := acquire(waitCtx, u.slots); err != nil {
		return nil, busyErr(ctx)
	}
	if err := acquire(waitCtx, l.global); err != nil {
		release(u.slots)
		return nil, busyErr(ctx)
	}
	return func() {
		release(l.global)
		release(u.slots)
		l.touch(u)
	}, nil
}

// AcquireMetadata 获取元数据槽。
func (l *Limiter) AcquireMetadata(ctx context.Context) (func(), error) {
	if !l.cfg.Enabled {
		return func() {}, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, l.cfg.QueueTimeout)
	defer cancel()
	if err := acquire(waitCtx, l.meta); err != nil {
		return nil, busyErr(ctx)
	}
	return func() { release(l.meta) }, nil
}

// WrapUpload 给上传请求体加每用户和全局写限速。
func (l *Limiter) WrapUpload(ctx context.Context, r io.Reader, username string) io.Reader {
	if !l.cfg.Enabled {
		return r
	}
	u := l.user(username)
	return newReader(ctx, r, u.up, l.globalUp)
}

// WrapDownload 给下载响应体加每用户和全局读限速。
func (l *Limiter) WrapDownload(ctx context.Context, r io.Reader, username string) io.Reader {
	if !l.cfg.Enabled {
		return r
	}
	u := l.user(username)
	return newReader(ctx, r, u.down, l.globalDn)
}

func busyErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrBusy
}

func acquire(ctx context.Context, ch chan struct{}) error {
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func release(ch chan struct{}) { <-ch }

func newRateLimiter(bps int64) *rate.Limiter {
	if bps <= 0 {
		return nil
	}
	burst := bps
	if burst < readBufferSize {
		burst = readBufferSize
	}
	if burst > maxBurst {
		burst = maxBurst
	}
	return rate.NewLimiter(rate.Limit(bps), int(burst))
}

func (l *Limiter) user(username string) *userState {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if len(l.users) >= userStateMax {
		l.gcLocked(now)
	}
	u := l.users[username]
	if u == nil {
		u = &userState{
			slots:    make(chan struct{}, l.cfg.MaxConcurrentPerUser),
			up:       newRateLimiter(l.cfg.PerUserWriteBPS),
			down:     newRateLimiter(l.cfg.PerUserReadBPS),
			lastUsed: now,
		}
		l.users[username] = u
	}
	u.lastUsed = now
	return u
}

func (l *Limiter) touch(u *userState) {
	l.mu.Lock()
	u.lastUsed = time.Now()
	l.mu.Unlock()
}

func (l *Limiter) gcLoop() {
	defer close(l.done)
	t := time.NewTicker(userStateTTL)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			l.mu.Lock()
			l.gcLocked(time.Now())
			l.mu.Unlock()
		case <-l.stop:
			return
		}
	}
}

func (l *Limiter) gcLocked(now time.Time) {
	for name, u := range l.users {
		if len(u.slots) == 0 && now.Sub(u.lastUsed) > userStateTTL {
			delete(l.users, name)
		}
	}
}
```

- [ ] **Step 4: 实现 `internal/limiter/reader.go`**

```go
package limiter

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

type limitedReader struct {
	ctx  context.Context
	r    io.Reader
	lims []*rate.Limiter
}

func newReader(ctx context.Context, r io.Reader, user, global *rate.Limiter) io.Reader {
	lims := make([]*rate.Limiter, 0, 2)
	if user != nil {
		lims = append(lims, user)
	}
	if global != nil {
		lims = append(lims, global)
	}
	if len(lims) == 0 {
		return r
	}
	return &limitedReader{ctx: ctx, r: r, lims: lims}
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if len(p) > readBufferSize {
		p = p[:readBufferSize]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		for _, lim := range r.lims {
			if werr := lim.WaitN(r.ctx, n); werr != nil {
				return 0, werr
			}
		}
	}
	return n, err
}
```

- [ ] **Step 5: 运行 limiter 测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/limiter -v
```

Expected: 全部 PASS。

- [ ] **Step 6: 运行 race 测试**

```bash
GOPROXY=https://goproxy.cn,direct go test -race ./internal/limiter
```

Expected: PASS。

- [ ] **Step 7: 提交**

```bash
git add internal/limiter
git commit -m "feat(limiter): add concurrency and bandwidth limiter"
```

---

## Task 3: 接入 `App` 和服务器生命周期

**Files:**
- Modify: `internal/server/app/app.go`
- Modify: `internal/server/server.go`

- [ ] **Step 1: 在 `App` 中增加字段**

在 `internal/server/app/app.go` 增加导入：

```go
"github.com/rfancn/file-transfer/internal/limiter"
```

在 `App` 结构体中增加：

```go
Limits   *limiter.Limiter
```

- [ ] **Step 2: 在 `server.New` 中创建 limiter**

在 `internal/server/server.go` 增加导入：

```go
"github.com/rfancn/file-transfer/internal/limiter"
```

在 `server.New` 构造 `s := &Server{app: &app.App{...}}` 时增加：

```go
Limits: limiter.New(rt.Limits),
```

- [ ] **Step 3: 关闭时停止 GC**

把 `Shutdown` 改为：

```go
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.httpSrv.Shutdown(ctx)
	if s.app.Limits != nil {
		s.app.Limits.Close()
	}
	return err
}
```

- [ ] **Step 4: 编译**

```bash
GOPROXY=https://goproxy.cn,direct go build ./...
```

Expected: 退出码 0。

- [ ] **Step 5: 提交**

```bash
git add internal/server/app/app.go internal/server/server.go
git commit -m "feat(server): wire app-level limiter"
```

---

## Task 4: 增加 429 响应

**Files:**
- Modify: `internal/server/response/response.go`
- Test: `internal/server/response/response_test.go`

- [ ] **Step 1: 写失败的响应测试**

创建 `internal/server/response/response_test.go`：

```go
package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/protocol"
)

func TestErrBusy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	ErrBusy(c, 10*time.Second)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d，期望 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "10" {
		t.Fatalf("Retry-After = %q，期望 10", got)
	}
	var body protocol.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "server_busy" {
		t.Fatalf("error = %q，期望 server_busy", body.Error)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/server/response -run TestErrBusy -v
```

Expected: 编译失败，提示 `ErrBusy` 未定义。

- [ ] **Step 3: 实现 `ErrBusy`**

把 `internal/server/response/response.go` 改为：

```go
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
```

- [ ] **Step 4: 运行测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/server/response -v
```

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/server/response
git commit -m "feat(response): add 429 busy response with Retry-After"
```

---

## Task 5: 接入 List/Download/Upload

**Files:**
- Modify: `internal/server/handler/handler.go`
- Modify: `internal/server/handler/file.go`
- Test: `internal/server/handler/file_test.go`

- [ ] **Step 1: 写失败的 handler 测试**

在 `internal/server/handler/file_test.go` 的现有导入中补充 `"context"`、`"config"`、`"limiter"`、`"app"`；`net/http`、`httptest`、`testing`、`time`、`gin` 已存在。合并后的导入应为：

```go
import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/limiter"
	"github.com/rfancn/file-transfer/internal/server/app"
)
```

在文件末尾追加：

```go
func TestDownloadReturnsBusy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rt := config.Runtime{Limits: config.Limits{
		Enabled:              true,
		MaxConcurrent:        1,
		MaxConcurrentPerUser: 1,
		QueueTimeout:         10 * time.Millisecond,
	}}
	lim := limiter.New(rt.Limits)
	defer lim.Close()

	release, err := lim.AcquireTransfer(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	h := &Handler{App: &app.App{Limits: lim, RT: rt}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/files/inbound/a.bin", nil)
	c.Set("username", "alice")
	c.Params = gin.Params{{Key: "path", Value: "/inbound/a.bin"}}

	h.Download(c)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d，期望 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q，期望 1", got)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/server/handler -run TestDownloadReturnsBusy -v
```

Expected: 测试失败，`Download` 没有返回 429（或在 nil `Data` 上 panic）。

- [ ] **Step 3: 在 `handler.go` 增加公共 helper**

把 `internal/server/handler/handler.go` 改为：

```go
// Package handler 实现 public、client 和 admin 三组 HTTP 接口。
package handler

import (
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/limiter"
	"github.com/rfancn/file-transfer/internal/server/app"
	"github.com/rfancn/file-transfer/internal/server/response"
)

const (
	maxJSONBody     = 4 << 10
	userPasswordLen = 8
	copyBufferSize  = 1 << 20
)

type Handler struct {
	*app.App
}

func New(a *app.App) *Handler {
	return &Handler{App: a}
}

func currentUser(c *gin.Context) string {
	return c.GetString("username")
}

func wildcardPath(c *gin.Context) string {
	return strings.TrimPrefix(c.Param("path"), "/")
}

// acquireTransfer 获取全局和单用户传输槽。返回 false 时已写入错误响应。
func (h *Handler) acquireTransfer(c *gin.Context, username string) (func(), bool) {
	if h.Limits == nil {
		return func() {}, true
	}
	release, err := h.Limits.AcquireTransfer(c.Request.Context(), username)
	if err != nil {
		if errors.Is(err, limiter.ErrBusy) {
			response.ErrBusy(c, h.RT.Limits.QueueTimeout)
		}
		return nil, false
	}
	return release, true
}

// acquireMetadata 获取元数据槽。返回 false 时已写入错误响应。
func (h *Handler) acquireMetadata(c *gin.Context) (func(), bool) {
	if h.Limits == nil {
		return func() {}, true
	}
	release, err := h.Limits.AcquireMetadata(c.Request.Context())
	if err != nil {
		if errors.Is(err, limiter.ErrBusy) {
			response.ErrBusy(c, h.RT.Limits.QueueTimeout)
		}
		return nil, false
	}
	return release, true
}

func (h *Handler) wrapUpload(c *gin.Context, r io.Reader, username string) io.Reader {
	if h.Limits == nil {
		return r
	}
	return h.Limits.WrapUpload(c.Request.Context(), r, username)
}

func (h *Handler) wrapDownload(c *gin.Context, r io.Reader, username string) io.Reader {
	if h.Limits == nil {
		return r
	}
	return h.Limits.WrapDownload(c.Request.Context(), r, username)
}
```

- [ ] **Step 4: 接入 `List`**

在 `file.go` 的 `List` 中，`dir` 校验通过之后、`h.Data.List` 之前插入：

```go
release, ok := h.acquireMetadata(c)
if !ok {
	return
}
defer release()
```

- [ ] **Step 5: 接入 `Download`**

在 `file.go` 的 `Download` 中，`key := username + "/" + rel` 之后、`h.Data.Stat` 之前插入：

```go
release, ok := h.acquireTransfer(c, username)
if !ok {
	return
}
defer release()
```

把：

```go
_, err = io.Copy(w, obj)
```

改为：

```go
_, err = io.CopyBuffer(w, h.wrapDownload(c, obj, username), make([]byte, copyBufferSize))
```

- [ ] **Step 6: 接入 `Upload`**

在 `file.go` 的 `Upload` 中，`key := username + "/" + rel` 之后、`h.Data.Put` 之前插入：

```go
release, ok := h.acquireTransfer(c, username)
if !ok {
	return
}
defer release()
```

把 `h.Data.Put` 的第三个参数改为：

```go
h.wrapUpload(c, c.Request.Body, username)
```

完整的 `Put` 调用和错误分支应为（增加了客户端断开检查，避免对已断开的连接写 502）：

```go
if err := h.Data.Put(c.Request.Context(), key, h.wrapUpload(c, c.Request.Body, username), c.Request.ContentLength); err != nil {
	if c.Request.Context().Err() != nil {
		return
	}
	logging.Error("upload", "key", key, "err", err)
	response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "上传失败，RustFS 服务不可用")
	return
}
```

- [ ] **Step 7: 运行 handler 测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/server/handler -v
```

Expected: 全部 PASS，包括新增的 `TestDownloadReturnsBusy`。

- [ ] **Step 8: 提交**

```bash
git add internal/server/handler/handler.go internal/server/handler/file.go internal/server/handler/file_test.go
git commit -m "feat(handler): apply transfer and metadata limits to file APIs"
```

---

## Task 6: 接入分片上传

**Files:**
- Modify: `internal/server/handler/multipart.go`
- Test: `internal/server/handler/multipart_test.go`（新建）

- [ ] **Step 1: 写失败的 multipart 测试**

创建 `internal/server/handler/multipart_test.go`：

```go
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/limiter"
	"github.com/rfancn/file-transfer/internal/server/app"
)

func TestMultipartPartReturnsBusy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rt := config.Runtime{
		Config: config.DefaultServer(),
		Limits: config.Limits{
			Enabled:              true,
			MaxConcurrent:        1,
			MaxConcurrentPerUser: 1,
			QueueTimeout:         10 * time.Millisecond,
		},
	}
	lim := limiter.New(rt.Limits)
	defer lim.Close()

	release, err := lim.AcquireTransfer(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	token, err := auth.Sign(rt.Config.Auth.UploadSecret, auth.MultipartClaims{
		Sub:        "alice",
		Path:       "alice/outbound/a.bin",
		PartSize:   5 << 20,
		S3UploadID: "u1",
		Iat:        time.Now().Unix(),
		Exp:        time.Now().Add(time.Hour).Unix(),
		Kind:       "multipart",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := &Handler{App: &app.App{Cfg: rt.Config, Limits: lim, RT: rt}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/v1/uploads/"+token+"/parts/1", strings.NewReader("x"))
	c.Set("username", "alice")
	c.Params = gin.Params{
		{Key: "upload_id", Value: token},
		{Key: "part", Value: "1"},
	}

	h.MultipartPart(c)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d，期望 429", w.Code)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/server/handler -run TestMultipartPartReturnsBusy -v
```

Expected: 测试失败，`MultipartPart` 没有返回 429（或在 nil `Data` 上 panic）。

- [ ] **Step 3: 接入 `MultipartInit`**

在 `MultipartInit` 的 `key := username + "/" + rel` 之后、`h.Data.NewMultipart` 之前插入：

```go
release, ok := h.acquireMetadata(c)
if !ok {
	return
}
defer release()
```

- [ ] **Step 4: 接入 `MultipartPart`**

在 `MultipartPart` 的 ContentLength 校验之后、`h.Data.PutPart` 之前插入：

```go
release, ok := h.acquireTransfer(c, username)
if !ok {
	return
}
defer release()
```

把 `h.Data.PutPart` 的请求体参数改为：

```go
h.wrapUpload(c, c.Request.Body, username)
```

完整的调用和错误分支应为（增加了客户端断开检查）：

```go
etag, err := h.Data.PutPart(c.Request.Context(), claims.Path, claims.S3UploadID, partNum, h.wrapUpload(c, c.Request.Body, username), c.Request.ContentLength)
if err != nil {
	if c.Request.Context().Err() != nil {
		return
	}
	if s3.IsNotFound(err) {
		response.Err(c, http.StatusNotFound, "upload_not_found", "分片上传不存在或已过期，请重新上传")
		return
	}
	logging.Error("multipart part", "path", rel, "part", partNum, "err", err)
	response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "上传分片失败")
	return
}
```

- [ ] **Step 5: 接入 `MultipartParts`**

在 `MultipartParts` 的 `verifyUploadID` 成功之后、`h.Data.ListParts` 之前插入：

```go
release, ok := h.acquireMetadata(c)
if !ok {
	return
}
defer release()
```

- [ ] **Step 6: 接入 `MultipartComplete`**

在 `MultipartComplete` 的 parts 校验和排序之后、`h.Data.CompleteMultipart` 之前插入：

```go
release, ok := h.acquireTransfer(c, username)
if !ok {
	return
}
defer release()
```

- [ ] **Step 7: 接入 `MultipartAbort`**

在 `MultipartAbort` 的 `verifyUploadID` 成功之后、`h.Data.AbortMultipart` 之前插入：

```go
release, ok := h.acquireMetadata(c)
if !ok {
	return
}
defer release()
```

- [ ] **Step 8: 运行 handler 测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/server/handler -v
```

Expected: 全部 PASS，包括 `TestMultipartPartReturnsBusy`。

- [ ] **Step 9: 提交**

```bash
git add internal/server/handler/multipart.go internal/server/handler/multipart_test.go
git commit -m "feat(handler): apply limits to multipart upload"
```

---

## Task 7: `ft-client` 429/503 重试

**Files:**
- Modify: `internal/client/api.go`
- Test: `internal/client/api_test.go`（新建）

- [ ] **Step 1: 写失败的重试测试**

创建 `internal/client/api_test.go`：

```go
package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExecuteRetriesTooManyRequests(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"server_busy","message":"busy"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client()}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.execute(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d，期望 200", resp.StatusCode)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d，期望 2", attempts)
	}
}

func TestExecuteStopsAfterMaxRetries(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"server_busy","message":"busy"}`)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client()}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.execute(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d，期望 429", resp.StatusCode)
	}
	if attempts != maxRetryAttempts {
		t.Fatalf("attempts = %d，期望 %d", attempts, maxRetryAttempts)
	}
}

func TestExecuteDoesNotRetryUnreplayableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client()}
	req, err := http.NewRequest(http.MethodPost, srv.URL, io.NopCloser(strings.NewReader("x")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.execute(req, nil); err == nil || !strings.Contains(err.Error(), "不可重放") {
		t.Fatalf("期望不可重放错误，得到 %v", err)
	}
}

func TestRetryWait(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "7")
	if got := retryWait(resp, 1); got != 7*time.Second {
		t.Fatalf("Retry-After 秒 = %v，期望 7s", got)
	}

	resp.Header.Set("Retry-After", time.Now().Add(2*time.Second).UTC().Format(http.TimeFormat))
	if got := retryWait(resp, 1); got <= 0 || got > 2*time.Second {
		t.Fatalf("Retry-After 日期 = %v", got)
	}

	resp.Header.Del("Retry-After")
	if got := retryWait(resp, 3); got < 3*time.Second {
		t.Fatalf("指数退避 = %v，期望至少 3s", got)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/client -run 'TestExecute|TestRetryWait' -v
```

Expected: 编译失败，提示 `maxRetryAttempts`、`retryWait` 未定义。

- [ ] **Step 3: 修改 `internal/client/api.go`**

在导入中增加：

```go
"math/rand"
"strconv"
```

在文件顶部常量区增加：

```go
const (
	maxRetryAttempts = 6
	maxRetryWait     = 5 * time.Minute
	retryBaseWait    = time.Second
)
```

把 `execute` 替换为：

```go
func (c *Client) execute(req *http.Request, getBody func() (io.ReadCloser, error)) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.mu.Lock()
		username, password := c.username, c.password
		c.mu.Unlock()
		if username == "" || password == "" {
			return resp, nil
		}
		resp.Body.Close()
		if err := c.Login(req.Context(), username, password); err != nil {
			return nil, err
		}
		req, err = replayRequest(req, getBody)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err = c.http.Do(req)
		if err != nil {
			return nil, err
		}
	}
	return c.retryBusy(req, getBody, resp)
}

func (c *Client) retryBusy(req *http.Request, getBody func() (io.ReadCloser, error), resp *http.Response) (*http.Response, error) {
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable {
			return resp, nil
		}
		wait := retryWait(resp, attempt)
		if attempt >= maxRetryAttempts || waited+wait > maxRetryWait {
			return resp, nil
		}
		resp.Body.Close()
		if err := sleepContext(req.Context(), wait); err != nil {
			return nil, err
		}
		next, err := replayRequest(req, getBody)
		if err != nil {
			return nil, err
		}
		resp, err = c.http.Do(next)
		if err != nil {
			return nil, err
		}
		req = next
		waited += wait
	}
}

func replayRequest(req *http.Request, getBody func() (io.ReadCloser, error)) (*http.Request, error) {
	if getBody == nil {
		getBody = req.GetBody
	}
	next := req.Clone(req.Context())
	if getBody != nil {
		body, err := getBody()
		if err != nil {
			return nil, err
		}
		next.Body = body
	} else if req.Body != nil {
		return nil, fmt.Errorf("请求体不可重放，请重试")
	}
	return next, nil
}

func retryWait(resp *http.Response, attempt int) time.Duration {
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
			return 0
		}
	}
	shift := attempt - 1
	if shift > 5 {
		shift = 5
	}
	base := retryBaseWait * time.Duration(1<<shift)
	jitter := time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
	return base + jitter
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
```

- [ ] **Step 4: 运行客户端测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/client -v
```

Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/client/api.go internal/client/api_test.go
git commit -m "feat(client): retry 429/503 with Retry-After"
```

---

## Task 8: 更新示例配置和 README

**Files:**
- Modify: `config/ft-server.example.toml`
- Modify: `README.md`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写示例配置解析测试**

在 `internal/config/config_test.go` 追加：

```go
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
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/config -run TestExampleServerConfigHasLimits -v
```

Expected: FAIL，提示「示例配置缺少 [limits] 段」。

- [ ] **Step 3: 修改示例配置**

在 `config/ft-server.example.toml` 末尾追加：

```toml
# 应用层 I/O 限速；bps 为 0 表示该维度不限，测量后再填。
[limits]
  enabled = true
  max_concurrent = 4
  max_concurrent_per_user = 1
  global_read_bps = "0"
  global_write_bps = "0"
  per_user_read_bps = "0"
  per_user_write_bps = "0"
  queue_timeout = "10s"
```

- [ ] **Step 4: 修改 README**

在 `README.md` 的「`ft-server` 运行与维护」章节内，在「日常运维」之后增加：

```markdown
### 应用层 I/O 限速

`ft-server` 通过 `[limits]` 限制经过它的上传、下载和分片上传：

| 配置 | 说明 | 默认 |
|---|---|---:|
| `max_concurrent` | 全局同时传输数 | 4 |
| `max_concurrent_per_user` | 单用户同时传输数 | 1 |
| `global_read_bps` | 全局下载带宽，0 表示不限 | 0 |
| `global_write_bps` | 全局上传带宽，0 表示不限 | 0 |
| `per_user_read_bps` | 单用户下载带宽 | 0 |
| `per_user_write_bps` | 单用户上传带宽 | 0 |
| `queue_timeout` | 排队超时 | 10s |

- 并发上限是第一道闸门，用于保护 HDD 寻道；bps 默认 0，先用 warp 测出对象层吞吐，再按 70% 左右填入。
- 单用户带宽建议取全局值的一半，防止一个用户占满。
- 超过 `queue_timeout` 返回 `429` 和 `Retry-After`，`ft-client` 会自动退避重试。
- Console/Cyberduck 直连不经过 `ft-server`，不受这些值限制，需要由系统层 cgroup 兜底。
- 修改配置后重启 `ft-server` 生效；置 `enabled = false` 可整体关闭限速。
```

- [ ] **Step 5: 运行配置测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/config -v
```

Expected: 全部 PASS。

- [ ] **Step 6: 提交**

```bash
git add config/ft-server.example.toml README.md internal/config/config_test.go
git commit -m "docs: document app-level I/O limits"
```

---

## Task 9: 全量验证

**Files:** 无新增。

- [ ] **Step 1: 格式化检查**

```bash
gofmt -l .
```

Expected: 无输出。

- [ ] **Step 2: 静态检查**

```bash
GOPROXY=https://goproxy.cn,direct go vet ./...
```

Expected: 退出码 0。

- [ ] **Step 3: 全量测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./...
```

Expected: 全部 PASS。

- [ ] **Step 4: Race 测试**

```bash
GOPROXY=https://goproxy.cn,direct go test -race ./...
```

Expected: 全部 PASS。

- [ ] **Step 5: 手工验收（有可用 ft-server 环境时）**

1. 配置 `max_concurrent = 1`、`max_concurrent_per_user = 1`、`queue_timeout = "2s"`，启动 `ft-server`。
2. 用两个 `ft-client` 同时下载大文件。
3. 预期：第二个请求要么在第一个完成后开始，要么收到 429；客户端自动重试后成功。
4. 检查响应头 `Retry-After` 存在，服务端日志出现 `server_busy`。

- [ ] **Step 6: 提交验证结果（如有文档变更）**

```bash
git status --short
```

Expected: 无未提交变更；如有，按实际变更提交。

---

## 自检清单

- [ ] 配置：`[limits]` 可解析、可校验、默认值正确、写回配置包含该段。
- [ ] limiter：全局并发、单用户并发、元数据独立池、两级令牌桶、context 取消、用户 GC 都有测试。
- [ ] handler：List/Download/Upload/分片上传都接入；429 带 `Retry-After`。
- [ ] client：429/503 重试、`Retry-After` 解析、最大次数、不可重放请求不重试。
- [ ] 文档：示例配置和 README 已更新。
- [ ] 全量 `go test -race ./...` 通过。

## 范围外（后续）

- `ft-admin clean` 的限速（设计文档 8.5）。
- RustFS 容器的 cgroup/blkio 和 md/内核调优（运维侧）。
- 元数据请求速率限制（`metadata_ops_per_sec`），如后续小文件压力明显再加。