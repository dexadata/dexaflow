package api

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// newTestServer builds the server from deps as NewServer does, after filling
// in the dependencies every server requires that deps leaves out: a logger
// that discards, an authenticator that refuses every token and credential,
// and a login rate limiter. It is internal/api/apitest.NewServer for the
// tests of this package, which cannot import apitest. It panics when deps
// still misses a dependency a feature it turns on needs.
func newTestServer(deps Dependencies) *gin.Engine {
	srv, err := NewServer(withTestDefaults(deps))
	if err != nil {
		panic(err)
	}
	return srv
}

// withTestDefaults returns deps with the defaults newTestServer fills in.
func withTestDefaults(deps Dependencies) Dependencies {
	if deps.Observability.Logger == nil {
		deps.Observability.Logger = discardLogger()
	}
	if deps.Auth.Authenticator == nil {
		deps.Auth.Authenticator = refuseAllAuthn{}
	}
	if deps.Auth.RateLimiter == nil {
		deps.Auth.RateLimiter = auth.NewRateLimiter(5, time.Minute)
	}
	return deps
}

// refuseAllAuthn is an authenticator that accepts nothing.
type refuseAllAuthn struct{}

func (refuseAllAuthn) Authenticate(context.Context, string) (*auth.User, error) {
	return nil, auth.ErrInvalidToken
}

func (refuseAllAuthn) IssueToken(context.Context, auth.Credentials) (string, error) {
	return "", auth.ErrInvalidCredentials
}
