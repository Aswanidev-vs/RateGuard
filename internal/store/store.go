// Package store defines the persistence boundary and its implementations.
package store

import (
	"context"
	"errors"

	"github.com/Aswanidev-vs/RateGuard/internal/model"
)

var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a conditional write loses a race.
	ErrConflict = errors.New("conflict")
)

// Store is the persistence contract used by the API, dashboard, and cost
// worker. Both the in-memory and DynamoDB implementations satisfy it.
type Store interface {
	CreateTenant(ctx context.Context, t *model.Tenant) error
	GetTenant(ctx context.Context, id string) (*model.Tenant, error)
	FindTenantsByKeyPrefix(ctx context.Context, prefix string) ([]*model.Tenant, error)
	ListTenants(ctx context.Context) ([]*model.Tenant, error)
	UpdateTenant(ctx context.Context, t *model.Tenant) error
	AddUsage(ctx context.Context, entries []model.UsageEntry) error
	GetUsage(ctx context.Context, tenantID, fromBucket, toBucket string) (model.UsageAggregate, error)
	PutAnomalyIfAbsent(ctx context.Context, a *model.Anomaly) (bool, error)
	MarkAnomalyAlerted(ctx context.Context, tenantID, detectedAt string) error
	ListAnomalies(ctx context.Context, limit int) ([]*model.Anomaly, error)
	Ping(ctx context.Context) error
	Close() error
}
