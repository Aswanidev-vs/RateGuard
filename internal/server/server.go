// Package server wires the RateGuard HTTP API, rate-limiting proxy, and
// dashboard together.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Aswanidev-vs/RateGuard/internal/auth"
	"github.com/Aswanidev-vs/RateGuard/internal/config"
	"github.com/Aswanidev-vs/RateGuard/internal/httpx"
	"github.com/Aswanidev-vs/RateGuard/internal/limiter"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
	"github.com/Aswanidev-vs/RateGuard/web"
)

const (
	dashboardSessionTTL = 12 * time.Hour
	loginAttempts       = 8
	loginWindow         = time.Minute
	adminAPIAttempts    = 120
	adminAPIWindow      = time.Minute
)

// UsageRecorder is satisfied by *usage.Recorder; tests can substitute a fake.
type UsageRecorder interface {
	Record(tenantID string, allowed bool, latency time.Duration)
}

// Renderer is satisfied by *web.Renderer.
type Renderer interface {
	Render(w http.ResponseWriter, status int, page string, data any)
	RenderPartial(w http.ResponseWriter, status int, name string, data any)
	Static() http.Handler
}

// Options are the server dependencies.
type Options struct {
	Config   *config.Config
	Store    store.Store
	Limiter  *limiter.Limiter
	Tenants  *auth.TenantAuthenticator
	Admin    *auth.AdminVerifier
	Usage    UsageRecorder
	Renderer Renderer
	Logger   *slog.Logger
	Version  string
}

// Server holds shared dependencies and the built router.
type Server struct {
	cfg        *config.Config
	store      store.Store
	limiter    *limiter.Limiter
	tenants    *auth.TenantAuthenticator
	admin      *auth.AdminVerifier
	usage      UsageRecorder
	renderer   Renderer
	log        *slog.Logger
	version    string
	sessions   *sessionManager
	loginLimit *auth.AttemptLimiter
	adminLimit *auth.AttemptLimiter
	proxy      http.Handler
	handler    http.Handler
	now        func() time.Time
}

// New validates dependencies, builds the proxy, and constructs the router.
func New(opts Options) (*Server, error) {
	if opts.Config == nil || opts.Store == nil || opts.Limiter == nil || opts.Tenants == nil ||
		opts.Admin == nil || opts.Usage == nil || opts.Renderer == nil || opts.Logger == nil {
		return nil, errors.New("server: missing required dependency")
	}
	s := &Server{
		cfg:        opts.Config,
		store:      opts.Store,
		limiter:    opts.Limiter,
		tenants:    opts.Tenants,
		admin:      opts.Admin,
		usage:      opts.Usage,
		renderer:   opts.Renderer,
		log:        opts.Logger,
		version:    opts.Version,
		sessions:   newSessionManager(opts.Config.SessionSecret, dashboardSessionTTL),
		loginLimit: auth.NewAttemptLimiter(loginAttempts, loginWindow),
		adminLimit: auth.NewAttemptLimiter(adminAPIAttempts, adminAPIWindow),
		now:        time.Now,
	}
	if opts.Config.UpstreamURL != "" {
		p, _, err := newProxy(opts.Config.UpstreamURL, opts.Logger)
		if err != nil {
			return nil, err
		}
		s.proxy = p
	}
	s.handler = s.routes()
	return s, nil
}

