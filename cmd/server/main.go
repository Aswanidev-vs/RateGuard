// Command server runs the RateGuard API, proxy, and dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Aswanidev-vs/RateGuard/internal/auth"
	"github.com/Aswanidev-vs/RateGuard/internal/config"
	"github.com/Aswanidev-vs/RateGuard/internal/limiter"
	"github.com/Aswanidev-vs/RateGuard/internal/server"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
	"github.com/Aswanidev-vs/RateGuard/internal/usage"
	"github.com/Aswanidev-vs/RateGuard/web"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local health endpoint and exit")
	flag.Parse()

	port := envOr("PORT", "8080")
	if *healthcheck {
		if err := probeHealth(port); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck:", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fatal("load config", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)

	initCtx, cancelInit := context.WithTimeout(context.Background(), 30*time.Second)
	if err := cfg.LoadSSM(initCtx); err != nil {
		cancelInit()
		fatal("load SSM parameters", err)
	}
	cancelInit()
	if err := cfg.Validate(); err != nil {
		fatal("invalid config", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := buildStore(ctx, cfg, logger)
	if err != nil {
		fatal("initialize store", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Warn("close store", "error", err)
		}
	}()

	admin, err := auth.NewAdminVerifier(cfg.AdminAPIKey)
	if err != nil {
		fatal("admin key", err)
	}
	tenants := auth.NewTenantAuthenticator(st, 30*time.Second)
	lim := limiter.New()
	renderer, err := web.NewRenderer(logger)
	if err != nil {
		fatal("parse templates", err)
	}
	recorder := usage.New(st, cfg.UsageBatchSize, cfg.UsageFlushInterval, logger)
	recorder.Start(ctx)

	srv, err := server.New(server.Options{
		Config:   cfg,
		Store:    st,
		Limiter:  lim,
		Tenants:  tenants,
		Admin:    admin,
		Usage:    recorder,
		Renderer: renderer,
		Logger:   logger,
		Version:  version,
	})
	if err != nil {
		fatal("initialize server", err)
	}

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0, // proxied responses may stream
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	logger.Info("rateguard starting",
		"version", version,
		"commit", commit,
		"addr", httpServer.Addr,
		"store", cfg.StoreBackend,
		"demo_enabled", cfg.DemoEnabled,
		"upstream_configured", cfg.UpstreamURL != "",
		"cookie_secure", cfg.CookieSecure,
	)

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "error", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown", "error", err)
	}
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelFlush()
	if err := recorder.Shutdown(flushCtx); err != nil {
		logger.Warn("flush usage recorder", "error", err)
	}
	logger.Info("rateguard stopped")
}

func buildStore(ctx context.Context, cfg *config.Config, logger *slog.Logger) (store.Store, error) {
	if cfg.StoreBackend == "memory" {
		logger.Warn("using in-memory store; tenants and usage are lost on restart")
		return store.NewMemory(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return store.NewDynamo(ctx, store.DynamoOptions{
		Region:         cfg.AWSRegion,
		Endpoint:       cfg.DynamoEndpoint,
		TenantsTable:   cfg.TenantsTable,
		UsageTable:     cfg.UsageTable,
		AnomaliesTable: cfg.AnomaliesTable,
	})
}

func probeHealth(port string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func logLevel(value string) slog.Level {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func fatal(message string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", message, err)
	os.Exit(1)
}
