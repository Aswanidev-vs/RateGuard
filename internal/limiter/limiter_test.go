package limiter

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testEpoch = time.Unix(1_700_000_000, 0)

func TestAllowExactBurst(t *testing.T) {
	l := New()

	for i := 0; i < 5; i++ {
		d := l.AllowAt("tenant", 60, 5, testEpoch)
		if !d.Allowed {
			t.Fatalf("request %d: expected allowed, got rejected", i+1)
		}
		if d.Limit != 5 {
			t.Fatalf("request %d: Limit = %d, want 5", i+1, d.Limit)
		}
		if want := 4 - i; d.Remaining != want {
			t.Fatalf("request %d: Remaining = %d, want %d", i+1, d.Remaining, want)
		}
		if d.RetryAfter != 0 {
			t.Fatalf("request %d: RetryAfter = %v, want 0 on allow", i+1, d.RetryAfter)
		}
		if i == 4 && d.ResetAfter != 5*time.Second {
			t.Fatalf("last allowed: ResetAfter = %v, want 5s", d.ResetAfter)
		}
	}

	d := l.AllowAt("tenant", 60, 5, testEpoch)
	if d.Allowed {
		t.Fatal("sixth request should be rejected")
	}
	if d.Limit != 5 || d.Remaining != 0 {
		t.Fatalf("rejection: Limit=%d Remaining=%d, want 5 and 0", d.Limit, d.Remaining)
	}
	if d.RetryAfter != time.Second {
		t.Fatalf("RetryAfter = %v, want 1s", d.RetryAfter)
	}
	if d.ResetAfter != 5*time.Second {
		t.Fatalf("ResetAfter = %v, want 5s", d.ResetAfter)
	}
}

func TestRefillMath(t *testing.T) {
	l := New()
	const tenant = "tenant"

	// 120 RPM = 2 tokens per second, capacity 5.
	for i := 0; i < 5; i++ {
		if d := l.AllowAt(tenant, 120, 5, testEpoch); !d.Allowed {
			t.Fatalf("drain request %d rejected", i+1)
		}
	}
	d := l.AllowAt(tenant, 120, 5, testEpoch)
	if d.Allowed {
		t.Fatal("bucket should be empty")
	}
	if d.RetryAfter != 500*time.Millisecond {
		t.Fatalf("RetryAfter = %v, want 500ms", d.RetryAfter)
	}
	if d.ResetAfter != 2500*time.Millisecond {
		t.Fatalf("ResetAfter = %v, want 2500ms", d.ResetAfter)
	}

	// After 1.5s at 2 tokens/s exactly three tokens have accumulated.
	for i := 0; i < 3; i++ {
		d := l.AllowAt(tenant, 120, 5, testEpoch.Add(1500*time.Millisecond))
		if !d.Allowed {
			t.Fatalf("refilled request %d rejected", i+1)
		}
		if want := 2 - i; d.Remaining != want {
			t.Fatalf("refilled request %d: Remaining = %d, want %d", i+1, d.Remaining, want)
		}
	}
	d = l.AllowAt(tenant, 120, 5, testEpoch.Add(1500*time.Millisecond))
	if d.Allowed || d.RetryAfter != 500*time.Millisecond {
		t.Fatalf("expected rejection with 500ms RetryAfter, got %+v", d)
	}

	// A further 250ms yields exactly half a token.
	d = l.AllowAt(tenant, 120, 5, testEpoch.Add(1750*time.Millisecond))
	if d.Allowed {
		t.Fatal("half a token should not be enough")
	}
	if d.RetryAfter != 250*time.Millisecond {
		t.Fatalf("RetryAfter = %v, want 250ms", d.RetryAfter)
	}
	if d.ResetAfter != 2250*time.Millisecond {
		t.Fatalf("ResetAfter = %v, want 2250ms", d.ResetAfter)
	}

	// Another 250ms completes the token.
	d = l.AllowAt(tenant, 120, 5, testEpoch.Add(2*time.Second))
	if !d.Allowed {
		t.Fatal("one full token should be allowed")
	}
	if d.Remaining != 0 {
		t.Fatalf("Remaining = %d, want 0", d.Remaining)
	}
}

