// Package httpx contains small HTTP helpers shared by the API and dashboard.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Error codes used across the API.
const (
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeRateLimited  = "rate_limited"
	CodeInvalidInput = "invalid_input"
	CodeInternal     = "internal"
)

// ErrorBody is the documented error envelope.
type ErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// JSON writes a JSON response with a fixed content type.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes the documented error envelope.
func Error(w http.ResponseWriter, status int, code, message string) {
	JSON(w, status, ErrorBody{Error: code, Message: message})
}

// MaxBodyBytes caps JSON API request bodies.
const MaxBodyBytes = 1 << 20 // 1 MiB

// DecodeJSON strictly decodes one JSON object from the request body.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if !isJSON(r.Header.Get("Content-Type")) {
		return fmt.Errorf("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			return fmt.Errorf("request body too large")
		case errors.Is(err, io.EOF):
			return fmt.Errorf("request body is required")
		case strings.Contains(err.Error(), "unknown field"):
			return fmt.Errorf("request contains an unknown field")
		default:
			return fmt.Errorf("malformed JSON body")
		}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain a single JSON object")
	}
	return nil
}

func isJSON(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "application/json")
}
