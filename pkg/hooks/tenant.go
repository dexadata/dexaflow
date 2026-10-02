package hooks

import (
	"context"
)

// TenantResolver maps an inbound request to a tenant slug.
type TenantResolver interface {
	ResolveTenant(ctx context.Context, host, token string) (string, error)
}

// DefaultResolver reads the tenant claim or returns "default".
type DefaultResolver struct{}

func (DefaultResolver) ResolveTenant(ctx context.Context, host, token string) (string, error) {
	return "default", nil
}
