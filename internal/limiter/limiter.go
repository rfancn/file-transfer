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
