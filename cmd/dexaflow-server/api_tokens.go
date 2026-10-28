package main

import (
	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
)

// apiTokenRepo is what the API tokens need from the storage repo: their own
// store, the routes' store and the users their owners are reloaded from.
type apiTokenRepo interface {
	auth.APITokenStore
	auth.UserStore
	api.TokenStore
}

// withAPITokens serves the API tokens (ADR 0074) unless
// auth.api_tokens.disabled is true. On, an API token is checked before every
// other bearer and the token routes and screen are registered. Off, neither:
// a dxf_ bearer reaches the next authenticator, which refuses it like any
// token it did not mint, and /api/v2/apiTokens is not served (#2056).
func withAPITokens(deps api.Dependencies, cfg *config.ServerConfig, repo apiTokenRepo) api.Dependencies {
	if cfg.Auth.APITokens.Disabled {
		deps.Stores.APITokens = nil
		return deps
	}
	deps.Auth.Authenticator = auth.NewAPITokenAuthenticator(deps.Auth.Authenticator, repo, repo)
	deps.Stores.APITokens = repo
	return deps
}
