package auth

import (
	"sync"
	"testing"
	"time"
)

func TestAttemptLimiter(t *testing.T) {
	l := NewAttemptLimiter(2, time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }
	if !l.Allow("ip") || !l.Allow("ip") || l.Allow("ip") {
		t.Fatal("fixed window limit not enforced")
	}
	if !l.Allow("other") {
		t.Fatal("keys are not independent")
	}
	l.Reset("ip")
	if !l.Allow("ip") {
		t.Fatal("reset did not clear the window")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("ip") {
		t.Fatal("expired window did not reset")
	}
	l.Cleanup(now)
}

func TestAttemptLimiterConcurrent(t *testing.T) {
	l := NewAttemptLimiter(1000, time.Minute)
	var wg sync.WaitGroup
	var allowed sync.Map
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if l.Allow("ip") {
					allowed.Store("ip", true)
				}
			}
		}()
	}
	wg.Wait()
}
