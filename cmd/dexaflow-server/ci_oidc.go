package main

import (
	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/cioidc"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/observability"
)

// withCIOIDC turns on the CI OIDC exchange (ADR 0092) in deps when
// auth.ci_oidc lists an issuer, and returns deps unchanged otherwise: the
// verifier serves the exchange and the deploy defaults, and the request
// authenticator learns to resolve the sessions it issues, in front of the
// one deps already has. roles reads the role a rule names; m counts the
// exchanges (nil: not counted). It makes no network call.
func withCIOIDC(deps api.Dependencies, cfg *config.ServerConfig, roles auth.RolePermissionStore, m *observability.Metrics) api.Dependencies {
	if !cfg.Auth.CIOIDC.Enabled() {
		return deps
	}
	v := cioidc.New(cfg.Auth.CIOIDC.CIIssuers())
	deps.Auth.Authenticator = auth.NewCISessionAuthenticator(deps.Auth.Authenticator, cfg.Auth.JWT.Secret, v, roles)
	deps.Issuers.CIOIDC = v
	deps.Issuers.CIOIDCRoles = roles
	deps.Issuers.CIOIDCSessionTTL = cfg.Auth.CIOIDC.SessionTTL()
	if m != nil {
		deps.Issuers.CIOIDCExchanges = m
	}
	return deps
}
