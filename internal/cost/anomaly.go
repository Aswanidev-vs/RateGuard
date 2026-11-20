// Package cost implements RateGuard's AWS spend anomaly detection: the pure
// detection rules used by the scheduled Lambda and the worker that persists
// anomalies and publishes alerts.
package cost

import "math"

const (
	// minHistorySamples is the number of usable historical points required
	// before an evaluation may report an anomaly. Fewer points cannot produce
	// a trustworthy baseline and would cause false positives on cold start.
	minHistorySamples = 3
	// stdDevFactor is the "mean + 2*stddev" trigger multiplier.
	stdDevFactor = 2.0
	// meanFactor is the "1.5*mean" trigger multiplier.
	meanFactor = 1.5
	// noiseFloorFactor suppresses alarms for tiny fluctuations around the
	// baseline (e.g. a few cents on a low bill).
	noiseFloorFactor = 1.25

	// SeverityLow, SeverityMed, and SeverityHigh are the alert severities.
	SeverityLow  = "low"
	SeverityMed  = "med"
	SeverityHigh = "high"
)

// Evaluation is the outcome of comparing a current spend value against
// historical daily spend samples.
type Evaluation struct {
	Anomaly   bool    `json:"anomaly"`
	Baseline  float64 `json:"baseline"`
	StdDev    float64 `json:"stddev"`
	Threshold float64 `json:"threshold"`
	Ratio     float64 `json:"ratio"`
	Severity  string  `json:"severity"`
}

// Evaluate compares current spend with history and reports whether it is an
// anomaly.
//
// History samples that are non-finite (NaN/Inf) or negative are ignored. At
// least three usable samples are required; otherwise Evaluate returns a zero
// Evaluation and does not flag anything. The baseline is the arithmetic mean
// and StdDev is the population standard deviation of the usable samples.
//
// Two PRD triggers are combined: current > mean + 2*stddev or
// current > 1.5*mean. The lower trigger becomes the threshold, which is then
// floored at 1.25*mean to suppress tiny-noise alarms. Anomaly is true only
// when current is strictly greater than the final threshold.
func Evaluate(current float64, history []float64) Evaluation {
	if !validSample(current) {
		return Evaluation{}
	}

	samples := make([]float64, 0, len(history))
	for _, v := range history {
		if validSample(v) {
			samples = append(samples, v)
		}
	}
	if len(samples) < minHistorySamples {
		return Evaluation{}
	}

	baseline, stdDev := meanStdDev(samples)
	threshold := math.Min(baseline+stdDevFactor*stdDev, meanFactor*baseline)
	if floor := baseline * noiseFloorFactor; threshold < floor {
		threshold = floor
	}

	return Evaluation{
		Anomaly:   current > threshold,
		Baseline:  baseline,
		StdDev:    stdDev,
		Threshold: threshold,
		Ratio:     spendRatio(current, baseline),
		Severity:  Severity(current, baseline),
	}
}

// Severity classifies the size of current relative to baseline:
// ratio >= 3 is high, ratio >= 2 is med, and anything else is low. A
// non-positive baseline yields a ratio of zero and therefore low severity.
func Severity(current, baseline float64) string {
	switch ratio := spendRatio(current, baseline); {
	case ratio >= 3:
		return SeverityHigh
	case ratio >= 2:
		return SeverityMed
	default:
		return SeverityLow
	}
}

// validSample reports whether v is a finite, non-negative spend value.
func validSample(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
}

// meanStdDev returns the arithmetic mean and population standard deviation of
// samples. samples must be non-empty.
func meanStdDev(samples []float64) (mean, stdDev float64) {
	var sum float64
	for _, v := range samples {
		sum += v
	}
	mean = sum / float64(len(samples))

	var squaredDiffs float64
	for _, v := range samples {
		d := v - mean
		squaredDiffs += d * d
	}
	return mean, math.Sqrt(squaredDiffs / float64(len(samples)))
}

// spendRatio returns current/baseline, or zero when baseline is non-positive.
func spendRatio(current, baseline float64) float64 {
	if baseline <= 0 {
		return 0
	}
	ratio := current / baseline
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0
	}
	return ratio
}
