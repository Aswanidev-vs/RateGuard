package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.html static/*
var assets embed.FS

var pages = []string{"login", "overview", "tenants", "anomalies", "help", "error"}

// Renderer renders the embedded server-side templates.
type Renderer struct {
	pages    map[string]*template.Template
	partials *template.Template
	log      *slog.Logger
}

// NewRenderer parses the layout, partials, and each page into independent
// template sets so page content definitions cannot collide.
func NewRenderer(log *slog.Logger) (*Renderer, error) {
	r := &Renderer{pages: make(map[string]*template.Template, len(pages)), log: log}
	partials, err := template.New("partials.html").Funcs(funcMap()).ParseFS(assets, "templates/partials.html")
	if err != nil {
		return nil, fmt.Errorf("parse partials: %w", err)
	}
	r.partials = partials
	for _, page := range pages {
		t, err := template.New("layout.html").Funcs(funcMap()).ParseFS(assets,
			"templates/layout.html",
			"templates/partials.html",
			"templates/"+page+".html",
		)
		if err != nil {
			return nil, fmt.Errorf("parse %s template: %w", page, err)
		}
		r.pages[page] = t
	}
	return r, nil
}

// Render executes a page into a buffer before writing, so template failures do
// not produce a half-written response.
func (r *Renderer) Render(w http.ResponseWriter, status int, page string, data any) {
	t, ok := r.pages[page]
	if !ok {
		r.log.Error("unknown template page", "page", page)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		r.log.Error("render template", "page", page, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// RenderPartial executes a named partial from partials.html.
func (r *Renderer) RenderPartial(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := r.partials.ExecuteTemplate(&buf, name, data); err != nil {
		r.log.Error("render partial", "partial", name, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// Static serves the embedded static assets with conservative caching.
func (r *Renderer) Static() http.Handler {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic("web: static assets are embedded incorrectly: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/" || strings.Contains(req.URL.Path, "..") {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasSuffix(req.URL.Path, ".min.js") {
			w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		fileServer.ServeHTTP(w, req)
	})
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"humanCount": humanCount,
		"pct":        func(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) + "%" },
		"add":        func(a, b int) int { return a + b },
		"add64":      func(a, b int64) int64 { return a + b },
		"pctOf": func(part, total int64) float64 {
			if total <= 0 {
				return 0
			}
			return float64(part) / float64(total) * 100
		},
		"timeShort": func(s string) string {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return s
			}
			return t.Local().Format("02 Jan 15:04")
		},
		"bucketLabel": func(s string) string {
			if len(s) == len("2006-01-02T15") {
				return s[11:16]
			}
			return s
		},
		"severityClass": func(s string) string {
			switch s {
			case "high":
				return "sev-high"
			case "med":
				return "sev-med"
			default:
				return "sev-low"
			}
		},
		"initial": func(s string) string {
			if s == "" {
				return "?"
			}
			return strings.ToUpper(s[:1])
		},
		"lower": strings.ToLower,
	}
}

func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1_000, 'f', 1, 64) + "k"
	default:
		return strconv.FormatInt(n, 10)
	}
}
