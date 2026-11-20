package cost

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Aswanidev-vs/RateGuard/internal/model"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
)

// fakeStore implements only the anomaly methods used by Worker; the embedded
// nil interface satisfies the rest of store.Store and would panic if the
// worker unexpectedly touched them.
type fakeStore struct {
	store.Store

	inserted bool
	putErr   error
	markErr  error

	putCalls   int
	markCalls  int
	putAnomaly *model.Anomaly
	markTenant string
	markAt     string
}

func (f *fakeStore) PutAnomalyIfAbsent(_ context.Context, a *model.Anomaly) (bool, error) {
	f.putCalls++
	f.putAnomaly = a
	return f.inserted, f.putErr
}

func (f *fakeStore) MarkAnomalyAlerted(_ context.Context, tenantID, detectedAt string) error {
	f.markCalls++
	f.markTenant = tenantID
	f.markAt = detectedAt
	return f.markErr
}

type fakeSource struct {
	values []float64
	err    error

	calls int
	days  int
}

func (f *fakeSource) History(_ context.Context, days int) ([]float64, error) {
	f.calls++
	f.days = days
	if f.err != nil {
		return nil, f.err
	}
	return append([]float64(nil), f.values...), nil
}

type fakeNotifier struct {
	err error

	calls   int
	subject string
	message string
}

func (f *fakeNotifier) Publish(_ context.Context, subject, message string) error {
	f.calls++
	f.subject = subject
	f.message = message
	return f.err
}

