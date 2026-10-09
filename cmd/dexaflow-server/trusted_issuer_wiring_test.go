package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/issuer"
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

// TestIssuerBearerFollowsTheConfig locks the #1468 wiring: the bearer mode is
// on only when bearer audiences are configured, and the verifier it uses is
// built with them.
func TestIssuerBearerFollowsTheConfig(t *testing.T) {
	cfg := &config.ServerConfig{}
	if got := issuerBearer(newTrustedIssuer(context.Background(), cfg)); got != nil {
		t.Fatalf("issuerBearer(no issuer) = %v, want nil", got)
	}
	cfg.Auth.TrustedIssuer = config.TrustedIssuerSection{
		Name: "portal", Issuer: "https://portal.example.com", JWKSURL: "https://portal.example.com/jwks",
		Audience: "leoflow-engine", TenantClaim: "tenant_id", AllowedTenants: []string{"*"},
		AllowedOrigins: []string{"https://portal.example.com"},
	}
	if got := issuerBearer(newTrustedIssuer(context.Background(), cfg)); got != nil {
		t.Fatalf("issuerBearer(no bearer audiences) = %v, want nil", got)
	}

	cfg.Auth.TrustedIssuer.BearerAudiences = []string{"leoflow-mcp"}
	got := issuerBearer(newTrustedIssuer(context.Background(), cfg))

	if got == nil || got.Provider() != "issuer:portal" {
		t.Fatalf("issuerBearer = %v, want the issuer:portal verifier", got)
	}
}

// TestTrustedIssuerConfigMapsEveryKey locks that each auth.trusted_issuer key
// reaches the verifier, the second-based lifetimes as durations.
func TestTrustedIssuerConfigMapsEveryKey(t *testing.T) {
	s := config.TrustedIssuerSection{
		Name: "portal", Issuer: "https://portal.example.com", JWKSURL: "https://portal.example.com/jwks",
		Audience: "leoflow-engine", TenantClaim: "org", AllowedTenants: []string{"acme"},
		MaxLifetimeSeconds: 90, BearerAudiences: []string{"leoflow-mcp"}, BearerMaxLifetimeSeconds: 300,
	}

	got := trustedIssuerConfig(s)

	want := issuer.Config{
		Name: "portal", Issuer: "https://portal.example.com", JWKSURL: "https://portal.example.com/jwks",
		Audience: "leoflow-engine", TenantClaim: "org", AllowedTenants: []string{"acme"},
		MaxLifetime: 90 * time.Second, BearerAudiences: []string{"leoflow-mcp"}, BearerMaxLifetime: 5 * time.Minute,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("trustedIssuerConfig = %+v, want %+v", got, want)
	}
}
