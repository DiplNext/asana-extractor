package asana

import (
	"context"
	"sync"
	"time"
)

// pacer spaces request starts evenly (1 / rate) and enforces a shared
// cooldown after a 429, so every request made with the token — including
// retries — obeys the same budget and the server's Retry-After.
type pacer struct {
	mu           sync.Mutex
	interval     time.Duration
	next         time.Time
	blockedUntil time.Time

	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func newPacer(perMinute int, now func() time.Time, sleep func(context.Context, time.Duration) error) *pacer {
	return &pacer{interval: time.Minute / time.Duration(perMinute), now: now, sleep: sleep}
}

// wait blocks until the caller may start a request. The slot is reserved
// before sleeping so concurrent callers queue up instead of bursting.
func (p *pacer) wait(ctx context.Context) error {
	p.mu.Lock()
	now := p.now()
	start := latest(now, p.next, p.blockedUntil)
	p.next = start.Add(p.interval)
	p.mu.Unlock()

	if d := start.Sub(now); d > 0 {
		return p.sleep(ctx, d)
	}
	return ctx.Err()
}

// cooldown forbids request starts before until. It never shortens an
// existing cooldown.
func (p *pacer) cooldown(until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if until.After(p.blockedUntil) {
		p.blockedUntil = until
	}
}

func latest(ts ...time.Time) time.Time {
	out := ts[0]
	for _, t := range ts[1:] {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
