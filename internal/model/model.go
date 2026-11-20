// Package model contains the shared domain types for RateGuard.
package model

import (
	"math"
	"time"
)

// AccountTenantID is the reserved tenant used for account-wide AWS billing
// anomalies. AWS cost data is account-scoped, not tenant-scoped.
const AccountTenantID = "__account__"

// Tenant is a RateGuard customer. APIKeyHash is never serialized.
type Tenant struct {
	TenantID     string `json:"tenant_id" dynamodbav:"tenant_id"`
	Name         string `json:"name" dynamodbav:"name"`
	Email        string `json:"email" dynamodbav:"email"`
	APIKeyHash   string `json:"-" dynamodbav:"api_key_hash"`
	APIKeyPrefix string `json:"api_key_prefix" dynamodbav:"api_key_prefix"`
	LimitRPM     int    `json:"limit_rpm" dynamodbav:"limit_rpm"`
	Burst        int    `json:"burst" dynamodbav:"burst"`
	Enabled      bool   `json:"enabled" dynamodbav:"enabled"`
	CreatedAt    string `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt    string `json:"updated_at" dynamodbav:"updated_at"`
}

// CreateTenantRequest is the admin API payload for creating a tenant.
type CreateTenantRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	LimitRPM int    `json:"limit_rpm"`
	Burst    int    `json:"burst"`
}

// UpdateTenantRequest is the admin API payload for patching a tenant.
type UpdateTenantRequest struct {
	LimitRPM *int  `json:"limit_rpm"`
	Burst    *int  `json:"burst"`
	Enabled  *bool `json:"enabled"`
}

// UsageDelta is an increment applied to one hourly usage bucket.
type UsageDelta struct {
	Allowed       int64
	Blocked       int64
	Count         int64
	SumLatencyMs  int64
	LatencyBucket string
}

// UsageEntry is a single aggregated write for a tenant/hour pair.
type UsageEntry struct {
	TenantID string
	Bucket   string
	UsageDelta
}

// UsageAggregate is the stored, multi-hour aggregation for a tenant.
type UsageAggregate struct {
	Allowed      int64            `json:"allowed"`
	Blocked      int64            `json:"blocked"`
	Count        int64            `json:"count"`
	SumLatencyMs int64            `json:"sum_latency_ms"`
	Histogram    map[string]int64 `json:"latency_histogram"`
}

// UsageSummary is the public /usage response shape.
type UsageSummary struct {
	Allowed      int64   `json:"allowed"`
	Blocked      int64   `json:"blocked"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	P95Ms        int64   `json:"p95_ms"`
}

// Summary derives average and approximate p95 latency from a fixed histogram.
func (a UsageAggregate) Summary() UsageSummary {
	s := UsageSummary{Allowed: a.Allowed, Blocked: a.Blocked}
	if a.Count > 0 {
		s.AvgLatencyMs = math.Round(float64(a.SumLatencyMs)/float64(a.Count)*100) / 100
	}
	s.P95Ms = HistogramPercentile(a.Histogram, 0.95)
	return s
}

// Anomaly is an AWS spend anomaly record.
type Anomaly struct {
	TenantID   string  `json:"tenant_id" dynamodbav:"tenant_id"`
	DetectedAt string  `json:"detected_at" dynamodbav:"detected_at"`
	Metric     string  `json:"metric" dynamodbav:"metric"`
	Value      float64 `json:"value" dynamodbav:"value"`
	Baseline   float64 `json:"baseline" dynamodbav:"baseline"`
	Threshold  float64 `json:"threshold" dynamodbav:"threshold"`
	Severity   string  `json:"severity" dynamodbav:"severity"`
	Alerted    bool    `json:"alerted" dynamodbav:"alerted"`
}

// Latency bucket names and upper bounds. The final bucket is open-ended.
var LatencyBuckets = []struct {
	Name  string
	Upper int64
}{
	{"0-4", 4},
	{"5-9", 9},
	{"10-19", 19},
	{"20-49", 49},
	{"50-99", 99},
	{"100-249", 249},
	{"250-499", 499},
	{"500-999", 999},
	{"1000+", -1},
}

// LatencyBucket returns the fixed histogram bucket for a latency in
// milliseconds.
func LatencyBucket(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	for _, b := range LatencyBuckets {
		if b.Upper >= 0 && ms <= b.Upper {
			return b.Name
		}
	}
	return LatencyBuckets[len(LatencyBuckets)-1].Name
}

// HistogramPercentile returns the upper bound of the bucket containing the
// requested percentile. It is an approximation by design: exact percentiles
// would require unbounded per-request storage.
func HistogramPercentile(hist map[string]int64, p float64) int64 {
	if p <= 0 || p > 1 || len(hist) == 0 {
		return 0
	}
	var total int64
	for _, n := range hist {
		total += n
	}
	if total == 0 {
		return 0
	}
	target := int64(math.Ceil(float64(total) * p))
	var seen int64
	for _, b := range LatencyBuckets {
		seen += hist[b.Name]
		if seen >= target {
			if b.Upper < 0 {
				return 1000
			}
			return b.Upper
		}
	}
	return 1000
}

// NowISO returns the current UTC time in RFC3339 format.
func NowISO() string { return time.Now().UTC().Format(time.RFC3339) }

// BucketFor returns the hour bucket key for a time.
func BucketFor(t time.Time) string { return t.UTC().Format("2006-01-02T15") }

// ParseBucket parses a YYYY-MM-DDTHH bucket key.
func ParseBucket(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02T15", s, time.UTC)
}
