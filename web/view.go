// Package web holds the embedded dashboard assets and view models.
package web

import "github.com/Aswanidev-vs/RateGuard/internal/model"

// LayoutData is shared by every full dashboard page.
type LayoutData struct {
	Title         string
	Active        string
	CSRF          string
	DevMode       bool
	Version       string
	Authenticated bool
}

// Stats is the overview summary.
type Stats struct {
	TenantCount    int
	EnabledTenants int
	Allowed24h     int64
	Blocked24h     int64
	AvgLatencyMs   float64
	P95Ms          int64
	BlockRatePct   float64
	AnomalyCount   int
}

// TenantRow is a tenant plus its trailing 24h usage.
type TenantRow struct {
	Tenant       model.Tenant
	Allowed24h   int64
	Blocked24h   int64
	AvgLatencyMs float64
	P95Ms        int64
}

// OverviewData backs the dashboard landing page.
type OverviewData struct {
	LayoutData
	Stats             Stats
	Tenants           []TenantRow
	Anomalies         []*model.Anomaly
	DemoEnabled       bool
	DemoDefaultTenant string
}

// TenantsData backs the tenant management page.
type TenantsData struct {
	LayoutData
	Tenants           []TenantRow
	DemoEnabled       bool
	DemoDefaultTenant string
}

// AnomaliesData backs the cost anomaly page.
type AnomaliesData struct {
	LayoutData
	Anomalies []*model.Anomaly
}

// CreatedTenantData is the one-time API key reveal partial.
type CreatedTenantData struct {
	Tenant model.Tenant
	APIKey string
}

// UsageCardData backs the HTMX usage card and demo live counters.
type UsageCardData struct {
	TenantID string
	Summary  model.UsageSummary
	Window   string
}

// LoginData backs the admin sign-in page.
type LoginData struct {
	Title         string
	Error         string
	Next          string
	CSRF          string
	Authenticated bool
	DevMode       bool
	Version       string
}

// ErrorData backs the styled error page.
type ErrorData struct {
	LayoutData
	Status  int
	Message string
}
