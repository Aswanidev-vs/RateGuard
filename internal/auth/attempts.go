package auth

import (
	"sync"
	"time"
)

const maxAttemptKeys = 8192

type attemptWindow struct {
	count   int
	resetAt time.Time
}

// AttemptLimiter is a bounded fixed-window counter keyed by client IP. It is
// used to slow admin-key and dashboard-login brute force attempts.
type AttemptLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	windows map[string]*attemptWindow
}

// NewAttemptLimiter allows max attempts per window per key.
func NewAttemptLimiter(max int, window time.Duration) *AttemptLimiter {
	if max < 1 {
		max = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &AttemptLimiter{
		max:     max,
		window:  window,
		now:     time.Now,
		windows: make(map[string]*attemptWindow),
	}
}

// Allow records an attempt and reports whether it is within the limit.
func (l *AttemptLimiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.windows) >= maxAttemptKeys {
		l.cleanupLocked(now)
	}
	w := l.windows[key]
	if w == nil || !now.Before(w.resetAt) {
		w = &attemptWindow{resetAt: now.Add(l.window)}
		l.windows[key] = w
	}
	w.count++
	return w.count <= l.max
}

// Reset clears the counter for key, typically after a successful login.
func (l *AttemptLimiter) Reset(key string) {
	l.mu.Lock()
	delete(l.windows, key)
	l.mu.Unlock()
}

// Cleanup drops expired windows.
func (l *AttemptLimiter) Cleanup(now time.Time) {
	l.mu.Lock()
	l.cleanupLocked(now)
	l.mu.Unlock()
}

func (l *AttemptLimiter) cleanupLocked(now time.Time) {
	for key, w := range l.windows {
		if !now.Before(w.resetAt) {
			delete(l.windows, key)
		}
	}
}