var fixedNow = func() time.Time {
	return time.Date(2026, 11, 20, 15, 42, 33, 0, time.UTC)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestWorker(src *fakeSource, st *fakeStore, notifier Notifier) *Worker {
	return &Worker{
		Store:    st,
		Source:   src,
		Notifier: notifier,
		Log:      testLogger(),
		Metric:   DefaultMetric,
		Now:      fixedNow,
	}
}

func TestWorkerAnomalyRecordsAndAlerts(t *testing.T) {
	src := &fakeSource{values: []float64{10, 10, 10, 10, 10, 10, 10, 40}}
	st := &fakeStore{inserted: true}
	notifier := &fakeNotifier{}
	w := newTestWorker(src, st, notifier)

	got, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !got.Checked {
		t.Error("Checked = false, want true")
	}
	if got.Current != 40 {
		t.Errorf("Current = %v, want 40", got.Current)
	}
	if !got.Evaluation.Anomaly {
		t.Errorf("Evaluation.Anomaly = false, want true (%+v)", got.Evaluation)
	}
	if !got.Recorded {
		t.Error("Recorded = false, want true")
	}
	if !got.Alerted {
		t.Error("Alerted = false, want true")
	}

	if src.calls != 1 || src.days != BillingHistoryDays {
		t.Errorf("source calls = %d days = %d, want 1 call with %d days", src.calls, src.days, BillingHistoryDays)
	}
	if st.putCalls != 1 {
		t.Fatalf("PutAnomalyIfAbsent calls = %d, want 1", st.putCalls)
	}
	a := st.putAnomaly
	if a == nil {
		t.Fatal("no anomaly passed to store")
	}
	if a.TenantID != model.AccountTenantID {
		t.Errorf("TenantID = %q, want %q", a.TenantID, model.AccountTenantID)
	}
	if a.DetectedAt != "2026-11-20T15:00:00Z" {
		t.Errorf("DetectedAt = %q, want 2026-11-20T15:00:00Z", a.DetectedAt)
	}
	if a.Metric != DefaultMetric {
		t.Errorf("Metric = %q, want %q", a.Metric, DefaultMetric)
	}
	if a.Value != 40 || a.Baseline != 10 || a.Threshold != 12.5 {
		t.Errorf("anomaly values = %+v, want value 40 baseline 10 threshold 12.5", a)
	}
	if a.Severity != SeverityHigh {
		t.Errorf("Severity = %q, want %q", a.Severity, SeverityHigh)
	}
	if a.Alerted {
		t.Error("Alerted persisted as true before publish")
	}

	if st.markCalls != 1 {
		t.Fatalf("MarkAnomalyAlerted calls = %d, want 1", st.markCalls)
	}
	if st.markTenant != model.AccountTenantID || st.markAt != "2026-11-20T15:00:00Z" {
		t.Errorf("mark args = (%q, %q), want (%q, %q)",
			st.markTenant, st.markAt, model.AccountTenantID, "2026-11-20T15:00:00Z")
	}

	if notifier.calls != 1 {
		t.Fatalf("notifier calls = %d, want 1", notifier.calls)
	}
	if notifier.subject != AlertSubject {
		t.Errorf("subject = %q, want %q", notifier.subject, AlertSubject)
	}
	for _, want := range []string{
		"Current: 40.00 USD",
		"Baseline: 10.00 USD",
		"Threshold: 12.50 USD",
		"Severity: high",
		"2026-11-20T15:00:00Z",
	} {
		if !strings.Contains(notifier.message, want) {
			t.Errorf("message missing %q:\n%s", want, notifier.message)
		}
	}
}

func TestWorkerNoAnomaly(t *testing.T) {
	src := &fakeSource{values: []float64{10, 10, 10, 10, 10, 10, 10, 10}}
	st := &fakeStore{inserted: true}
	notifier := &fakeNotifier{}
	w := newTestWorker(src, st, notifier)

	got, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !got.Checked {
		t.Error("Checked = false, want true")
	}
	if got.Recorded || got.Alerted {
		t.Errorf("Recorded/Alerted = %v/%v, want false/false", got.Recorded, got.Alerted)
	}
	if st.putCalls != 0 || notifier.calls != 0 {
		t.Errorf("put calls = %d, notifier calls = %d, want 0/0", st.putCalls, notifier.calls)
	}
}

func TestWorkerInsufficientHistory(t *testing.T) {
	src := &fakeSource{values: []float64{9, 10, 12}}
	st := &fakeStore{inserted: true}
	w := newTestWorker(src, st, &fakeNotifier{})

	got, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Checked {
		t.Error("Checked = true, want false")
	}
	if st.putCalls != 0 {
		t.Errorf("PutAnomalyIfAbsent calls = %d, want 0", st.putCalls)
	}
}

func TestWorkerAcceptsExactlyFourPoints(t *testing.T) {
	src := &fakeSource{values: []float64{10, 10, 10, 50}}
	st := &fakeStore{inserted: true}
	w := newTestWorker(src, st, &fakeNotifier{})

	got, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !got.Checked || !got.Recorded {
		t.Errorf("Checked/Recorded = %v/%v, want true/true", got.Checked, got.Recorded)
	}
}

func TestWorkerDeduplicatesByHour(t *testing.T) {
	src := &fakeSource{values: []float64{10, 10, 10, 10, 10, 10, 10, 40}}
	st := &fakeStore{inserted: false}
	notifier := &fakeNotifier{}
	w := newTestWorker(src, st, notifier)

	got, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.putCalls != 1 {
		t.Errorf("PutAnomalyIfAbsent calls = %d, want 1", st.putCalls)
	}
	if got.Recorded {
		t.Error("Recorded = true for deduplicated anomaly, want false")
	}
	if got.Alerted {
		t.Error("Alerted = true for deduplicated anomaly, want false")
	}
	if notifier.calls != 0 {
		t.Errorf("notifier calls = %d, want 0", notifier.calls)
	}
	if st.markCalls != 0 {
		t.Errorf("MarkAnomalyAlerted calls = %d, want 0", st.markCalls)
	}
}

func TestWorkerPublishFailureKeepsRecord(t *testing.T) {
	src := &fakeSource{values: []float64{10, 10, 10, 10, 10, 10, 10, 40}}
	st := &fakeStore{inserted: true}
	notifier := &fakeNotifier{err: errors.New("sns down")}
	w := newTestWorker(src, st, notifier)

	got, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run error = nil, want publish error")
	}
	if !strings.Contains(err.Error(), "sns down") {
		t.Errorf("error = %v, want wrapped sns error", err)
	}
	if !got.Recorded {
		t.Error("Recorded = false, want true (anomaly must remain)")
	}
	if got.Alerted {
		t.Error("Alerted = true, want false")
	}
	if st.putCalls != 1 {
		t.Errorf("PutAnomalyIfAbsent calls = %d, want 1", st.putCalls)
	}
	if st.markCalls != 0 {
		t.Errorf("MarkAnomalyAlerted calls = %d, want 0", st.markCalls)
	}
}

