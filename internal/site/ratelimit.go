package site

import (
	"sync"
	"time"
)

// Limiter is a set of token buckets keyed by site. It lives in one
// replica's memory: limits are per replica, which is coarse but needs no
// coordination. Buckets idle for a while are pruned.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
	last    time.Time
}

type bucket struct {
	tokens float64
	rate   float64 // tokens per second
	burst  float64
	at     time.Time
}

// NewLimiter creates an empty limiter.
func NewLimiter() *Limiter {
	return &Limiter{buckets: make(map[string]*bucket), now: time.Now}
}

// Allow takes one token from the bucket for key, creating it with the
// given rate (per second) and burst if needed. rate <= 0 means unlimited.
func (l *Limiter) Allow(key string, rate int, burst int) bool {
	if rate <= 0 {
		return true
	}
	if burst <= 0 {
		burst = rate
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(burst), rate: float64(rate), burst: float64(burst), at: now}
		l.buckets[key] = b
	} else {
		// Limits can change between refreshes; follow the latest.
		b.rate, b.burst = float64(rate), float64(burst)
		elapsed := now.Sub(b.at).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * b.rate
			if b.tokens > b.burst {
				b.tokens = b.burst
			}
			b.at = now
		}
	}
	if now.Sub(l.last) > time.Minute {
		l.prune(now)
		l.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// prune drops buckets that have been full for over a minute (locked).
func (l *Limiter) prune(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.at) > time.Minute {
			delete(l.buckets, k)
		}
	}
}
