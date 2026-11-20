package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Aswanidev-vs/RateGuard/internal/auth"
	"github.com/Aswanidev-vs/RateGuard/internal/model"
	"github.com/Aswanidev-vs/RateGuard/internal/store"
)

const (
	maxNameRunes  = 100
	maxEmailBytes = 254
	maxRateLimit  = 1_000_000
)

func validateCreate(req model.CreateTenantRequest) (model.CreateTenantRequest, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)
	if err := validateName(req.Name); err != nil {
		return req, err
	}
	if err := validateEmail(req.Email); err != nil {
		return req, err
	}
	if err := validateLimits(req.LimitRPM, req.Burst); err != nil {
		return req, err
	}
	return req, nil
}

func validateName(name string) error {
	if !utf8.ValidString(name) {
		return errors.New("name must be valid UTF-8")
	}
	n := utf8.RuneCountInString(name)
	if n < 1 || n > maxNameRunes {
		return fmt.Errorf("name must be 1-%d characters", maxNameRunes)
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("name must not contain control characters")
	}
	return nil
}

func validateEmail(email string) error {
	if len(email) < 3 || len(email) > maxEmailBytes {
		return fmt.Errorf("email must be 3-%d characters", maxEmailBytes)
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email {
		return errors.New("email must be a plain email address")
	}
	return nil
}

func validateLimits(limitRPM, burst int) error {
	if limitRPM < 1 || limitRPM > maxRateLimit {
		return fmt.Errorf("limit_rpm must be 1-%d", maxRateLimit)
	}
	if burst < 1 || burst > maxRateLimit {
		return fmt.Errorf("burst must be 1-%d", maxRateLimit)
	}
	return nil
}

func newTenantID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "tn_" + hex.EncodeToString(b), nil
}

// createTenantRecord generates a tenant and its API key, retrying the
// vanishingly unlikely tenant ID collision.
func (s *Server) createTenantRecord(ctx context.Context, req model.CreateTenantRequest) (*model.Tenant, string, error) {
	creds, err := auth.GenerateAPIKey()
	if err != nil {
		return nil, "", fmt.Errorf("generate API key: %w", err)
	}
	now := model.NowISO()
	for attempt := 0; attempt < 3; attempt++ {
		id, err := newTenantID()
		if err != nil {
			return nil, "", fmt.Errorf("generate tenant id: %w", err)
		}
		t := &model.Tenant{
			TenantID:     id,
			Name:         req.Name,
			Email:        req.Email,
			APIKeyHash:   creds.Hash,
			APIKeyPrefix: creds.Prefix,
			LimitRPM:     req.LimitRPM,
			Burst:        req.Burst,
			Enabled:      true,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.store.CreateTenant(ctx, t); err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			return nil, "", err
		}
		return t, creds.Raw, nil
	}
	return nil, "", errors.New("could not allocate a unique tenant id")
}
