package server

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipLimiter is a per-key token-bucket limiter with automatic cleanup.
type ipLimiter struct {
	mu      sync.Mutex
	entries map[string]*limiterEntry
	rate    rate.Limit
	burst   int
	ttl     time.Duration
}

type limiterEntry struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

func newIPLimiter(ctx context.Context, perMinute, burst int) *ipLimiter {
	l := &ipLimiter{
		entries: make(map[string]*limiterEntry),
		rate:    rate.Limit(float64(perMinute) / 60.0),
		burst:   burst,
		ttl:     10 * time.Minute,
	}
	go l.janitor(ctx)
	return l
}

// Allow reports whether key may proceed now.
func (l *ipLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(l.rate, l.burst)}
		l.entries[key] = e
	}
	e.lastSeen = time.Now()
	return e.lim.Allow()
}

func (l *ipLimiter) janitor(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.mu.Lock()
			for k, e := range l.entries {
				if now.Sub(e.lastSeen) > l.ttl {
					delete(l.entries, k)
				}
			}
			l.mu.Unlock()
		}
	}
}
