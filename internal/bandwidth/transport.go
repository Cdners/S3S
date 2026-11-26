package bandwidth

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/Cdners/S3S/internal/core"
)

type payloadKey struct{}

// WithPayload marks only the SDK call that exchanges file bytes. Browsing,
// signing, hashing, recovery checks, and multipart metadata need no allowance.
func WithPayload(ctx context.Context, direction core.Direction) context.Context {
	return context.WithValue(ctx, payloadKey{}, direction)
}

var defaultTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 3 * time.Minute
	return t
}()

type transport struct {
	limiter *Limiter
	base    http.RoundTripper
}

func (l *Limiter) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = defaultTransport
	}
	return transport{limiter: l, base: base}
}

type pacedBody struct {
	source  io.ReadCloser
	ctx     context.Context
	cancel  context.CancelFunc
	bucket  *bucket
	pending []byte
	readErr error
}

func pace(ctx context.Context, source io.ReadCloser, bucket *bucket) io.ReadCloser {
	life, cancel := context.WithCancel(ctx)
	return &pacedBody{source: source, ctx: life, cancel: cancel, bucket: bucket}
}

func (b *pacedBody) Close() error {
	b.cancel()
	return b.source.Close()
}

func (b *pacedBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	buffered := len(b.pending) > 0
	var n int
	if buffered {
		n = copy(p, b.pending)
	} else {
		// Pace actual bytes at handoff. Concurrent reads blocked in the OS
		// cannot accumulate reservations and burst when their data arrives.
		n, b.readErr = b.source.Read(p[:b.bucket.readSize(len(p))])
	}
	if n == 0 {
		return 0, b.readErr
	}
	allowed, err := b.bucket.acquire(b.ctx, n)
	if err != nil {
		return 0, err
	}
	if buffered {
		b.pending = b.pending[allowed:]
	} else if allowed < n {
		// A live budget reduction can shrink a quantum after the read.
		b.pending = append([]byte(nil), p[allowed:n]...)
	}
	if len(b.pending) > 0 {
		return allowed, nil
	}
	b.pending = nil
	return allowed, b.readErr
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	dir, _ := req.Context().Value(payloadKey{}).(core.Direction)
	if dir == core.Upload && req.Body != nil && req.Body != http.NoBody {
		// Wrap after SDK signing. A fresh SDK retry (or a request body replay
		// via GetBody) still draws from the same application's upload budget.
		clone := req.Clone(req.Context())
		wrap := func(body io.ReadCloser) io.ReadCloser { return pace(req.Context(), body, &t.limiter.upload) }
		clone.Body = wrap(req.Body)
		if original := req.GetBody; original != nil {
			clone.GetBody = func() (io.ReadCloser, error) {
				body, err := original()
				if err != nil {
					return nil, err
				}
				return wrap(body), nil
			}
		}
		req = clone
	}
	resp, err := t.base.RoundTrip(req)
	if err == nil && dir == core.Download && resp.Body != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		resp.Body = pace(req.Context(), resp.Body, &t.limiter.download)
	}
	return resp, err
}
