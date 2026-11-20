// Package auth handles admin keys, tenant API keys, and authentication
// throttling.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// KeyPrefix is the non-secret, human-readable part of every tenant API key.
const KeyPrefix = "ag_live_"

// prefixSecretLen is the number of secret characters stored in the lookup
// prefix. All keys share the literal KeyPrefix, so including secret characters
// is required to keep the DynamoDB GSI selective.
const prefixSecretLen = 8

// Credentials is the raw key (shown once) plus its stored lookup material.
type Credentials struct {
	Raw    string
	Hash   string
	Prefix string
}

// GenerateAPIKey creates a cryptographically random tenant API key.
func GenerateAPIKey() (Credentials, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Credentials{}, err
	}
	raw := KeyPrefix + base64.RawURLEncoding.EncodeToString(secret)
	return Credentials{Raw: raw, Hash: Hash(raw), Prefix: Prefix(raw)}, nil
}

// Hash returns the lowercase hex SHA-256 digest of raw. Tenant keys are
// 256-bit random values, so a fast hash is appropriate; bcrypt buys nothing
// here and would break prefix-index lookups.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Prefix returns the display/lookup prefix for a key.
func Prefix(raw string) string {
	if !strings.HasPrefix(raw, KeyPrefix) {
		return ""
	}
	secret := raw[len(KeyPrefix):]
	if len(secret) > prefixSecretLen {
		secret = secret[:prefixSecretLen]
	}
	return KeyPrefix + secret
}

// Verify compares a presented key to a stored hash in constant time.
func Verify(raw, storedHash string) bool {
	if raw == "" || storedHash == "" || !strings.HasPrefix(raw, KeyPrefix) {
		return false
	}
	got := Hash(raw)
	if len(got) != len(storedHash) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(storedHash)) == 1
}

// ErrMalformedKey is returned for keys that cannot be tenant API keys.
var ErrMalformedKey = errors.New("malformed API key")
