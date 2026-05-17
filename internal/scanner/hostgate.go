package scanner

import (
	"context"
	"net/url"
	"sync"
	"time"
)

// hostGate enforces a minimum delay between requests to the same host.
type hostGate struct {
	delay time.Duration
	mu    sync.Mutex
	last  map[string]time.Time
}

func newHostGate(delay time.Duration) *hostGate {
	return &hostGate{delay: delay, last: make(map[string]time.Time)}
}

// wait blocks until the per-host delay has elapsed, or ctx is canceled.
// Returns ctx.Err() on cancellation.
func (g *hostGate) wait(ctx context.Context, rawURL string) error {
	if g.delay <= 0 {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	host := u.Host

	g.mu.Lock()
	last := g.last[host]
	now := time.Now()
	wait := time.Duration(0)
	if !last.IsZero() {
		elapsed := now.Sub(last)
		if elapsed < g.delay {
			wait = g.delay - elapsed
		}
	}
	g.last[host] = now.Add(wait)
	g.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
