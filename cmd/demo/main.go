// Command demo is a tiny "Quote of the Day" HTTP upstream used behind the
// RateGuard proxy during local demos. It has no external dependencies, no
// secrets, and logs no request headers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// quote is the public JSON shape returned by GET /quote.
type quote struct {
	Quote  string `json:"quote"`
	Author string `json:"author"`
}

// quotes is a small, dependency-free selection of well-known quotations.
var quotes = []quote{
	{Quote: "Simplicity is the ultimate sophistication.", Author: "Leonardo da Vinci"},
	{Quote: "Talk is cheap. Show me the code.", Author: "Linus Torvalds"},
	{Quote: "Programs must be written for people to read, and only incidentally for machines to execute.", Author: "Harold Abelson"},
	{Quote: "Premature optimization is the root of all evil.", Author: "Donald Knuth"},
	{Quote: "Make it work, make it right, make it fast.", Author: "Kent Beck"},
	{Quote: "The best way to predict the future is to invent it.", Author: "Alan Kay"},
	{Quote: "Any sufficiently advanced technology is indistinguishable from magic.", Author: "Arthur C. Clarke"},
	{Quote: "First, solve the problem. Then, write the code.", Author: "John Johnson"},
	{Quote: "Experience is the name everyone gives to their mistakes.", Author: "Oscar Wilde"},
	{Quote: "Quality is not an act, it is a habit.", Author: "Aristotle"},
	{Quote: "The journey of a thousand miles begins with a single step.", Author: "Lao Tzu"},
	{Quote: "Keep it simple, stupid.", Author: "Kelly Johnson"},
	{Quote: "If you can't explain it simply, you don't understand it well enough.", Author: "Albert Einstein"},
	{Quote: "The only way to do great work is to love what you do.", Author: "Steve Jobs"},
}

// main starts the demo server and shuts it down gracefully on SIGINT or
// SIGTERM. PORT defaults to 8090.
func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	srv := &http.Server{
		Addr:              ":" + port(),
		Handler:           securityHeaders(routes(time.Now)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("demo upstream listening", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("demo upstream failed", "error", err)
			os.Exit(1)
		}
		return
	case <-ctx.Done():
		logger.Info("shutting down demo upstream")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("demo upstream stopped with error", "error", err)
		os.Exit(1)
	}
}

// routes builds the demo handler. now is injectable so the quote of the day is
// deterministic in tests.
func routes(now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /quote", quoteOfTheDay(now))
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /{$}", home)
	return mux
}

// quoteOfTheDay returns the quotation for the current UTC day.
func quoteOfTheDay(now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		day := now().UTC().YearDay()
		writeJSON(w, http.StatusOK, quotes[day%len(quotes)])
	}
}

// health reports liveness.
func health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// home serves a minimal HTML landing page linking to the quote endpoint.
func home(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Quote of the Day</title></head>
<body>
<main>
<h1>Quote of the Day</h1>
<p><a href="/quote">Get today's quote (JSON)</a></p>
<p><a href="/healthz">Health check</a></p>
</main>
</body>
</html>
`))
}

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// securityHeaders applies conservative response headers to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// port returns the listen port from PORT, defaulting to 8090.
func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8090"
}
