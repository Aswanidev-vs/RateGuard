// Package limiter implements a concurrency-safe, in-process token-bucket
// rate limiter with one bucket per tenant. It starts no goroutines; callers
// own time progression and bucket cleanup.
package limiter

import (
	"math"
	"sync"
	"time"
)

const (
	// minRefillRate is used for non-positive configured limits. One token per
	// billion seconds (about 31.7 years) is effectively no refill while
	// keeping every duration calculation finite and NaN-free.
	minRefillRate = 1e-9

	// availabilityEpsilon absorbs floating-point noise at the one-token
	// boundary so a bucket refilled with exactly one token is not rejected.
	availabilityEpsilon = 1e-9

	// maxDuration is the largest representable time.Duration. Retry and reset
	// hints are clamped to it for pathological configurations.
	maxDuration = time.Duration(1<<63 - 1)
)

// Decision is the result of one admission check.
type Decision struct {
	// Allowed reports whether the request may proceed.
	Allowed bool
	// Limit is the bucket capacity, i.e. the effective burst size.
	Limit int
	// Remaining is the whole number of tokens left after the check.
	Remaining int
	// RetryAfter is how long until one token is available. It is zero when
	// Allowed is true.
	RetryAfter time.Duration
	// ResetAfter is how long until the bucket is full again.
	ResetAfter time.Duration
}

// bucket is the mutable state for one tenant. All access is serialized by the
// owning Limiter's mutex.
type bucket struct {
	tokens   float64
	capacity int
	rate     float64 // tokens per second
	last     time.Time
}

// Limiter is a set of per-tenant token buckets. It is safe for concurrent use.
// Its zero value is ready to use.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

// New returns an empty Limiter. Buckets are created on first use and start
// full.
func New() *Limiter {
	return &Limiter{buckets: make(map[string]*bucket)}
}

// Allow is AllowAt using the current wall clock.
func (l *Limiter) Allow(tenantID string, limitRPM, burst int) Decision {
	return l.AllowAt(tenantID, limitRPM, burst, time.Now())
}

// AllowAt applies one request against the tenant's bucket at the supplied
// time. A bucket starts full with capacity max(burst, 1). It refills at
// limitRPM/60 tokens per second; non-positive limits use an effectively zero
// refill rate.
//
// Configuration changes are applied on the next call without discarding the
// bucket: the bucket is first refilled using its previous rate, then its
// capacity and rate are updated and any excess tokens are capped. As a result
// a limit increase never grants retroactive tokens. Clock movement backwards
// neither removes tokens nor adds any.
func (l *Limiter) AllowAt(tenantID string, limitRPM, burst int, now time.Time) Decision {
	capacity := normalizeBurst(burst)
	rate := refillRate(limitRPM)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.buckets == nil {
		l.buckets = make(map[string]*bucket)
	}
	b := l.buckets[tenantID]
	if b == nil {
		b = &bucket{
			tokens:   float64(capacity),
			capacity: capacity,
			rate:     rate,
			last:     now,
		}
		l.buckets[tenantID] = b
	} else {
		b.refill(now)
		b.reconfigure(capacity, rate)
	}

	if b.tokens+availabilityEpsilon >= 1 {
		b.tokens -= 1
		if b.tokens < 0 {
			b.tokens = 0
		}
		return Decision{
			Allowed:    true,
			Limit:      capacity,
			Remaining:  b.remaining(),
			ResetAfter: b.timeToFull(),
		}
	}

	return Decision{
		Allowed:    false,
		Limit:      capacity,
		Remaining:  b.remaining(),
		RetryAfter: durationForTokens(1-b.tokens, b.rate),
		ResetAfter: b.timeToFull(),
	}
}

// Forget drops all state for a tenant. The next request starts with a full
// bucket.
func (l *Limiter) Forget(tenantID string) {
	l.mu.Lock()
	delete(l.buckets, tenantID)
	l.mu.Unlock()
}

// Len reports how many tenant buckets are currently tracked.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// refill adds tokens for time elapsed since the last observation. A backwards
// clock is ignored entirely: no tokens are removed and "last" is left in
// place so that later calls are measured from the latest known time.
func (b *bucket) refill(now time.Time) {
	if now.Before(b.last) {
		return
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	b.last = now

	capF := float64(b.capacity)
	if b.tokens >= capF {
		b.tokens = capF
		return
	}
	b.tokens += elapsed * b.rate
	if b.tokens >= capF {
		b.tokens = capF
	}
}

// reconfigure applies a new capacity and refill rate while preserving the
// bucket's tokens as far as the new capacity allows.
func (b *bucket) reconfigure(capacity int, rate float64) {
	b.capacity = capacity
	capF := float64(capacity)
	if b.tokens > capF {
		b.tokens = capF
	}
	if b.tokens < 0 {
		b.tokens = 0
	}
	b.rate = rate
}

// remaining returns the whole-token remainder for response headers.
func (b *bucket) remaining() int {
	if b.tokens <= 0 {
		return 0
	}
	if b.tokens >= float64(b.capacity) {
		return b.capacity
	}
	return int(math.Floor(b.tokens))
}

// timeToFull returns how long until the bucket reaches capacity.
func (b *bucket) timeToFull() time.Duration {
	return durationForTokens(float64(b.capacity)-b.tokens, b.rate)
}

// normalizeBurst enforces the documented minimum capacity.
func normalizeBurst(burst int) int {
	if burst < 1 {
		return 1
	}
	return burst
}

// refillRate converts requests per minute to tokens per second. Non-positive
// limits get the smallest practical positive rate rather than zero, which
// would produce NaN or an infinite wait.
func refillRate(limitRPM int) float64 {
	if limitRPM < 1 {
		return minRefillRate
	}
	return float64(limitRPM) / 60.0
}

// durationForTokens converts a token deficit into a duration, rounded up to a
// whole millisecond and clamped to the largest representable duration.
func durationForTokens(tokens, rate float64) time.Duration {
	if tokens <= 0 {
		return 0
	}
	if !(rate > 0) || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return maxDuration
	}
	seconds := tokens / rate
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return maxDuration
	}
	if seconds <= 0 {
		return 0
	}
	ms := math.Ceil(seconds * 1000)
	if ms <= 0 {
		return 0
	}
	if ms >= float64(maxDuration/time.Millisecond) {
		return maxDuration
	}
	return time.Duration(ms) * time.Millisecond
}
