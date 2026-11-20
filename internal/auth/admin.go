package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
)

// AdminVerifier verifies the dashboard/API admin key without retaining the
// raw value in memory.
type AdminVerifier struct {
	hash    [32]byte
	enabled bool
}

// NewAdminVerifier creates a verifier for key. Short keys are rejected so a
// misconfiguration cannot leave the admin API protected by a trivial secret.
func NewAdminVerifier(key string) (*AdminVerifier, error) {
	if len(key) < 16 {
		return nil, errors.New("admin key must be at least 16 characters")
	}
	return &AdminVerifier{hash: sha256.Sum256([]byte(key)), enabled: true}, nil
}

// Verify checks candidate in constant time. An unconfigured verifier always
// rejects (fail closed).
func (v *AdminVerifier) Verify(candidate string) bool {
	if v == nil || !v.enabled || candidate == "" {
		return false
	}
	sum := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(v.hash[:], sum[:]) == 1
}

// String prevents accidental logging of the verifier's internals.
func (v *AdminVerifier) String() string {
	return fmt.Sprintf("AdminVerifier(enabled=%t)", v != nil && v.enabled)
}