func TestRetryAfterRoundsUpToMillisecond(t *testing.T) {
	l := New()
	const tenant = "tenant"

	// 180 RPM = 3 tokens per second.
	if d := l.AllowAt(tenant, 180, 1, testEpoch); !d.Allowed {
		t.Fatal("first request rejected")
	}
	// 100ms gives 0.3 tokens; the remaining 0.7 needs 233.33ms -> 234ms.
	d := l.AllowAt(tenant, 180, 1, testEpoch.Add(100*time.Millisecond))
	if d.Allowed {
		t.Fatal("request with 0.3 tokens should be rejected")
	}
	if d.RetryAfter != 234*time.Millisecond {
		t.Fatalf("RetryAfter = %v, want 234ms", d.RetryAfter)
	}
	if d.ResetAfter != 234*time.Millisecond {
		t.Fatalf("ResetAfter = %v, want 234ms", d.ResetAfter)
	}
}

func TestConfigIncreaseBurstPreservesTokens(t *testing.T) {
	l := New()
	const tenant = "tenant"

	// Drain a capacity-2 bucket at 1 token/s.
	l.AllowAt(tenant, 60, 2, testEpoch)
	l.AllowAt(tenant, 60, 2, testEpoch)

	// One second of refill at the old rate is still only one token, even
	// though the new burst is 5. The capacity increase must not fill it.
	d := l.AllowAt(tenant, 60, 5, testEpoch.Add(time.Second))
	if !d.Allowed || d.Limit != 5 || d.Remaining != 0 {
		t.Fatalf("first post-change decision = %+v, want allowed with Limit 5 and Remaining 0", d)
	}

	// Another second yields one more token at the unchanged refill rate.
	d = l.AllowAt(tenant, 60, 5, testEpoch.Add(2*time.Second))
	if !d.Allowed || d.Remaining != 0 {
		t.Fatalf("second post-change decision = %+v", d)
	}

	// Four seconds now accumulate four tokens (capacity 5).
	at := testEpoch.Add(6 * time.Second)
	for i := 0; i < 4; i++ {
		d = l.AllowAt(tenant, 60, 5, at)
		if !d.Allowed {
			t.Fatalf("token %d after long refill rejected", i+1)
		}
		if want := 3 - i; d.Remaining != want {
			t.Fatalf("token %d: Remaining = %d, want %d", i+1, d.Remaining, want)
		}
	}
	d = l.AllowAt(tenant, 60, 5, at)
	if d.Allowed {
		t.Fatal("bucket should be empty again")
	}
	if d.ResetAfter != 5*time.Second {
		t.Fatalf("ResetAfter = %v, want 5s", d.ResetAfter)
	}
}

func TestConfigDecreaseCapsTokens(t *testing.T) {
	l := New()
	const tenant = "tenant"

	// A fresh capacity-5 bucket. Drop the burst to 2: the excess tokens are
	// discarded immediately rather than on a later refill.
	d := l.AllowAt(tenant, 60, 5, testEpoch)
	if !d.Allowed {
		t.Fatal("initial request rejected")
	}
	d = l.AllowAt(tenant, 60, 2, testEpoch)
	if !d.Allowed || d.Limit != 2 || d.Remaining != 1 {
		t.Fatalf("after capacity decrease = %+v, want allowed Limit 2 Remaining 1", d)
	}
	d = l.AllowAt(tenant, 60, 2, testEpoch)
	if !d.Allowed || d.Remaining != 0 {
		t.Fatalf("second request after decrease = %+v", d)
	}
	d = l.AllowAt(tenant, 60, 2, testEpoch)
	if d.Allowed {
		t.Fatal("capacity-2 bucket should be empty")
	}
	if d.RetryAfter != time.Second || d.ResetAfter != 2*time.Second {
		t.Fatalf("RetryAfter=%v ResetAfter=%v, want 1s and 2s", d.RetryAfter, d.ResetAfter)
	}
}

