package tenantctx

import (
	"context"
	"testing"
)

func TestWithFromWithout(t *testing.T) {
	ctx := context.Background()
	if From(ctx) != "" {
		t.Fatal("a bare context carries a tenant")
	}
	if With(ctx, "") != ctx {
		t.Fatal("an empty name must leave the context alone")
	}
	bound := With(ctx, "acme")
	if From(bound) != "acme" {
		t.Fatalf("From = %q", From(bound))
	}
	if From(Without(bound)) != "" {
		t.Fatal("Without must clear the tenant")
	}
	if Without(ctx) != ctx {
		t.Fatal("Without on an unbound context must leave it alone")
	}
}
