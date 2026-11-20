package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookieName = "rg_session"

// sessionManager issues stateless, HMAC-signed dashboard sessions. The token
// proves only that the admin key was verified at login time; no tenant data is
// stored in it.
type sessionManager struct {
	key []byte
	ttl time.Duration
}

func newSessionManager(secret string, ttl time.Duration) *sessionManager {
	sum := sha256.Sum256([]byte("rateguard-session|" + secret))
	return &sessionManager{key: sum[:], ttl: ttl}
}

func (m *sessionManager) create(now time.Time) (string, time.Time, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, err
	}
	expires := now.Add(m.ttl)
	payload := fmt.Sprintf("v1.%d.%s", expires.Unix(), base64.RawURLEncoding.EncodeToString(nonce))
	return payload + "." + base64.RawURLEncoding.EncodeToString(m.sign(payload)), expires, nil
}

func (m *sessionManager) verify(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || !now.Before(time.Unix(exp, 0)) {
		return false
	}
	payload := strings.Join(parts[:3], ".")
	want := m.sign(payload)
	got, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (m *sessionManager) csrfFor(token string) string {
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte("csrf|" + token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *sessionManager) validCSRF(token, candidate string) bool {
	if token == "" || candidate == "" {
		return false
	}
	want := m.csrfFor(token)
	return subtle.ConstantTimeCompare([]byte(want), []byte(candidate)) == 1
}

func (m *sessionManager) sign(payload string) []byte {
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (m *sessionManager) setCookie(w http.ResponseWriter, token string, expires time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (m *sessionManager) clearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}
