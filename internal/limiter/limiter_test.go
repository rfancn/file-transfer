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
		t.Fatalf("disabled: release 为 nil, err=%v", err)
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
