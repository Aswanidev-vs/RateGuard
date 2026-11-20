package cost

import (
	"math"
	"testing"
)

func TestEvaluateSpikeAgainstFlatHistory(t *testing.T) {
	history := []float64{10, 10, 10, 10, 10, 10, 10}

	ev := Evaluate(50, history)
	if !ev.Anomaly {
		t.Fatalf("expected anomaly, got %+v", ev)
	}
	if ev.Baseline != 10 {
		t.Errorf("baseline = %v, want 10", ev.Baseline)
	}
	if ev.StdDev != 0 {
		t.Errorf("stddev = %v, want 0", ev.StdDev)
	}
	if ev.Threshold != 12.5 {
		t.Errorf("threshold = %v, want 12.5", ev.Threshold)
	}
	if ev.Ratio != 5 {
		t.Errorf("ratio = %v, want 5", ev.Ratio)
	}
	if ev.Severity != SeverityHigh {
		t.Errorf("severity = %q, want %q", ev.Severity, SeverityHigh)
	}
}

func TestEvaluateHighVarianceSuppressesNoise(t *testing.T) {
	// mean 5, population stddev 4, so 1.5*mean (7.5) is the lower trigger and
	// is above the 1.25*mean noise floor (6.25).
	history := []float64{1, 9, 1, 9, 1, 9}

	noise := Evaluate(7, history)
	if noise.Anomaly {
		t.Fatalf("value inside normal variance flagged as anomaly: %+v", noise)
	}
	if noise.Threshold != 7.5 {
		t.Errorf("threshold = %v, want 7.5", noise.Threshold)
	}

	spike := Evaluate(20, history)
	if !spike.Anomaly {
		t.Fatalf("large spike not flagged: %+v", spike)
	}
	if spike.Severity != SeverityHigh {
		t.Errorf("severity = %q, want %q", spike.Severity, SeverityHigh)
	}
}

func TestEvaluateInsufficientHistory(t *testing.T) {
	for _, history := range [][]float64{
		nil,
		{},
		{10},
		{10, 10},
	} {
		ev := Evaluate(1000, history)
		if ev != (Evaluation{}) {
			t.Errorf("history %v: evaluation = %+v, want zero value", history, ev)
		}
	}

	// Exactly three valid samples are enough to evaluate.
	ev := Evaluate(13, []float64{10, 10, 10})
	if !ev.Anomaly {
		t.Fatalf("expected anomaly with three samples, got %+v", ev)
	}
}

func TestEvaluateIgnoresInvalidHistorySamples(t *testing.T) {
	history := []float64{math.NaN(), math.Inf(1), math.Inf(-1), -5, 10, 10, 10}

	ev := Evaluate(20, history)
	if !ev.Anomaly {
		t.Fatalf("expected anomaly, got %+v", ev)
	}
	if ev.Baseline != 10 {
		t.Errorf("baseline = %v, want 10 (invalid samples filtered)", ev.Baseline)
	}
	if ev.StdDev != 0 {
		t.Errorf("stddev = %v, want 0", ev.StdDev)
	}

	// Once filtering leaves fewer than three samples, evaluation is skipped.
	if got := Evaluate(20, []float64{math.NaN(), -1, 10, 10}); got != (Evaluation{}) {
		t.Errorf("evaluation = %+v, want zero value", got)
	}

	allInvalid := []float64{math.NaN(), math.Inf(1), -1}
	if got := Evaluate(100, allInvalid); got != (Evaluation{}) {
		t.Errorf("all-invalid history evaluation = %+v, want zero value", got)
	}
}

func TestEvaluateRejectsInvalidCurrent(t *testing.T) {
	history := []float64{10, 10, 10}
	for _, current := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01} {
		if got := Evaluate(current, history); got != (Evaluation{}) {
			t.Errorf("current %v: evaluation = %+v, want zero value", current, got)
		}
	}
}

func TestEvaluateExactBoundary(t *testing.T) {
	// Flat history gives threshold = 1.25*mean = 12.5 (floored above the
	// unfloored lower trigger of 10).
	history := []float64{10, 10, 10}

	atBoundary := Evaluate(12.5, history)
	if atBoundary.Anomaly {
		t.Fatalf("value equal to threshold flagged: %+v", atBoundary)
	}
	if atBoundary.Threshold != 12.5 {
		t.Errorf("threshold = %v, want 12.5", atBoundary.Threshold)
	}

	justAbove := Evaluate(12.500001, history)
	if !justAbove.Anomaly {
		t.Fatalf("value just above threshold not flagged: %+v", justAbove)
	}
}

func TestEvaluateUnflooredBoundary(t *testing.T) {
	// mean 10, stddev ~8.165: 1.5*mean (15) is lower than mean+2*stddev and
	// above the 12.5 noise floor.
	history := []float64{0, 10, 20}

	ev := Evaluate(15, history)
	if math.Abs(ev.Threshold-15) > 1e-9 {
		t.Fatalf("threshold = %v, want 15", ev.Threshold)
	}
	if ev.Anomaly {
		t.Fatalf("value equal to threshold flagged: %+v", ev)
	}

	if !Evaluate(15.1, history).Anomaly {
		t.Fatal("value above threshold not flagged")
	}
}

func TestSeverity(t *testing.T) {
	tests := []struct {
		current  float64
		baseline float64
		want     string
	}{
		{30, 10, SeverityHigh},
		{29.99, 10, SeverityMed},
		{20, 10, SeverityMed},
		{19.99, 10, SeverityLow},
		{15, 10, SeverityLow},
		{0, 10, SeverityLow},
		{10, 0, SeverityLow},
		{10, -1, SeverityLow},
	}
	for _, tt := range tests {
		if got := Severity(tt.current, tt.baseline); got != tt.want {
			t.Errorf("Severity(%v, %v) = %q, want %q", tt.current, tt.baseline, got, tt.want)
		}
	}
}
