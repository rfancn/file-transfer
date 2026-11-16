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
