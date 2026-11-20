package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Aswanidev-vs/RateGuard/internal/model"
)

const (
	// defaultAnomalyLimit is the page size used when a caller does not request
	// one.
	defaultAnomalyLimit = 50
	// maxAnomalyLimit bounds an anomaly query so a single request cannot load
	// an unbounded number of records.
	maxAnomalyLimit = 200
)

// Memory is an in-process Store implementation. It is safe for concurrent use
// and copies values on every read and write so callers cannot mutate stored
// state through pointers or maps they already hold.
type Memory struct {
	mu        sync.RWMutex
	tenants   map[string]model.Tenant
	usage     map[string]map[string]*memoryUsage
	anomalies map[memoryAnomalyKey]model.Anomaly
}

// memoryUsage is the mutable hourly aggregate held by Memory.
type memoryUsage struct {
	allowed      int64
	blocked      int64
	count        int64
	sumLatencyMs int64
	histogram    map[string]int64
}

// memoryAnomalyKey deduplicates anomalies by tenant and detection time.
type memoryAnomalyKey struct {
	tenantID   string
	detectedAt string
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		tenants:   make(map[string]model.Tenant),
		usage:     make(map[string]map[string]*memoryUsage),
		anomalies: make(map[memoryAnomalyKey]model.Anomaly),
	}
}

var _ Store = (*Memory)(nil)

