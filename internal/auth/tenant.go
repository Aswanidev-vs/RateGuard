package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Aswanidev-vs/RateGuard/internal/model"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
)

var (
	// ErrInvalidKey means no enabled tenant matched the presented key.
	ErrInvalidKey = errors.New("invalid API key")
	// ErrDisabled means the key matched a tenant that has been soft-deleted.
	ErrDisabled = errors.New("tenant is disabled")
)

const maxCacheEntries = 4096

type cacheEntry struct {
	tenant  model.Tenant
	expires time.Time
	seq     uint64
}

// TenantAuthenticator resolves tenant API keys with a bounded TTL cache. The
// cache stores only hashes and tenant records; raw keys are never retained.
type TenantAuthenticator struct {
	st  store.Store
	ttl time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
	seq   uint64
	now   func() time.Time
}

// NewTenantAuthenticator creates an authenticator backed by st.
func NewTenantAuthenticator(st store.Store, ttl time.Duration) *TenantAuthenticator {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &TenantAuthenticator{
		st:    st,
		ttl:   ttl,
		cache: make(map[string]cacheEntry),
		now:   time.Now,
	}
}

// Authenticate resolves raw to its tenant. It never logs the raw key.
func (a *TenantAuthenticator) Authenticate(ctx context.Context, raw string) (*model.Tenant, error) {
	if err := validateKeyShape(raw); err != nil {
		return nil, err
	}
	hash := Hash(raw)
	now := a.now()

	if t, ok, disabled := a.fromCache(hash, now); ok {
		if disabled {
			return nil, ErrDisabled
		}
		return &t, nil
	}

	prefix := Prefix(raw)
	tenants, err := a.st.FindTenantsByKeyPrefix(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("look up API key prefix: %w", err)
	}
	for _, t := range tenants {
		if t == nil || !Verify(raw, t.APIKeyHash) {
			continue
		}
		a.cacheResult(hash, *t, now)
		if !t.Enabled {
			return nil, ErrDisabled
		}
		return t, nil
	}
	return nil, ErrInvalidKey
}

// Invalidate removes cached state for tenantID after a config or status
// change so limit updates and disablement take effect immediately.
func (a *TenantAuthenticator) Invalidate(tenantID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for hash, entry := range a.cache {
		if entry.tenant.TenantID == tenantID {
			delete(a.cache, hash)
		}
	}
}

func validateKeyShape(raw string) error {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return ErrInvalidKey
	}
	if !strings.HasPrefix(raw, KeyPrefix) || len(raw) < len(KeyPrefix)+prefixSecretLen {
		return ErrInvalidKey
	}
	return nil
}

func (a *TenantAuthenticator) fromCache(hash string, now time.Time) (model.Tenant, bool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.cache[hash]
	if !ok {
		return model.Tenant{}, false, false
	}
	if !now.Before(entry.expires) {
		delete(a.cache, hash)
		return model.Tenant{}, false, false
	}
	return entry.tenant, true, !entry.tenant.Enabled
}

func (a *TenantAuthenticator) cacheResult(hash string, tenant model.Tenant, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) >= maxCacheEntries {
		a.evictOldestLocked(now)
	}
	a.seq++
	a.cache[hash] = cacheEntry{tenant: tenant, expires: now.Add(a.ttl), seq: a.seq}
}

func (a *TenantAuthenticator) evictOldestLocked(now time.Time) {
	var oldestKey string
	var oldestSeq uint64 = ^uint64(0)
	for key, entry := range a.cache {
		if !now.Before(entry.expires) {
			delete(a.cache, key)
			return
		}
		if entry.seq < oldestSeq {
			oldestSeq = entry.seq
			oldestKey = key
		}
	}
	if oldestKey != "" {
		delete(a.cache, oldestKey)
	}
}

func (a *TenantAuthenticator) cacheLen() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.cache)
}
