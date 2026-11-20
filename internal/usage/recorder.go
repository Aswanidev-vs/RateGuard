// Package usage buffers per-request usage events and flushes them to the
// configured store in batches. Recording is non-blocking so rate-limited
// traffic is never delayed by DynamoDB writes.
package usage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Aswanidev-vs/RateGuard/internal/model"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
)

const (
	eventBufferSize = 8192
	flushTimeout    = 5 * time.Second
)

type event struct {
	tenantID string
	allowed  bool
	latency  time.Duration
	at       time.Time
}

type aggKey struct {
	tenantID string
	bucket   string
}

type aggregate struct {
	allowed int64
	blocked int64
	sumMs   int64
	hist    map[string]int64
}

// Recorder aggregates usage events and writes them in batches.
type Recorder struct {
	st        store.Store
	batchSize int
	interval  time.Duration
	log       *slog.Logger

	events  chan event
	dropped atomic.Uint64
	closed  atomic.Uint32

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// New creates a Recorder.
func New(st store.Store, batchSize int, interval time.Duration, log *slog.Logger) *Recorder {
	if batchSize < 1 {
		batchSize = 100
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{
		st:        st,
		batchSize: batchSize,
		interval:  interval,
		log:       log,
		events:    make(chan event, eventBufferSize),
		done:      make(chan struct{}),
	}
}

// Start launches the background flush loop. It is safe to call once.
func (r *Recorder) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return
	}
	r.started = true
	loopCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	go r.loop(loopCtx)
}

// Record enqueues one request outcome. It never blocks; events are dropped
// and counted when the bounded buffer is full.
func (r *Recorder) Record(tenantID string, allowed bool, latency time.Duration) {
	if r.closed.Load() == 1 {
		r.dropped.Add(1)
		return
	}
	e := event{tenantID: tenantID, allowed: allowed, latency: latency, at: time.Now()}
	select {
	case r.events <- e:
	default:
		r.dropped.Add(1)
	}
}

// Dropped reports the number of events dropped since startup.
func (r *Recorder) Dropped() uint64 { return r.dropped.Load() }

// Shutdown stops the background loop, drains queued events, and performs a
// final flush. It is idempotent.
func (r *Recorder) Shutdown(ctx context.Context) error {
	r.closed.Store(1)
	r.mu.Lock()
	cancel := r.cancel
	started := r.started
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		select {
		case <-r.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.flushQueue(ctx)
}

func (r *Recorder) loop(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	pending := make(map[aggKey]*aggregate)
	count := 0

	flush := func() {
		if count == 0 {
			return
		}
		flushCtx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		if err := r.flushAggregates(flushCtx, pending); err != nil {
			r.log.Error("flush usage", "error", err)
		}
		cancel()
		pending = make(map[aggKey]*aggregate)
		count = 0
	}

	for {
		select {
		case e := <-r.events:
			addEvent(pending, e)
			count++
			if count >= r.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			flush()
			return
		}
	}
}

// flushQueue drains whatever is still queued and writes it. It is used by
// Shutdown after the background loop has exited.
func (r *Recorder) flushQueue(ctx context.Context) error {
	pending := make(map[aggKey]*aggregate)
	for {
		select {
		case e := <-r.events:
			addEvent(pending, e)
		default:
			return r.flushAggregates(ctx, pending)
		}
	}
}

func addEvent(pending map[aggKey]*aggregate, e event) {
	key := aggKey{tenantID: e.tenantID, bucket: model.BucketFor(e.at)}
	a := pending[key]
	if a == nil {
		a = &aggregate{hist: make(map[string]int64)}
		pending[key] = a
	}
	ms := e.latency.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	if e.allowed {
		a.allowed++
	} else {
		a.blocked++
	}
	a.sumMs += ms
	bucket := model.LatencyBucket(ms)
	a.hist[bucket]++
}

func (r *Recorder) flushAggregates(ctx context.Context, pending map[aggKey]*aggregate) error {
	if len(pending) == 0 {
		return nil
	}
	entries := make([]model.UsageEntry, 0, len(pending)*2)
	for key, a := range pending {
		entries = append(entries, model.UsageEntry{
			TenantID: key.tenantID,
			Bucket:   key.bucket,
			UsageDelta: model.UsageDelta{
				Allowed:      a.allowed,
				Blocked:      a.blocked,
				SumLatencyMs: a.sumMs,
			},
		})
		for bucket, n := range a.hist {
			entries = append(entries, model.UsageEntry{
				TenantID: key.tenantID,
				Bucket:   key.bucket,
				UsageDelta: model.UsageDelta{
					Count:         n,
					LatencyBucket: bucket,
				},
			})
		}
	}
	return r.st.AddUsage(ctx, entries)
}
