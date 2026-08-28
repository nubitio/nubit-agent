package command

import (
	"sync"
	"time"
)

// tokenBucket is the in-memory rate limit primitive behind the executor's
// per-type caps. It is intentionally simple: a capacity, a steady refill
// rate, and a counter of available tokens. The refill is computed on demand
// from elapsed wall-clock time, so the bucket does not need a background
// goroutine. The bucket is not persisted: restarting the agent resets every
// type's quota, which is the documented trade-off — a control plane that
// floods the agent right after a restart will briefly get the full bucket.
type tokenBucket struct {
	mu           sync.Mutex
	tokens       float64
	capacity     float64
	refillPerSec float64
	lastRefill   time.Time
}

func newTokenBucket(perMinute float64) *tokenBucket {
	// Burst capacity equals one minute's worth: that is the longest a
	// control plane can run at peak before the steady-state cap kicks in,
	// and matches what operators expect when they read "30 per minute".
	capacity := perMinute
	if capacity < 1 {
		capacity = 1
	}
	refillPerSec := perMinute / 60.0
	return &tokenBucket{
		tokens:       capacity,
		capacity:     capacity,
		refillPerSec: refillPerSec,
		lastRefill:   time.Now(),
	}
}

// allow tries to consume one token. It returns ok=true on success, and
// ok=false with retryAfter as the time the caller should wait before the
// bucket has at least one token available again.
func (bucket *tokenBucket) allow() (bool, time.Duration) {
	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	if elapsed > 0 {
		bucket.tokens += elapsed * bucket.refillPerSec
		if bucket.tokens > bucket.capacity {
			bucket.tokens = bucket.capacity
		}
		bucket.lastRefill = now
	}

	if bucket.tokens >= 1 {
		bucket.tokens -= 1
		return true, 0
	}

	missing := 1 - bucket.tokens
	seconds := missing / bucket.refillPerSec
	return false, time.Duration(seconds * float64(time.Second))
}
