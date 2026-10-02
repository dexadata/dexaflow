package main

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestNewTrustedIssuerFollowsTheConfig locks the wiring (#1284): no issuer
// configured means no verifier, so the handoff route is never registered; a
// configured one yields a verifier keyed by the configured name.
func TestNewTrustedIssuerFollowsTheConfig(t *testing.T) {
	cfg := &config.ServerConfig{}
	if got := newTrustedIssuer(context.Background(), cfg); got != nil {
		t.Fatalf("newTrustedIssuer(unset) = %v, want nil", got)
	}

	cfg.Auth.TrustedIssuer = config.TrustedIssuerSection{
		Name: "portal", Issuer: "https://portal.example.com", JWKSURL: "https://portal.example.com/jwks",
		Audience: "leoflow-engine", TenantClaim: "tenant_id", AllowedTenants: []string{"*"},
		AllowedOrigins: []string{"https://portal.example.com"},
	}
	got := newTrustedIssuer(context.Background(), cfg)

	if got == nil || got.Provider() != "issuer:portal" {
		t.Fatalf("newTrustedIssuer = %v, want a verifier for issuer:portal", got)
	}
}
