package usage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Aswanidev-vs/RateGuard/internal/model"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
)

type captureStore struct {
	store.Store
	mu      sync.Mutex
	entries []model.UsageEntry
}

func (c *captureStore) AddUsage(_ context.Context, entries []model.UsageEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, entries...)
	return nil
}

func (c *captureStore) snapshot() []model.UsageEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.UsageEntry(nil), c.entries...)
}

func TestShutdownFlushesAggregatedUsage(t *testing.T) {
	st := &captureStore{}
	r := New(st, 1000, time.Hour, nil)
	r.Start(context.Background())
	for i := 0; i < 3; i++ {
		r.Record("t1", true, 7*time.Millisecond)
	}
	for i := 0; i < 2; i++ {
		r.Record("t1", false, 120*time.Millisecond)
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	var allowed, blocked, count, sum int64
	hist := make(map[string]int64)
	for _, e := range st.snapshot() {
		allowed += e.Allowed
		blocked += e.Blocked
		count += e.Count
		sum += e.SumLatencyMs
		if e.LatencyBucket != "" {
			hist[e.LatencyBucket] += e.Count
		}
	}
	if allowed != 3 || blocked != 2 || count != 5 {
		t.Fatalf("got allowed=%d blocked=%d count=%d", allowed, blocked, count)
	}
	if sum != 3*7+2*120 {
		t.Fatalf("sum latency = %d", sum)
	}
	if hist["5-9"] != 3 || hist["100-249"] != 2 {
		t.Fatalf("histogram = %v", hist)
	}
}

func TestPeriodicFlushAndDoubleShutdown(t *testing.T) {
	st := &captureStore{}
	r := New(st, 1000, 20*time.Millisecond, nil)
	r.Start(context.Background())
	r.Record("t2", true, time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for len(st.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(st.snapshot()) == 0 {
		t.Fatal("periodic flush did not run")
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}

func TestRecordAfterShutdownDrops(t *testing.T) {
	st := &captureStore{}
	r := New(st, 100, time.Hour, nil)
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := r.Dropped()
	r.Record("t3", true, time.Millisecond)
	if r.Dropped() != before+1 {
		t.Fatal("expected event to be counted as dropped")
	}
}

func TestConcurrentRecord(t *testing.T) {
	st := &captureStore{}
	r := New(st, 100, time.Hour, nil)
	r.Start(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Record("t4", true, time.Millisecond)
			}
		}()
	}
	wg.Wait()
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int64
	for _, e := range st.snapshot() {
		count += e.Count
	}
	if count != 800 {
		t.Fatalf("count = %d, want 800 (dropped=%d)", count, r.Dropped())
	}
}