func TestConfigLimitIncreaseAffectsFutureRefill(t *testing.T) {
	l := New()
	const tenant = "tenant"

	// Empty a capacity-2 bucket at 1 token/s.
	l.AllowAt(tenant, 60, 2, testEpoch)
	l.AllowAt(tenant, 60, 2, testEpoch)

	// Apply 180 RPM (3 tokens/s) at the same instant.
	if d := l.AllowAt(tenant, 180, 2, testEpoch); d.Allowed {
		t.Fatal("empty bucket allowed at config-change instant")
	}

	// 500ms at the new rate gives 1.5 tokens.
	d := l.AllowAt(tenant, 180, 2, testEpoch.Add(500*time.Millisecond))
	if !d.Allowed || d.Remaining != 0 {
		t.Fatalf("after 500ms = %+v, want allowed Remaining 0", d)
	}
	// Another 500ms adds 1.5 tokens, capped at capacity 2.
	d = l.AllowAt(tenant, 180, 2, testEpoch.Add(time.Second))
	if !d.Allowed || d.Remaining != 1 {
		t.Fatalf("after 1s = %+v, want allowed Remaining 1", d)
	}
}

func TestZeroAndNegativeConfiguration(t *testing.T) {
	l := New()

	// burst 0 becomes capacity 1 and the bucket starts full.
	d := l.AllowAt("zero", 0, 0, testEpoch)
	if !d.Allowed || d.Limit != 1 || d.Remaining != 0 {
		t.Fatalf("first zero-config decision = %+v", d)
	}
	if d.ResetAfter <= 0 {
		t.Fatalf("emptied one-token bucket must report a positive ResetAfter, got %v", d.ResetAfter)
	}
	d = l.AllowAt("zero", 0, 0, testEpoch)
	if d.Allowed {
		t.Fatal("second zero-config request should be rejected")
	}
	if d.RetryAfter <= 0 || d.ResetAfter <= 0 {
		t.Fatalf("retry and reset hints must be positive, got %+v", d)
	}
	if d.RetryAfter > maxDuration || d.ResetAfter > maxDuration {
		t.Fatalf("durations must be clamped, got %+v", d)
	}
	// Even an hour later a non-positive RPM has effectively not refilled.
	if d = l.AllowAt("zero", 0, 0, testEpoch.Add(time.Hour)); d.Allowed {
		t.Fatal("zero-RPM bucket refilled unexpectedly")
	}

	// Negative values are normalized the same way.
	d = l.AllowAt("negative", -10, -10, testEpoch)
	if !d.Allowed || d.Limit != 1 {
		t.Fatalf("first negative-config decision = %+v", d)
	}
	if d = l.AllowAt("negative", -10, -10, testEpoch); d.Allowed {
		t.Fatal("second negative-config request should be rejected")
	}
}

func TestBackwardsClockDoesNotRefill(t *testing.T) {
	l := New()
	const tenant = "tenant"

	// Empty a capacity-2 bucket at 1 token/s.
	l.AllowAt(tenant, 60, 2, testEpoch)
	l.AllowAt(tenant, 60, 2, testEpoch)

	// A large backwards jump must not add tokens.
	d := l.AllowAt(tenant, 60, 2, testEpoch.Add(-time.Hour))
	if d.Allowed || d.Remaining != 0 {
		t.Fatalf("backwards clock decision = %+v, want rejected", d)
	}

	// Half a second from the last known time yields half a token.
	d = l.AllowAt(tenant, 60, 2, testEpoch.Add(500*time.Millisecond))
	if d.Allowed {
		t.Fatalf("half token allowed: %+v", d)
	}

	// A full second from the last known time yields the missing token.
	d = l.AllowAt(tenant, 60, 2, testEpoch.Add(time.Second))
	if !d.Allowed || d.Remaining != 0 {
		t.Fatalf("forward recovery decision = %+v, want allowed", d)
	}
}

