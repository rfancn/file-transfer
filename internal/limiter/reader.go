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
				// 有意丢弃已从底层读出但尚未限速的 n 字节：调用方会收到错误并终止流，
				// 不能把未等待令牌的数据继续返回，否则会绕过限速。
				// WaitN 在所需等待时间超过截止时间时会提前返回普通错误（此时 ctx 尚未到期），
				// 这里统一归一到 context 错误，便于调用方用 errors.Is 判断。
				if ctxErr := r.ctx.Err(); ctxErr != nil {
					return 0, ctxErr
				}
				if _, ok := r.ctx.Deadline(); ok {
					return 0, context.DeadlineExceeded
				}
				return 0, werr
			}
		}
	}
	return n, err
}