func TestWorkerMarkAlertedFailure(t *testing.T) {
	src := &fakeSource{values: []float64{10, 10, 10, 10, 10, 10, 10, 40}}
	st := &fakeStore{inserted: true, markErr: errors.New("ddb down")}
	notifier := &fakeNotifier{}
	w := newTestWorker(src, st, notifier)

	got, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run error = nil, want mark error")
	}
	if !got.Recorded || !got.Alerted {
		t.Errorf("Recorded/Alerted = %v/%v, want true/true", got.Recorded, got.Alerted)
	}
	if notifier.calls != 1 {
		t.Errorf("notifier calls = %d, want 1", notifier.calls)
	}
}

func TestWorkerWithoutNotifierRecordsOnly(t *testing.T) {
	src := &fakeSource{values: []float64{10, 10, 10, 10, 10, 10, 10, 40}}
	st := &fakeStore{inserted: true}
	w := newTestWorker(src, st, nil)

	got, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !got.Recorded {
		t.Error("Recorded = false, want true")
	}
	if got.Alerted {
		t.Error("Alerted = true, want false")
	}
	if st.markCalls != 0 {
		t.Errorf("MarkAnomalyAlerted calls = %d, want 0", st.markCalls)
	}
}

func TestWorkerSourceError(t *testing.T) {
	src := &fakeSource{err: errors.New("cloudwatch down")}
	w := newTestWorker(src, &fakeStore{inserted: true}, &fakeNotifier{})

	if _, err := w.Run(context.Background()); err == nil {
		t.Fatal("Run error = nil, want source error")
	}
}

func TestWorkerPutError(t *testing.T) {
	src := &fakeSource{values: []float64{10, 10, 10, 10, 10, 10, 10, 40}}
	st := &fakeStore{inserted: true, putErr: errors.New("ddb down")}
	notifier := &fakeNotifier{}
	w := newTestWorker(src, st, notifier)

	got, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run error = nil, want put error")
	}
	if got.Recorded || got.Alerted {
		t.Errorf("Recorded/Alerted = %v/%v, want false/false", got.Recorded, got.Alerted)
	}
	if notifier.calls != 0 {
		t.Errorf("notifier calls = %d, want 0", notifier.calls)
	}
}

func TestWorkerTrimsToLastEightPoints(t *testing.T) {
	// Leading outliers must not alter the seven-day baseline.
	src := &fakeSource{values: []float64{1000, 2000, 10, 10, 10, 10, 10, 10, 10, 40}}
	st := &fakeStore{inserted: true}
	w := newTestWorker(src, st, &fakeNotifier{})

	got, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !got.Evaluation.Anomaly {
		t.Errorf("Evaluation = %+v, want anomaly over last eight points", got.Evaluation)
	}
	if got.Evaluation.Baseline != 10 {
		t.Errorf("Baseline = %v, want 10", got.Evaluation.Baseline)
	}
}

func TestWorkerNilDependencies(t *testing.T) {
	if _, err := (&Worker{}).Run(context.Background()); err == nil {
		t.Error("nil worker: error = nil, want error")
	}
	if _, err := (&Worker{Source: &fakeSource{}}).Run(context.Background()); err == nil {
		t.Error("nil store: error = nil, want error")
	}
	if _, err := (&Worker{Store: &fakeStore{}}).Run(context.Background()); err == nil {
		t.Error("nil source: error = nil, want error")
	}
}

func TestWorkerDetectedAtUsesUTC(t *testing.T) {
	src := &fakeSource{values: []float64{10, 10, 10, 10, 10, 10, 10, 40}}
	st := &fakeStore{inserted: true}
	w := newTestWorker(src, st, &fakeNotifier{})
	ist := time.FixedZone("IST", 5*3600+1800)
	w.Now = func() time.Time {
		return time.Date(2026, 11, 20, 21, 42, 33, 0, ist) // 16:12:33 UTC
	}

	if _, err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := st.putAnomaly.DetectedAt, "2026-11-20T16:00:00Z"; got != want {
		t.Errorf("DetectedAt = %q, want %q", got, want)
	}
}
