package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aswanidev-vs/RateGuard/internal/model"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
)

type fakeKeyStore struct {
	store.Store
	tenants []*model.Tenant
	calls   int
	err     error
}

func (f *fakeKeyStore) FindTenantsByKeyPrefix(_ context.Context, _ string) ([]*model.Tenant, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.tenants, nil
}

func tenantWithKey(t *testing.T, enabled bool) (*model.Tenant, string) {
	t.Helper()
	creds, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	return &model.Tenant{
		TenantID:     "tn_test",
		APIKeyHash:   creds.Hash,
		APIKeyPrefix: creds.Prefix,
		Enabled:      enabled,
	}, creds.Raw
}

func TestTenantAuthenticateAndCache(t *testing.T) {
	tenant, raw := tenantWithKey(t, true)
	fake := &fakeKeyStore{tenants: []*model.Tenant{tenant}}
	a := NewTenantAuthenticator(fake, time.Minute)
	got, err := a.Authenticate(context.Background(), raw)
	if err != nil || got.TenantID != "tn_test" {
		t.Fatalf("authenticate: %v %v", got, err)
	}
	if _, err := a.Authenticate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || a.cacheLen() != 1 {
		t.Fatalf("cache not used: calls=%d cache=%d", fake.calls, a.cacheLen())
	}
	a.Invalidate("tn_test")
	if a.cacheLen() != 0 {
		t.Fatal("invalidate did not clear cache")
	}
}

func TestTenantAuthenticateInvalidDisabledAndErrors(t *testing.T) {
	tenant, raw := tenantWithKey(t, false)
	fake := &fakeKeyStore{tenants: []*model.Tenant{tenant}}
	a := NewTenantAuthenticator(fake, time.Minute)
	if _, err := a.Authenticate(context.Background(), raw); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	fake.err = errors.New("boom")
	if _, err := a.Authenticate(context.Background(), raw); err == nil {
		t.Fatal("store error swallowed")
	}
	fake.err = nil
	if _, err := a.Authenticate(context.Background(), "ag_live_short"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("short key: %v", err)
	}
	if _, err := a.Authenticate(context.Background(), raw+" "); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("whitespace key: %v", err)
	}
	other, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(context.Background(), other.Raw); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("unknown key: %v", err)
	}
}

func TestTenantCacheExpiryAndCollision(t *testing.T) {
	good, raw := tenantWithKey(t, true)
	bad, otherRaw := tenantWithKey(t, true)
	bad.APIKeyPrefix = good.APIKeyPrefix
	fake := &fakeKeyStore{tenants: []*model.Tenant{bad, good}}
	a := NewTenantAuthenticator(fake, time.Minute)
	now := time.Now()
	a.now = func() time.Time { return now }
	if _, err := a.Authenticate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(context.Background(), otherRaw); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := a.Authenticate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 3 {
		t.Fatalf("store calls = %d, want 3 after expiry", fake.calls)
	}
}
