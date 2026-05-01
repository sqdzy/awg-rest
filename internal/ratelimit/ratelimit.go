// Package ratelimit provides a small, swappable token-bucket rate limiter.
// The Limiter interface keeps the door open for a Redis-backed implementation
// in production (atomic Lua, shared counters); the in-memory implementation
// shipped here is sufficient for single-process control planes and tests.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is the contract used by HTTP middleware.
type Limiter interface {
	// Allow returns (allowed, retryAfter). When allowed is false, retryAfter
	// is a hint to the client of the recommended wait until the next token.
	Allow(key string) (bool, time.Duration)
}

// TokenBucket is a per-key token bucket with capacity and refill rate.
type TokenBucket struct {
	Capacity   int
	RefillRate float64 // tokens per second

	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewTokenBucket constructs a bucket. capacity must be > 0; refillPerSec must be > 0.
func NewTokenBucket(capacity int, refillPerSec float64) *TokenBucket {
	return &TokenBucket{
		Capacity:   capacity,
		RefillRate: refillPerSec,
		buckets:    map[string]*bucket{},
		now:        time.Now,
	}
}

// Allow consumes one token if available.
func (t *TokenBucket) Allow(key string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	b, ok := t.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(t.Capacity), last: now}
		t.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = min64(float64(t.Capacity), b.tokens+elapsed*t.RefillRate)
		b.last = now
	}
	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true, 0
	}
	missing := 1.0 - b.tokens
	wait := time.Duration(missing / t.RefillRate * float64(time.Second))
	return false, wait
}

// SetNow is a test hook.
func (t *TokenBucket) SetNow(fn func() time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = fn
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
