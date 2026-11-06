// Package tenantctx carries the tenant a piece of work runs for on its
// context, so the storage layer can bind database connections to it (row
// level security, ADR 0104) without the API depending on storage.
package tenantctx

import "context"

type key struct{}

// With returns ctx carrying the tenant, by name, the work on it is for. An
// empty name leaves ctx as it is.
func With(ctx context.Context, tenant string) context.Context {
	if tenant == "" {
		return ctx
	}
	return context.WithValue(ctx, key{}, tenant)
}

// Without returns ctx with no tenant, for work a request of one tenant starts
// that legitimately reads across tenants.
func Without(ctx context.Context) context.Context {
	if From(ctx) == "" {
		return ctx
	}
	return context.WithValue(ctx, key{}, "")
}

// From reports the tenant ctx carries, "" for none.
func From(ctx context.Context) string {
	t, _ := ctx.Value(key{}).(string)
	return t
}
