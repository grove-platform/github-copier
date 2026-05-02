package services

import (
	"sync"
	"time"
)

// tokenBucket is a trivial fixed-window rate limiter keyed by opaque string
// (typically a hashed PAT digest). Not a strict token-bucket in the
// telecom sense — a fixed-window counter is good enough to cap LLM cost
// per operator, and is cheaper to reason about than leaky-bucket math.
//
// Eviction happens opportunistically on writes once the map grows past a
// soft cap; there's no background goroutine.
type tokenBucket struct {
	mu      sync.Mutex
	buckets map[string]*bucketState
	max     int
	window  time.Duration
}

type bucketState struct {
	remaining int
	resetAt   time.Time
}

func newTokenBucket(max int, window time.Duration) *tokenBucket {
	return &tokenBucket{
		buckets: make(map[string]*bucketState),
		max:     max,
		window:  window,
	}
}

// Allow decrements the caller's remaining allowance for the current window.
// Returns (allowed, resetAt). resetAt is always non-zero so callers can
// surface a Retry-After hint regardless of the allow/deny decision.
func (t *tokenBucket) Allow(key string) (bool, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	b, ok := t.buckets[key]
	if !ok || now.After(b.resetAt) {
		reset := now.Add(t.window)
		t.buckets[key] = &bucketState{remaining: t.max - 1, resetAt: reset}
		t.evictExpiredLocked(now)
		return true, reset
	}
	if b.remaining <= 0 {
		return false, b.resetAt
	}
	b.remaining--
	return true, b.resetAt
}

func (t *tokenBucket) evictExpiredLocked(now time.Time) {
	if len(t.buckets) < 256 {
		return
	}
	for k, b := range t.buckets {
		if now.After(b.resetAt) {
			delete(t.buckets, k)
		}
	}
}
