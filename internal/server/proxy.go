package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Aswanidev-vs/RateGuard/internal/httpx"
)

type rateInfoKey struct{}

type rateInfo struct {
	limit     string
	remaining string
	reset     string
}

func newProxy(rawURL string, log *slog.Logger) (*httputil.ReverseProxy, *url.URL, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse PROXY_UPSTREAM_URL: %w", err)
	}
	if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, nil, errors.New("PROXY_UPSTREAM_URL must be an absolute http(s) URL")
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		path := strings.TrimPrefix(req.URL.Path, "/proxy")
		if path == "" {
			path = "/"
		} else if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		req.URL.Path = path
		req.URL.RawPath = ""
		baseDirector(req)
		req.Host = target.Host

		// The tenant key, dashboard session, and admin key must never reach the
		// upstream. Client-supplied forwarding headers are spoofable; clear them
		// so the reverse proxy can append its own.
		for _, h := range []string{
			"Authorization", "X-Api-Key", "X-Admin-Key", "Cookie", "X-Csrf-Token",
			"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
			"X-Real-Ip",
		} {
			req.Header.Del(h)
		}
	}
	proxy.Transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	proxy.FlushInterval = 100 * time.Millisecond
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Never let an upstream spoof the proxy's own rate-limit contract.
		resp.Header.Del("Server")
		resp.Header.Del("X-Powered-By")
		resp.Header.Del("X-RateLimit-Limit")
		resp.Header.Del("X-RateLimit-Remaining")
		resp.Header.Del("X-RateLimit-Reset")
		resp.Header.Del("Retry-After")
		applyRateHeaders(resp.Header, resp.Request.Context())
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Warn("upstream request failed", "path", r.URL.Path, "error", err)
		applyRateHeaders(w.Header(), r.Context())
		httpx.Error(w, http.StatusBadGateway, httpx.CodeInternal, "upstream request failed")
	}
	return proxy, target, nil
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	if s.proxy == nil {
		httpx.Error(w, http.StatusServiceUnavailable, httpx.CodeInternal, "proxy upstream is not configured")
		return
	}
	key := tenantKey(r)
	if key == "" {
		httpx.Error(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "API key required")
		return
	}
	authCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	tenant, err := s.tenants.Authenticate(authCtx, key)
	cancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			httpx.Error(w, http.StatusServiceUnavailable, httpx.CodeInternal, "authentication temporarily unavailable")
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="rateguard"`)
		httpx.Error(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "invalid API key")
		return
	}

	start := time.Now()
	decision := s.limiter.Allow(tenant.TenantID, tenant.LimitRPM, tenant.Burst)
	info := rateInfo{
		limit:     strconv.Itoa(decision.Limit),
		remaining: strconv.Itoa(decision.Remaining),
		reset:     strconv.FormatInt(time.Now().Add(decision.ResetAfter).Unix(), 10),
	}
	if !decision.Allowed {
		s.usage.Record(tenant.TenantID, false, time.Since(start))
		applyRateHeaders(w.Header(), context.WithValue(r.Context(), rateInfoKey{}, info))
		retry := int64(decision.RetryAfter.Seconds() + 0.999)
		if retry < 1 {
			retry = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(retry, 10))
		w.Header().Set("Cache-Control", "no-store")
		httpx.Error(w, http.StatusTooManyRequests, httpx.CodeRateLimited, "rate limit exceeded")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	ctx := context.WithValue(r.Context(), rateInfoKey{}, info)
	defer func() {
		s.usage.Record(tenant.TenantID, true, time.Since(start))
	}()
	s.proxy.ServeHTTP(w, r.WithContext(ctx))
}

func applyRateHeaders(h http.Header, ctx context.Context) {
	info, ok := ctx.Value(rateInfoKey{}).(rateInfo)
	if !ok {
		return
	}
	h.Set("X-RateLimit-Limit", info.limit)
	h.Set("X-RateLimit-Remaining", info.remaining)
	h.Set("X-RateLimit-Reset", info.reset)
}
