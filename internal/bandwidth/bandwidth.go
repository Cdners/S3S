// Package bandwidth meters object payload bytes across every live task.
package bandwidth

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/Cdners/S3S/internal/core"
)

// Burst is at most 50 ms of the direction's budget or 32 KiB. One very
// slow direction may need longer than 50 ms to earn even one whole byte.
const maxChunk = 32 << 10

type bucket struct {
	mu      sync.Mutex
	turn    chan struct{}
	rate    int64
	credit  float64
	updated time.Time
	changed chan struct{}
}

func (b *bucket) set(rate int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.turn == nil {
		b.turn = make(chan struct{}, 1)
	}
	if b.rate == rate && b.changed != nil {
		return
	}
	if b.changed != nil {
		close(b.changed)
	}
	b.rate = rate
	b.credit = 0
	b.updated = time.Now()
	b.changed = make(chan struct{})
}
func (b *bucket) burst() int64 { return max(1, min(maxChunk, b.rate/20)) }
func (b *bucket) readSize(requested int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rate == 0 {
		return min(requested, maxChunk)
	}
	return min(requested, int(b.burst()))
}

func (b *bucket) acquire(ctx context.Context, requested int) (int, error) {
	// Queue waiters in this direction. Only the front reader owns a timer.
	select {
	case b.turn <- struct{}{}:
		defer func() { <-b.turn }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		b.mu.Lock()
		generation := b.changed
		if b.rate == 0 {
			b.mu.Unlock()
			return min(requested, maxChunk), nil
		}
		now := time.Now()
		capBytes := b.burst()
		b.credit = min(float64(capBytes), b.credit+now.Sub(b.updated).Seconds()*float64(b.rate))
		b.updated = now
		want := min(requested, int(capBytes))
		if b.credit >= float64(want) {
			b.credit -= float64(want)
			b.mu.Unlock()
			return want, nil
		}
		wait := time.Duration(math.Ceil((float64(want) - b.credit) * float64(time.Second) / float64(b.rate)))
		b.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-generation:
			timer.Stop()
		case <-timer.C:
		}
	}
}

type Limiter struct{ upload, download bucket }

func New(options core.TransferOptions) *Limiter {
	l := &Limiter{}
	l.Set(options)
	return l
}

// Set takes canonical effective budgets returned by store.SaveTransferOptions.
func (l *Limiter) Set(o core.TransferOptions) {
	l.upload.set(o.UploadBytesPerSecond)
	l.download.set(o.DownloadBytesPerSecond)
}
