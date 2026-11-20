package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Aswanidev-vs/RateGuard/internal/auth"
	"github.com/Aswanidev-vs/RateGuard/internal/config"
	"github.com/Aswanidev-vs/RateGuard/internal/limiter"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
	"github.com/Aswanidev-vs/RateGuard/web"
)

const testAdminKey = "test-admin-key-with-at-least-16-chars"

type usageCall struct {
	tenantID string
	allowed  bool
	latency  time.Duration
}

type fakeUsage struct {
	mu    sync.Mutex
	calls []usageCall
}

func (f *fakeUsage) Record(tenantID string, allowed bool, latency time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, usageCall{tenantID, allowed, latency})
}

func (f *fakeUsage) snapshot() []usageCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]usageCall(nil), f.calls...)
}

type testEnv struct {
	srv          *Server
	upstream     *httptest.Server
	upstreamAuth *string
	usage        *fakeUsage
	store        store.Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/quote" {
			t.Errorf("upstream path = %s, want /quote", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "spoofed")
		_, _ = io.WriteString(w, `{"quote":"test quote","author":"tester"}`)
	}))
	t.Cleanup(upstream.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	renderer, err := web.NewRenderer(log)
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	st := store.NewMemory()
	admin, err := auth.NewAdminVerifier(testAdminKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Port:               "8080",
		StoreBackend:       "memory",
		TenantsTable:       "api_guard_tenants",
		UsageTable:         "api_guard_usage",
		AnomaliesTable:     "api_guard_cost_anomalies",
		AdminAPIKey:        testAdminKey,
		SessionSecret:      strings.Repeat("s", 48),
		UpstreamURL:        upstream.URL,
		DemoEnabled:        true,
		DevMode:            true,
		CookieSecure:       false,
		UsageFlushInterval: time.Second,
		UsageBatchSize:     100,
	}
	u := &fakeUsage{}
	srv, err := New(Options{
		Config:   cfg,
		Store:    st,
		Limiter:  limiter.New(),
		Tenants:  auth.NewTenantAuthenticator(st, time.Minute),
		Admin:    admin,
		Usage:    u,
		Renderer: renderer,
		Logger:   log,
		Version:  "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{srv: srv, upstream: upstream, upstreamAuth: &gotAuth, usage: u, store: st}
}

func (e *testEnv) do(req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(w, req)
	return w
}

func adminRequest(method, path string, body []byte) *http.Request {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+testAdminKey)
	return r
}

func createTestTenant(t *testing.T, e *testEnv) (string, string) {
	t.Helper()
	body := []byte(`{"name":"Acme","email":"ops@acme.example","limit_rpm":60,"burst":2}`)
	w := e.do(adminRequest(http.MethodPost, "/api/v1/tenants", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create tenant: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		TenantID string `json:"tenant_id"`
		APIKey   string `json:"api_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.TenantID == "" || !strings.HasPrefix(out.APIKey, "ag_live_") {
		t.Fatalf("bad create response: %+v", out)
	}
	return out.TenantID, out.APIKey
}

func TestTenantLifecycleAndProxyRateLimit(t *testing.T) {
	e := newTestEnv(t)
	id, key := createTestTenant(t, e)

	w := e.do(adminRequest(http.MethodGet, "/api/v1/tenants/"+id, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("get tenant: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "api_key_hash") {
		t.Fatal("tenant response leaked API key hash")
	}

	proxyReq := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/proxy/quote", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		return e.do(r)
	}
	for i := 0; i < 2; i++ {
		w := proxyReq()
		if w.Code != http.StatusOK {
			t.Fatalf("allowed proxy request %d: %d %s", i, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "test quote") {
			t.Fatalf("unexpected upstream body: %s", w.Body.String())
		}
		if w.Header().Get("X-RateLimit-Limit") != "2" {
			t.Fatalf("missing rate limit headers: %v", w.Header())
		}
		if w.Header().Get("X-RateLimit-Remaining") == "spoofed" {
			t.Fatal("upstream spoofed rate header survived")
		}
		if *e.upstreamAuth != "" {
			t.Fatal("tenant API key was forwarded to upstream")
		}
	}
	w = proxyReq()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" || w.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("missing 429 headers: %v", w.Header())
	}
	if !strings.Contains(w.Body.String(), "rate_limited") {
		t.Fatalf("bad 429 body: %s", w.Body.String())
	}
	calls := e.usage.snapshot()
	if len(calls) != 3 || calls[2].allowed {
		t.Fatalf("usage calls = %+v", calls)
	}

	patch := []byte(`{"limit_rpm":120,"burst":5}`)
	w = e.do(adminRequest(http.MethodPatch, "/api/v1/tenants/"+id, patch))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"burst":5`) {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	w = e.do(adminRequest(http.MethodDelete, "/api/v1/tenants/"+id, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	w = proxyReq()
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled key accepted: %d", w.Code)
	}

	w = e.do(adminRequest(http.MethodGet, "/api/v1/tenants/"+id+"/usage", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "avg_latency_ms") {
		t.Fatalf("usage: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminAuthAndSecurityHeaders(t *testing.T) {
	e := newTestEnv(t)
	w := e.do(httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized admin: %d", w.Code)
	}
	w = e.do(httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz: %d %s", w.Code, w.Body.String())
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP missing: %q", csp)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers missing: %v", w.Header())
	}
	w = e.do(httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("readyz: %d", w.Code)
	}
}

var csrfPattern = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func TestDashboardLoginCSRFAndPages(t *testing.T) {
	e := newTestEnv(t)
	w := e.do(httptest.NewRequest(http.MethodGet, "/login", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "name=\"key\"") {
		t.Fatalf("login page: %d %s", w.Code, w.Body.String())
	}

	form := url.Values{"key": {testAdminKey}, "next": {"/dashboard"}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://example.com")
	w = e.do(r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("session cookie: %+v", cookies)
	}
	cookie := cookies[0]

	withSession := func(method, path string, body url.Values, csrf string) *httptest.ResponseRecorder {
		var req *http.Request
		if body == nil {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(cookie)
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("HX-Request", "true")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		return e.do(req)
	}

	w = withSession(http.MethodGet, "/dashboard/tenants", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("tenants page: %d %s", w.Code, w.Body.String())
	}
	match := csrfPattern.FindStringSubmatch(w.Body.String())
	if len(match) != 2 {
		t.Fatal("no CSRF token in tenants page")
	}
	csrf := match[1]

	formTenant := url.Values{
		"name": {"Dashboard Tenant"}, "email": {"dash@example.com"},
		"limit_rpm": {"30"}, "burst": {"3"},
	}
	w = withSession(http.MethodPost, "/dashboard/tenants", formTenant, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF token should be rejected, got %d", w.Code)
	}
	w = withSession(http.MethodPost, "/dashboard/tenants", formTenant, csrf)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ag_live_") {
		t.Fatalf("dashboard create: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("HX-Trigger") != "tenantCreated" {
		t.Fatalf("missing HX-Trigger: %v", w.Header())
	}

	for _, path := range []string{"/dashboard", "/dashboard/anomalies", "/dashboard/help", "/dashboard/partials/tenants"} {
		w = withSession(http.MethodGet, path, nil, "")
		if w.Code != http.StatusOK {
			t.Fatalf("page %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w = withSession(http.MethodPost, "/logout", url.Values{"csrf_token": {csrf}}, csrf)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("logout: %d %s", w.Code, w.Body.String())
	}
}
