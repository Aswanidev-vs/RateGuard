package server

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Aswanidev-vs/RateGuard/internal/httpx"
	"github.com/Aswanidev-vs/RateGuard/internal/model"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
	"github.com/Aswanidev-vs/RateGuard/web"
)

const maxDashboardRows = 50

func (s *Server) sessionFromRequest(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	if !s.sessions.verify(c.Value, s.now()) {
		return ""
	}
	return c.Value
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token := s.sessionFromRequest(r)
		if token == "" {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusSeeOther)
			return
		}
		if isUnsafeMethod(r.Method) {
			if !s.sameOrigin(r) {
				s.renderError(w, r, http.StatusForbidden, "Request origin was not accepted.")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if err := r.ParseForm(); err != nil {
				s.renderError(w, r, http.StatusBadRequest, "The form could not be read.")
				return
			}
			if !s.sessions.validCSRF(token, csrfToken(r)) {
				s.renderError(w, r, http.StatusForbidden, "The security token was missing or invalid. Refresh and try again.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func csrfToken(r *http.Request) string {
	if h := r.Header.Get("X-CSRF-Token"); h != "" {
		return h
	}
	if token := r.PostFormValue("csrf"); token != "" {
		return token
	}
	return r.PostFormValue("csrf_token")
}

func (s *Server) layoutData(r *http.Request, title, active string) web.LayoutData {
	data := web.LayoutData{
		Title:   title,
		Active:  active,
		DevMode: s.cfg.DevMode,
		Version: s.version,
	}
	if token := s.sessionFromRequest(r); token != "" {
		data.Authenticated = true
		data.CSRF = s.sessions.csrfFor(token)
	}
	return data
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.sessionFromRequest(r) != "" {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderLogin(w, http.StatusOK, "", safeNext(r.URL.Query().Get("next")))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.sameOrigin(r) {
		s.renderLogin(w, http.StatusForbidden, "Request origin was not accepted.", "/dashboard")
		return
	}
	if !s.loginLimit.Allow(s.clientIP(r)) {
		s.renderLogin(w, http.StatusTooManyRequests, "Too many sign-in attempts. Wait a minute and try again.", "/dashboard")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, http.StatusBadRequest, "The sign-in form could not be read.", "/dashboard")
		return
	}
	next := safeNext(r.PostFormValue("next"))
	if !s.admin.Verify(r.PostFormValue("key")) {
		s.log.Warn("dashboard login failed", "client_ip", s.clientIP(r))
		s.renderLogin(w, http.StatusUnauthorized, "That admin key is not valid.", next)
		return
	}
	s.loginLimit.Reset(s.clientIP(r))
	token, expires, err := s.sessions.create(s.now())
	if err != nil {
		s.log.Error("create dashboard session", "error", err)
		s.renderLogin(w, http.StatusInternalServerError, "Could not start a session.", next)
		return
	}
	s.sessions.setCookie(w, token, expires, s.cfg.CookieSecure)
	s.log.Info("dashboard login", "client_ip", s.clientIP(r))
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := s.sessionFromRequest(r)
	if token == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !s.sameOrigin(r) {
		s.renderError(w, r, http.StatusForbidden, "Request origin was not accepted.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "The form could not be read.")
		return
	}
	if !s.sessions.validCSRF(token, csrfToken(r)) {
		s.renderError(w, r, http.StatusForbidden, "The security token was missing or invalid.")
		return
	}
	s.sessions.clearCookie(w, s.cfg.CookieSecure)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) renderLogin(w http.ResponseWriter, status int, message, next string) {
	s.renderer.Render(w, status, "login", web.LoginData{
		Title:   "Sign in",
		Error:   message,
		Next:    next,
		DevMode: s.cfg.DevMode,
		Version: s.version,
	})
}

func safeNext(next string) string {
	if next == "" {
		return "/dashboard"
	}
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.ContainsAny(next, "\\\r\n") {
		return "/dashboard"
	}
	return next
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenants, err := s.store.ListTenants(ctx)
	if err != nil {
		s.log.Error("dashboard list tenants", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Tenant data could not be loaded.")
		return
	}
	anomalies, err := s.store.ListAnomalies(ctx, 100)
	if err != nil {
		s.log.Error("dashboard list anomalies", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Anomaly data could not be loaded.")
		return
	}
	rows, agg, err := s.loadTenantRows(ctx, tenants, maxDashboardRows, s.now().UTC())
	if err != nil {
		s.log.Error("dashboard usage", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Usage data could not be loaded.")
		return
	}
	summary := agg.Summary()
	enabled := 0
	demoTenant := ""
	for _, t := range tenants {
		if t.Enabled {
			enabled++
			if demoTenant == "" {
				demoTenant = t.TenantID
			}
		}
	}
	stats := web.Stats{
		TenantCount:    len(tenants),
		EnabledTenants: enabled,
		Allowed24h:     summary.Allowed,
		Blocked24h:     summary.Blocked,
		AvgLatencyMs:   summary.AvgLatencyMs,
		P95Ms:          summary.P95Ms,
		AnomalyCount:   len(anomalies),
	}
	if total := summary.Allowed + summary.Blocked; total > 0 {
		stats.BlockRatePct = float64(summary.Blocked) / float64(total) * 100
	}
	recent := anomalies
	if len(recent) > 8 {
		recent = recent[:8]
	}
	s.renderer.Render(w, http.StatusOK, "overview", web.OverviewData{
		LayoutData:        s.layoutData(r, "Overview", "overview"),
		Stats:             stats,
		Tenants:           rows,
		Anomalies:         recent,
		DemoEnabled:       s.cfg.DemoEnabled,
		DemoDefaultTenant: demoTenant,
	})
}

func (s *Server) handleTenantsPage(w http.ResponseWriter, r *http.Request) {
	tenants, err := s.store.ListTenants(r.Context())
	if err != nil {
		s.log.Error("dashboard list tenants", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Tenant data could not be loaded.")
		return
	}
	rows, _, err := s.loadTenantRows(r.Context(), tenants, maxDashboardRows, s.now().UTC())
	if err != nil {
		s.log.Error("dashboard usage", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Usage data could not be loaded.")
		return
	}
	demoTenant := ""
	for _, t := range tenants {
		if t.Enabled {
			demoTenant = t.TenantID
			break
		}
	}
	s.renderer.Render(w, http.StatusOK, "tenants", web.TenantsData{
		LayoutData:        s.layoutData(r, "Tenants", "tenants"),
		Tenants:           rows,
		DemoEnabled:       s.cfg.DemoEnabled,
		DemoDefaultTenant: demoTenant,
	})
}

func (s *Server) handleAnomaliesPage(w http.ResponseWriter, r *http.Request) {
	anomalies, err := s.store.ListAnomalies(r.Context(), 200)
	if err != nil {
		s.log.Error("dashboard list anomalies", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Anomaly data could not be loaded.")
		return
	}
	s.renderer.Render(w, http.StatusOK, "anomalies", web.AnomaliesData{
		LayoutData: s.layoutData(r, "Cost anomalies", "anomalies"),
		Anomalies:  anomalies,
	})
}

func (s *Server) handleHelpPage(w http.ResponseWriter, r *http.Request) {
	s.renderer.Render(w, http.StatusOK, "help", s.layoutData(r, "Operator guide", "help"))
}

func (s *Server) handleTenantsPartial(w http.ResponseWriter, r *http.Request) {
	tenants, err := s.store.ListTenants(r.Context())
	if err != nil {
		s.log.Error("tenant partial", "error", err)
		http.Error(w, "tenant data unavailable", http.StatusInternalServerError)
		return
	}
	rows, _, err := s.loadTenantRows(r.Context(), tenants, maxDashboardRows, s.now().UTC())
	if err != nil {
		s.log.Error("tenant partial usage", "error", err)
		http.Error(w, "usage data unavailable", http.StatusInternalServerError)
		return
	}
	s.renderer.RenderPartial(w, http.StatusOK, "tenants_table_body", web.TenantsData{
		LayoutData: s.layoutData(r, "Tenants", "tenants"),
		Tenants:    rows,
	})
}

func (s *Server) handleUsagePartial(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tenant, err := s.store.GetTenant(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Tenant not found.")
		return
	}
	summary, err := s.usageForTenant(r.Context(), tenant.TenantID, s.now().UTC())
	if err != nil {
		s.log.Error("usage partial", "tenant_id", id, "error", err)
		http.Error(w, "usage unavailable", http.StatusInternalServerError)
		return
	}
	s.renderer.RenderPartial(w, http.StatusOK, "usage_card", web.UsageCardData{
		TenantID: tenant.TenantID,
		Summary:  summary,
		Window:   "last 24h",
	})
}

func (s *Server) handleAnomaliesPartial(w http.ResponseWriter, r *http.Request) {
	anomalies, err := s.store.ListAnomalies(r.Context(), 200)
	if err != nil {
		s.log.Error("anomaly partial", "error", err)
		http.Error(w, "anomaly data unavailable", http.StatusInternalServerError)
		return
	}
	s.renderer.RenderPartial(w, http.StatusOK, "anomaly_list", web.AnomaliesData{
		LayoutData: s.layoutData(r, "Cost anomalies", "anomalies"),
		Anomalies:  anomalies,
	})
}

func (s *Server) handleUsageJSON(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetTenant(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, httpx.CodeNotFound, "tenant not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, httpx.CodeInternal, "internal error")
		return
	}
	summary, err := s.usageForTenant(r.Context(), id, s.now().UTC())
	if err != nil {
		s.log.Error("dashboard usage json", "tenant_id", id, "error", err)
		httpx.Error(w, http.StatusInternalServerError, httpx.CodeInternal, "could not load usage")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, summary)
}

func (s *Server) handleDashboardCreateTenant(w http.ResponseWriter, r *http.Request) {
	req := model.CreateTenantRequest{
		Name:     r.PostFormValue("name"),
		Email:    r.PostFormValue("email"),
		LimitRPM: atoiOrZero(r.PostFormValue("limit_rpm")),
		Burst:    atoiOrZero(r.PostFormValue("burst")),
	}
	req, err := validateCreate(req)
	if err != nil {
		formError(w, err.Error())
		return
	}
	tenant, rawKey, err := s.createTenantRecord(r.Context(), req)
	if err != nil {
		s.log.Error("dashboard create tenant", "error", err)
		formError(w, "Could not create the tenant. Try again.")
		return
	}
	s.log.Info("tenant created via dashboard", "tenant_id", tenant.TenantID)
	w.Header().Set("HX-Trigger", "tenantCreated")
	s.renderer.RenderPartial(w, http.StatusOK, "created_tenant", web.CreatedTenantData{
		Tenant: *tenant,
		APIKey: rawKey,
	})
}

func (s *Server) handleDashboardUpdateTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tenant, err := s.store.GetTenant(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Tenant not found.")
		return
	}
	if v := strings.TrimSpace(r.PostFormValue("limit_rpm")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			formError(w, "limit_rpm must be a number.")
			return
		}
		tenant.LimitRPM = n
	}
	if v := strings.TrimSpace(r.PostFormValue("burst")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			formError(w, "burst must be a number.")
			return
		}
		tenant.Burst = n
	}
	tenant.Enabled = r.PostFormValue("enabled") == "on" || r.PostFormValue("enabled") == "true"
	if err := validateLimits(tenant.LimitRPM, tenant.Burst); err != nil {
		formError(w, err.Error())
		return
	}
	tenant.UpdatedAt = model.NowISO()
	if err := s.store.UpdateTenant(r.Context(), tenant); err != nil {
		s.log.Error("dashboard update tenant", "tenant_id", id, "error", err)
		formError(w, "Could not update the tenant.")
		return
	}
	s.tenants.Invalidate(tenant.TenantID)
	row, err := s.tenantRow(r.Context(), tenant, s.now().UTC())
	if err != nil {
		s.log.Error("dashboard updated row usage", "tenant_id", id, "error", err)
	}
	s.renderer.RenderPartial(w, http.StatusOK, "tenant_row", row)
}

func (s *Server) handleDashboardDisableTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tenant, err := s.store.GetTenant(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Tenant not found.")
		return
	}
	if tenant.Enabled {
		tenant.Enabled = false
		tenant.UpdatedAt = model.NowISO()
		if err := s.store.UpdateTenant(r.Context(), tenant); err != nil {
			s.log.Error("dashboard disable tenant", "tenant_id", id, "error", err)
			formError(w, "Could not disable the tenant.")
			return
		}
		s.tenants.Invalidate(tenant.TenantID)
	}
	row, err := s.tenantRow(r.Context(), tenant, s.now().UTC())
	if err != nil {
		s.log.Error("dashboard disabled row usage", "tenant_id", id, "error", err)
	}
	s.renderer.RenderPartial(w, http.StatusOK, "tenant_row", row)
}

func formError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<div class="form-error" role="alert">%s</div>`, html.EscapeString(message))
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func (s *Server) usageWindow(now time.Time) (string, string) {
	return now.Add(-23 * time.Hour).Format("2006-01-02T15"), now.Format("2006-01-02T15")
}

func (s *Server) usageForTenant(ctx context.Context, tenantID string, now time.Time) (model.UsageSummary, error) {
	from, to := s.usageWindow(now)
	agg, err := s.store.GetUsage(ctx, tenantID, from, to)
	if err != nil {
		return model.UsageSummary{}, err
	}
	return agg.Summary(), nil
}

func (s *Server) tenantRow(ctx context.Context, tenant *model.Tenant, now time.Time) (web.TenantRow, error) {
	from, to := s.usageWindow(now)
	agg, err := s.store.GetUsage(ctx, tenant.TenantID, from, to)
	if err != nil {
		return web.TenantRow{Tenant: *tenant}, err
	}
	summary := agg.Summary()
	return web.TenantRow{
		Tenant:       *tenant,
		Allowed24h:   summary.Allowed,
		Blocked24h:   summary.Blocked,
		AvgLatencyMs: summary.AvgLatencyMs,
		P95Ms:        summary.P95Ms,
	}, nil
}

func (s *Server) loadTenantRows(ctx context.Context, tenants []*model.Tenant, limit int, now time.Time) ([]web.TenantRow, model.UsageAggregate, error) {
	if limit > len(tenants) {
		limit = len(tenants)
	}
	rows := make([]web.TenantRow, 0, limit)
	agg := model.UsageAggregate{Histogram: make(map[string]int64)}
	from, to := s.usageWindow(now)
	for _, t := range tenants[:limit] {
		u, err := s.store.GetUsage(ctx, t.TenantID, from, to)
		if err != nil {
			return nil, agg, err
		}
		summary := u.Summary()
		rows = append(rows, web.TenantRow{
			Tenant:       *t,
			Allowed24h:   summary.Allowed,
			Blocked24h:   summary.Blocked,
			AvgLatencyMs: summary.AvgLatencyMs,
			P95Ms:        summary.P95Ms,
		})
		agg.Allowed += u.Allowed
		agg.Blocked += u.Blocked
		agg.Count += u.Count
		agg.SumLatencyMs += u.SumLatencyMs
		for k, v := range u.Histogram {
			agg.Histogram[k] += v
		}
	}
	return rows, agg, nil
}