// Handler returns the composed HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.CleanPath)
	r.Use(s.securityHeaders)
	r.Use(s.requestLogger)
	r.Use(s.recoverer)

	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Handle("/static/*", http.StripPrefix("/static", s.renderer.Static()))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		if s.sessionFromRequest(r) != "" {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
	r.Get("/login", s.handleLoginPage)
	r.Post("/login", s.handleLogin)
	r.Post("/logout", s.handleLogout)

	r.Route("/dashboard", func(r chi.Router) {
		r.Use(s.requireSession)
		r.Get("/", s.handleOverview)
		r.Get("/tenants", s.handleTenantsPage)
		r.Get("/anomalies", s.handleAnomaliesPage)
		r.Get("/help", s.handleHelpPage)
		r.Get("/partials/tenants", s.handleTenantsPartial)
		r.Get("/partials/usage/{id}", s.handleUsagePartial)
		r.Get("/partials/anomalies", s.handleAnomaliesPartial)
		r.Get("/api/usage/{id}", s.handleUsageJSON)
		r.Post("/tenants", s.handleDashboardCreateTenant)
		r.Patch("/tenants/{id}", s.handleDashboardUpdateTenant)
		r.Delete("/tenants/{id}", s.handleDashboardDisableTenant)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/tenants/{id}/usage", s.handleUsage)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAdminAPI)
			r.Get("/tenants", s.handleListTenants)
			r.Post("/tenants", s.handleCreateTenant)
			r.Get("/tenants/{id}", s.handleGetTenant)
			r.Patch("/tenants/{id}", s.handleUpdateTenant)
			r.Delete("/tenants/{id}", s.handleDeleteTenant)
		})
	})

	r.Handle("/proxy/*", http.HandlerFunc(s.handleProxy))

	r.NotFound(s.handleNotFound)
	r.MethodNotAllowed(s.handleMethodNotAllowed)
	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	if err := s.store.Ping(ctx); err != nil {
		s.log.Warn("readiness check failed", "error", err)
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/proxy/") {
		httpx.Error(w, http.StatusNotFound, httpx.CodeNotFound, "endpoint not found")
		return
	}
	s.renderError(w, r, http.StatusNotFound, "That page does not exist.")
}

func (s *Server) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/proxy/") {
		httpx.Error(w, http.StatusMethodNotAllowed, httpx.CodeInvalidInput, "method not allowed")
		return
	}
	s.renderError(w, r, http.StatusMethodNotAllowed, "That method is not allowed here.")
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	csp := "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; " +
		"form-action 'self'; img-src 'self' data:; font-src 'self'; style-src 'self'; " +
		"script-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'none'"
	if s.cfg.CookieSecure {
		csp += "; upgrade-insecure-requests"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if s.cfg.CookieSecure {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		s.log.Info("http request",
			"request_id", middleware.GetReqID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", sw.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"client_ip", s.clientIP(r),
		)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic recovered",
					"request_id", middleware.GetReqID(r.Context()),
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				httpx.Error(w, http.StatusInternalServerError, httpx.CodeInternal, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.cfg.TrustProxy {
		// Only the right-most entry is guaranteed to have been appended by our
		// single trusted reverse proxy; earlier entries are client-controlled.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			candidate := strings.TrimSpace(parts[len(parts)-1])
			if net.ParseIP(candidate) != nil {
				return candidate
			}
		}
	}
	return host
}

func (s *Server) sameOrigin(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := r.Header.Get("Origin")
	forwarded := r.Header.Get("X-Forwarded-Host")
	if originAllowed(origin, r.Host, forwarded, s.cfg.TrustProxy) {
		return true
	}
	s.log.Warn("origin rejected",
		"path", r.URL.Path,
		"origin", origin,
		"host", r.Host,
		"forwarded_host", forwarded,
	)
	return false
}

// originAllowed compares a browser Origin against the host Go sees. Loopback
// aliases (localhost and 127.0.0.1 on the same port) are treated as one host,
// and a trusted proxy may supply the public host via X-Forwarded-Host.
func originAllowed(origin, host, forwardedHost string, trustProxy bool) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	hosts := []string{host}
	if trustProxy && forwardedHost != "" {
		hosts = append(hosts, strings.TrimSpace(strings.Split(forwardedHost, ",")[0]))
	}
	for _, candidate := range hosts {
		if candidate == "" {
			continue
		}
		if strings.EqualFold(u.Host, candidate) || sameLoopbackHost(u.Host, candidate) {
			return true
		}
	}
	return false
}

func sameLoopbackHost(a, b string) bool {
	ah, ap, errA := net.SplitHostPort(a)
	bh, bp, errB := net.SplitHostPort(b)
	if errA != nil || errB != nil || ap != bp {
		return false
	}
	return isLoopbackHost(ah) && isLoopbackHost(bh)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	token := s.sessionFromRequest(r)
	data := web.ErrorData{
		LayoutData: web.LayoutData{
			Title:         http.StatusText(status),
			Authenticated: token != "",
			Version:       s.version,
			DevMode:       s.cfg.DevMode,
		},
		Status:  status,
		Message: message,
	}
	if token != "" {
		data.CSRF = s.sessions.csrfFor(token)
	}
	s.renderer.Render(w, status, "error", data)
}