func TestForwardClockCapsAtCapacity(t *testing.T) {
	l := New()
	const tenant = "tenant"

	// A one-hour jump must not overflow past the burst capacity.
	d := l.AllowAt(tenant, 60, 3, testEpoch.Add(time.Hour))
	if !d.Allowed || d.Remaining != 2 {
		t.Fatalf("first decision = %+v, want allowed Remaining 2", d)
	}
	for i := 0; i < 2; i++ {
		if d = l.AllowAt(tenant, 60, 3, testEpoch.Add(time.Hour)); !d.Allowed {
			t.Fatalf("remaining token %d rejected", i+1)
		}
	}
	if d = l.AllowAt(tenant, 60, 3, testEpoch.Add(time.Hour)); d.Allowed {
		t.Fatal("bucket exceeded its burst capacity")
	}
}

func TestDurationsClamped(t *testing.T) {
	l := New()

	// Only one token is consumed from a billion-token bucket, so the refill
	// time is one token at the minimum rate: one billion seconds.
	d := l.AllowAt("tenant", 0, 1_000_000_000, testEpoch)
	if !d.Allowed {
		t.Fatal("initial request rejected")
	}
	if want := time.Duration(1_000_000_000) * time.Second; d.ResetAfter != want {
		t.Fatalf("ResetAfter = %v, want %v", d.ResetAfter, want)
	}
	if d.Remaining != 999_999_999 {
		t.Fatalf("Remaining = %d, want 999999999", d.Remaining)
	}
	if got := durationForTokens(1e18, minRefillRate); got != maxDuration {
		t.Fatalf("huge token deficit = %v, want clamp to %v", got, maxDuration)
	}
}

func TestConcurrentUse(t *testing.T) {
	l := New()
	const (
		tenant  = "tenant"
		burst   = 37
		callers = 1000
	)

	var allowed int64
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			if l.AllowAt(tenant, 0, burst, testEpoch).Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&allowed); got != burst {
		t.Fatalf("allowed = %d, want %d", got, burst)
	}
	if got := l.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
}

func TestForgetAndLen(t *testing.T) {
	l := New()
	if l.Len() != 0 {
		t.Fatalf("new Limiter Len = %d, want 0", l.Len())
	}

	for _, id := range []string{"a", "b", "c"} {
		if d := l.AllowAt(id, 60, 1, testEpoch); !d.Allowed {
			t.Fatalf("tenant %q rejected", id)
		}
	}
	if l.Len() != 3 {
		t.Fatalf("Len = %d, want 3", l.Len())
	}

	l.Forget("b")
	if l.Len() != 2 {
		t.Fatalf("after Forget Len = %d, want 2", l.Len())
	}
	l.Forget("missing")
	if l.Len() != 2 {
		t.Fatalf("Forget of unknown tenant changed Len to %d", l.Len())
	}

	// A forgotten tenant starts full again even at the same instant.
	if d := l.AllowAt("a", 60, 1, testEpoch); d.Allowed {
		t.Fatal("bucket a should have been drained before Forget")
	}
	l.Forget("a")
	if d := l.AllowAt("a", 60, 1, testEpoch); !d.Allowed {
		t.Fatal("forgotten bucket did not start full")
	}

	l.Forget("a")
	l.Forget("c")
	if l.Len() != 0 {
		t.Fatalf("Len = %d, want 0", l.Len())
	}
}

func TestZeroValueLimiter(t *testing.T) {
	var l Limiter

	if d := l.AllowAt("tenant", 60, 1, testEpoch); !d.Allowed {
		t.Fatal("zero-value Limiter rejected first request")
	}
	if l.Len() != 1 {
		t.Fatalf("Len = %d, want 1", l.Len())
	}
	l.Forget("tenant")
	if l.Len() != 0 {
		t.Fatalf("Len = %d, want 0", l.Len())
	}
}

func TestAllowUsesCurrentTime(t *testing.T) {
	l := New()
	if d := l.Allow("tenant", 60, 1); !d.Allowed {
		t.Fatal("first Allow rejected")
	}
	if d := l.Allow("tenant", 60, 1); d.Allowed {
		t.Fatal("immediate second Allow should be rejected")
	}
}
