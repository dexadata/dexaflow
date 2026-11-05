package serverapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
)

// requestAuthenticator is the authenticator the API checks requests with: the
// engine's own JWT authenticator, with one passed as an option behind it
// (ADR 0099 §3). The API tokens and CI sessions are put in front of either
// later, by withAPITokens and withCIOIDC.
func requestAuthenticator(engine *auth.JWTAuthenticator, fromOptions auth.Authenticator, logger *slog.Logger) auth.Authenticator {
	if fromOptions == nil {
		return engine
	}
	logger.Info("auth: authenticator from options, password sign-in goes to it", "type", fmt.Sprintf("%T", fromOptions))
	return optionAuthenticator{engine: engine, custom: fromOptions}
}

// optionAuthenticator checks a token as one of the engine's own first (the
// sessions an SSO login, a trusted-issuer handoff or a renewal mints), then
// as one of the authenticator passed as an option. Password sign-in goes to
// the option's authenticator only.
//
// A user the option's authenticator returns cannot carry what only the
// server grants (ADR 0099 §6): Scoped, Scopes and DeployAudience are cleared,
// so such a principal is authorized by its roles alone and can never pass
// for a trusted-issuer deploy bearer.
type optionAuthenticator struct {
	engine auth.Authenticator
	custom auth.Authenticator
}

// errNoUser is a nil user returned with a nil error.
var errNoUser = fmt.Errorf("%w: the authenticator from options returned no user", auth.ErrInvalidToken)

// Authenticate implements auth.Authenticator.
func (a optionAuthenticator) Authenticate(ctx context.Context, token string) (*auth.User, error) {
	user, err := a.engine.Authenticate(ctx, token)
	if err == nil || !errors.Is(err, auth.ErrInvalidToken) {
		return user, err
	}
	user, err = a.custom.Authenticate(ctx, token)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errNoUser
	}
	return withoutServerGrants(user), nil
}

// IssueToken implements auth.Authenticator.
func (a optionAuthenticator) IssueToken(ctx context.Context, creds auth.Credentials) (string, error) {
	return a.custom.IssueToken(ctx, creds)
}

// withoutServerGrants returns a copy of u without the fields only the server
// sets, sharing no slice with u, so the authenticator that returned u cannot
// change the principal while a request uses it.
func withoutServerGrants(u *auth.User) *auth.User {
	c := *u
	c.Roles = slices.Clone(u.Roles)
	c.Permissions = slices.Clone(u.Permissions)
	c.Scoped = false
	c.Scopes = nil
	c.DeployAudience = false
	c.SessionsValidAfter = time.Time{}
	return &c
}

// errIssuerOrigins and errIssuerTwice are the startup refusals of a trusted
// issuer passed as an option.
var (
	errIssuerOrigins = errors.New("a trusted issuer passed as an option needs one or more allowed origins, each scheme://host[:port] with no path")
	errIssuerTwice   = errors.New("a trusted issuer is passed as an option and auth.trusted_issuer is configured too; keep one")
)

// validateAPIOptions refuses option combinations the API could not serve
// safely: a trusted issuer with no valid origin to accept a handoff from,
// which would refuse every sign-in, and a trusted issuer from options next to the
// configured one, whose host pattern, deploy audience and bearer settings
// would then apply to an issuer they were not written for.
func validateAPIOptions(cfg *config.ServerConfig, issuer api.TrustedIssuer, origins []string) error {
	if issuer == nil {
		return nil
	}
	if cfg.Auth.TrustedIssuer.Enabled() {
		return errIssuerTwice
	}
	if !config.ValidOrigins(origins) {
		return errIssuerOrigins
	}
	return nil
}