// CreateTenant stores a copy of t. It returns ErrConflict when the tenant ID
// already exists.
func (m *Memory) CreateTenant(_ context.Context, t *model.Tenant) error {
	if t == nil {
		return errors.New("create tenant: nil tenant")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tenants == nil {
		m.tenants = make(map[string]model.Tenant)
	}
	if _, exists := m.tenants[t.TenantID]; exists {
		return fmt.Errorf("create tenant %q: %w", t.TenantID, ErrConflict)
	}
	m.tenants[t.TenantID] = *t
	return nil
}

// GetTenant returns a copy of the tenant with the given ID. It returns
// ErrNotFound when the tenant does not exist.
func (m *Memory) GetTenant(_ context.Context, id string) (*model.Tenant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tenants[id]
	if !ok {
		return nil, fmt.Errorf("get tenant %q: %w", id, ErrNotFound)
	}
	return cloneTenant(&t), nil
}

// FindTenantsByKeyPrefix returns copies of all tenants whose API key prefix
// begins with prefix.
func (m *Memory) FindTenantsByKeyPrefix(_ context.Context, prefix string) ([]*model.Tenant, error) {
	m.mu.RLock()
	tenants := make([]*model.Tenant, 0)
	for _, t := range m.tenants {
		if strings.HasPrefix(t.APIKeyPrefix, prefix) {
			t := t
			tenants = append(tenants, cloneTenant(&t))
		}
	}
	m.mu.RUnlock()
	sortTenantsNewestFirst(tenants)
	return tenants, nil
}

// ListTenants returns copies of every tenant, newest first.
func (m *Memory) ListTenants(_ context.Context) ([]*model.Tenant, error) {
	m.mu.RLock()
	tenants := make([]*model.Tenant, 0, len(m.tenants))
	for _, t := range m.tenants {
		t := t
		tenants = append(tenants, cloneTenant(&t))
	}
	m.mu.RUnlock()
	sortTenantsNewestFirst(tenants)
	return tenants, nil
}

// UpdateTenant replaces an existing tenant with a copy of t. It returns
// ErrNotFound when the tenant has not been created yet.
func (m *Memory) UpdateTenant(_ context.Context, t *model.Tenant) error {
	if t == nil {
		return errors.New("update tenant: nil tenant")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tenants[t.TenantID]; !exists {
		return fmt.Errorf("update tenant %q: %w", t.TenantID, ErrNotFound)
	}
	m.tenants[t.TenantID] = *t
	return nil
}

// AddUsage atomically applies every entry to its tenant and hourly bucket.
// Histogram counts are incremented by each entry's Count.
func (m *Memory) AddUsage(_ context.Context, entries []model.UsageEntry) error {
	if len(entries) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.usage == nil {
		m.usage = make(map[string]map[string]*memoryUsage)
	}
	for _, e := range entries {
		buckets := m.usage[e.TenantID]
		if buckets == nil {
			buckets = make(map[string]*memoryUsage)
			m.usage[e.TenantID] = buckets
		}
		u := buckets[e.Bucket]
		if u == nil {
			u = &memoryUsage{histogram: make(map[string]int64)}
			buckets[e.Bucket] = u
		}
		u.allowed += e.Allowed
		u.blocked += e.Blocked
		u.count += e.Count
		u.sumLatencyMs += e.SumLatencyMs
		if e.LatencyBucket != "" {
			u.histogram[e.LatencyBucket] += e.Count
		}
	}
	return nil
}

// GetUsage sums the usage buckets for tenantID from fromBucket through
// toBucket, inclusive. Bucket keys use the lexicographically sortable
// YYYY-MM-DDTHH format.
func (m *Memory) GetUsage(_ context.Context, tenantID, fromBucket, toBucket string) (model.UsageAggregate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var agg model.UsageAggregate
	for bucket, u := range m.usage[tenantID] {
		if bucket < fromBucket || bucket > toBucket {
			continue
		}
		agg.Allowed += u.allowed
		agg.Blocked += u.blocked
		agg.Count += u.count
		agg.SumLatencyMs += u.sumLatencyMs
		for name, n := range u.histogram {
			if agg.Histogram == nil {
				agg.Histogram = make(map[string]int64)
			}
			agg.Histogram[name] += n
		}
	}
	return agg, nil
}

// PutAnomalyIfAbsent stores a copy of a unless an anomaly with the same tenant
// and detection time already exists. The boolean reports whether a new record
// was inserted.
func (m *Memory) PutAnomalyIfAbsent(_ context.Context, a *model.Anomaly) (bool, error) {
	if a == nil {
		return false, errors.New("put anomaly: nil anomaly")
	}
	key := memoryAnomalyKey{tenantID: a.TenantID, detectedAt: a.DetectedAt}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.anomalies == nil {
		m.anomalies = make(map[memoryAnomalyKey]model.Anomaly)
	}
	if _, exists := m.anomalies[key]; exists {
		return false, nil
	}
	m.anomalies[key] = *a
	return true, nil
}

// MarkAnomalyAlerted marks the anomaly identified by tenantID and detectedAt
// as alerted. It returns ErrNotFound when no such anomaly exists.
func (m *Memory) MarkAnomalyAlerted(_ context.Context, tenantID, detectedAt string) error {
	key := memoryAnomalyKey{tenantID: tenantID, detectedAt: detectedAt}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.anomalies[key]
	if !ok {
		return fmt.Errorf("mark anomaly alerted (%q, %q): %w", tenantID, detectedAt, ErrNotFound)
	}
	a.Alerted = true
	m.anomalies[key] = a
	return nil
}

// ListAnomalies returns copies of anomalies sorted by detection time
// descending. A non-positive limit uses the default of 50; limits above 200
// are capped.
func (m *Memory) ListAnomalies(_ context.Context, limit int) ([]*model.Anomaly, error) {
	limit = normalizeAnomalyLimit(limit)
	m.mu.RLock()
	anomalies := make([]*model.Anomaly, 0, len(m.anomalies))
	for _, a := range m.anomalies {
		a := a
		anomalies = append(anomalies, cloneAnomaly(&a))
	}
	m.mu.RUnlock()
	sortAnomaliesNewestFirst(anomalies)
	if len(anomalies) > limit {
		anomalies = anomalies[:limit]
	}
	return anomalies, nil
}

// Ping always succeeds for the in-memory store.
func (m *Memory) Ping(context.Context) error { return nil }

// Close releases no resources for the in-memory store.
func (m *Memory) Close() error { return nil }

func cloneTenant(t *model.Tenant) *model.Tenant {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

func cloneAnomaly(a *model.Anomaly) *model.Anomaly {
	if a == nil {
		return nil
	}
	cp := *a
	return &cp
}

// sortTenantsNewestFirst orders tenants by CreatedAt descending. Tenant ID is
// used as a stable tiebreaker.
func sortTenantsNewestFirst(tenants []*model.Tenant) {
	sort.Slice(tenants, func(i, j int) bool {
		if tenants[i].CreatedAt != tenants[j].CreatedAt {
			return tenants[i].CreatedAt > tenants[j].CreatedAt
		}
		return tenants[i].TenantID < tenants[j].TenantID
	})
}

// sortAnomaliesNewestFirst orders anomalies by DetectedAt descending. Tenant
// ID is used as a stable tiebreaker.
func sortAnomaliesNewestFirst(anomalies []*model.Anomaly) {
	sort.Slice(anomalies, func(i, j int) bool {
		if anomalies[i].DetectedAt != anomalies[j].DetectedAt {
			return anomalies[i].DetectedAt > anomalies[j].DetectedAt
		}
		return anomalies[i].TenantID < anomalies[j].TenantID
	})
}

// normalizeAnomalyLimit applies the shared default and maximum list size.
func normalizeAnomalyLimit(limit int) int {
	if limit <= 0 {
		return defaultAnomalyLimit
	}
	if limit > maxAnomalyLimit {
		return maxAnomalyLimit
	}
	return limit
}
