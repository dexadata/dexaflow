// Package apitest builds the API server for the tests of other packages.
package apitest

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
)

// NewServer builds the server from deps as api.NewServer does, after filling
// in the dependencies every server requires that deps leaves out: a logger
// that discards, an authenticator that refuses every token and credential,
// and a login rate limiter. A test sets the ones it exercises itself. It
// panics when deps still misses a dependency a feature it turns on needs.
func NewServer(deps api.Dependencies) *gin.Engine {
	srv, err := api.NewServer(WithDefaults(deps))
	if err != nil {
		panic(err)
	}
	return srv
}

// WithDefaults returns deps with the defaults NewServer fills in.
func WithDefaults(deps api.Dependencies) api.Dependencies {
	if deps.Observability.Logger == nil {
		deps.Observability.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if deps.Auth.Authenticator == nil {
		deps.Auth.Authenticator = refuseAll{}
	}
	if deps.Auth.RateLimiter == nil {
		deps.Auth.RateLimiter = auth.NewRateLimiter(5, time.Minute)
	}
	return deps
}

// refuseAll is an authenticator that accepts nothing.
type refuseAll struct{}

// Authenticate refuses every token.
func (refuseAll) Authenticate(context.Context, string) (*auth.User, error) {
	return nil, auth.ErrInvalidToken
}

// IssueToken refuses every credential.
func (refuseAll) IssueToken(context.Context, auth.Credentials) (string, error) {
	return "", auth.ErrInvalidCredentials
}
