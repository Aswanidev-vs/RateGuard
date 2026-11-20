package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Aswanidev-vs/RateGuard/internal/httpx"
	"github.com/Aswanidev-vs/RateGuard/internal/model"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
)

func (s *Server) requireAdminAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !s.adminLimit.Allow(s.clientIP(r)) {
			httpx.Error(w, http.StatusTooManyRequests, httpx.CodeRateLimited, "too many admin attempts")
			return
		}
		if !s.admin.Verify(adminKey(r)) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="rateguard-admin"`)
			httpx.Error(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "invalid admin key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	var req model.CreateTenantRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, httpx.CodeInvalidInput, err.Error())
		return
	}
	req, err := validateCreate(req)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, httpx.CodeInvalidInput, err.Error())
		return
	}
	tenant, rawKey, err := s.createTenantRecord(r.Context(), req)
	if err != nil {
		s.log.Error("create tenant", "error", err)
		httpx.Error(w, http.StatusInternalServerError, httpx.CodeInternal, "could not create tenant")
		return
	}
	s.log.Info("tenant created", "tenant_id", tenant.TenantID)
	w.Header().Set("Location", "/api/v1/tenants/"+tenant.TenantID)
	httpx.JSON(w, http.StatusCreated, map[string]string{
		"tenant_id": tenant.TenantID,
		"api_key":   rawKey,
	})
}

func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	tenants, err := s.store.ListTenants(r.Context())
	if err != nil {
		s.log.Error("list tenants", "error", err)
		httpx.Error(w, http.StatusInternalServerError, httpx.CodeInternal, "could not list tenants")
		return
	}
	if tenants == nil {
		tenants = []*model.Tenant{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"tenants": tenants})
}

func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tenant, err := s.store.GetTenant(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err, "tenant")
		return
	}
	httpx.JSON(w, http.StatusOK, tenant)
}

func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	var req model.UpdateTenantRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, httpx.CodeInvalidInput, err.Error())
		return
	}
	if req.LimitRPM == nil && req.Burst == nil && req.Enabled == nil {
		httpx.Error(w, http.StatusBadRequest, httpx.CodeInvalidInput, "at least one of limit_rpm, burst, enabled is required")
		return
	}
	id := chi.URLParam(r, "id")
	tenant, err := s.store.GetTenant(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err, "tenant")
		return
	}
	if req.LimitRPM != nil {
		tenant.LimitRPM = *req.LimitRPM
	}
	if req.Burst != nil {
		tenant.Burst = *req.Burst
	}
	if req.Enabled != nil {
		tenant.Enabled = *req.Enabled
	}
	if err := validateLimits(tenant.LimitRPM, tenant.Burst); err != nil {
		httpx.Error(w, http.StatusBadRequest, httpx.CodeInvalidInput, err.Error())
		return
	}
	tenant.UpdatedAt = model.NowISO()
	if err := s.store.UpdateTenant(r.Context(), tenant); err != nil {
		s.writeStoreError(w, err, "tenant")
		return
	}
	s.tenants.Invalidate(tenant.TenantID)
	httpx.JSON(w, http.StatusOK, tenant)
}

func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tenant, err := s.store.GetTenant(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err, "tenant")
		return
	}
	if tenant.Enabled {
		tenant.Enabled = false
		tenant.UpdatedAt = model.NowISO()
		if err := s.store.UpdateTenant(r.Context(), tenant); err != nil {
			s.writeStoreError(w, err, "tenant")
			return
		}
		s.tenants.Invalidate(tenant.TenantID)
		s.log.Info("tenant disabled", "tenant_id", tenant.TenantID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := chi.URLParam(r, "id")
	if !s.authorizeUsage(w, r, id) {
		return
	}
	tenant, err := s.store.GetTenant(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err, "tenant")
		return
	}
	from, to, err := parseUsageWindow(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, httpx.CodeInvalidInput, err.Error())
		return
	}
	agg, err := s.store.GetUsage(r.Context(), tenant.TenantID, from, to)
	if err != nil {
		s.log.Error("get usage", "tenant_id", id, "error", err)
		httpx.Error(w, http.StatusInternalServerError, httpx.CodeInternal, "could not load usage")
		return
	}
	httpx.JSON(w, http.StatusOK, agg.Summary())
}

// authorizeUsage allows an admin key or the tenant's own key. It writes the
// error response and returns false when neither is acceptable.
func (s *Server) authorizeUsage(w http.ResponseWriter, r *http.Request, tenantID string) bool {
	if admin := adminKey(r); admin != "" && s.admin.Verify(admin) {
		return true
	}
	if !s.adminLimit.Allow(s.clientIP(r)) {
		httpx.Error(w, http.StatusTooManyRequests, httpx.CodeRateLimited, "too many auth attempts")
		return false
	}
	raw := tenantKey(r)
	if raw == "" {
		httpx.Error(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "API key required")
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	tenant, err := s.tenants.Authenticate(ctx, raw)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "invalid API key")
		return false
	}
	if tenant.TenantID != tenantID {
		httpx.Error(w, http.StatusForbidden, httpx.CodeForbidden, "API key does not belong to this tenant")
		return false
	}
	return true
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error, resource string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, httpx.CodeNotFound, resource+" not found")
	case errors.Is(err, store.ErrConflict):
		httpx.Error(w, http.StatusConflict, httpx.CodeInvalidInput, resource+" was modified concurrently")
	default:
		s.log.Error("store operation failed", "resource", resource, "error", err)
		httpx.Error(w, http.StatusInternalServerError, httpx.CodeInternal, "internal server error")
	}
}

func parseUsageWindow(r *http.Request) (string, string, error) {
	now := time.Now().UTC()
	from, err := parseTimeParam(r.URL.Query().Get("from"), now.Add(-24*time.Hour))
	if err != nil {
		return "", "", errors.New("from must be RFC3339, YYYY-MM-DD, or YYYY-MM-DDTHH")
	}
	to, err := parseTimeParam(r.URL.Query().Get("to"), now)
	if err != nil {
		return "", "", errors.New("to must be RFC3339, YYYY-MM-DD, or YYYY-MM-DDTHH")
	}
	if !from.Before(to) {
		return "", "", errors.New("from must be before to")
	}
	if to.Sub(from) > 31*24*time.Hour {
		return "", "", errors.New("usage window cannot exceed 31 days")
	}
	return from.UTC().Truncate(time.Hour).Format("2006-01-02T15"), to.UTC().Truncate(time.Hour).Format("2006-01-02T15"), nil
}

func parseTimeParam(value string, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("unsupported time format")
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func adminKey(r *http.Request) string {
	if token := bearerToken(r); token != "" {
		return token
	}
	return strings.TrimSpace(r.Header.Get("X-Admin-Key"))
}

func tenantKey(r *http.Request) string {
	if token := bearerToken(r); token != "" {
		return token
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}
