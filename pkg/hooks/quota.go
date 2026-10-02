package hooks

import (
	"context"
)

// QuotaEnforcer gates actions based on plan limits.
type QuotaEnforcer interface {
	CheckDagRunQuota(ctx context.Context, tenantID string) error
	CheckActiveDagQuota(ctx context.Context, tenantID string) error
	CheckUserQuota(ctx context.Context, tenantID string) error
}

// NoOpEnforcer permits all actions.
type NoOpEnforcer struct{}

func (NoOpEnforcer) CheckDagRunQuota(ctx context.Context, tenantID string) error {
	return nil
}

func (NoOpEnforcer) CheckActiveDagQuota(ctx context.Context, tenantID string) error {
	return nil
}

func (NoOpEnforcer) CheckUserQuota(ctx context.Context, tenantID string) error {
	return nil
}
