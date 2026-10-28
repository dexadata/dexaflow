package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// APITokenPrefix starts every API token (ADR 0074). It tells an API token
// apart from a JWT without parsing it, and lets secret scanners recognize a
// leaked one.
const APITokenPrefix = "dxf_"

// apiTokenSecretBytes is the entropy of an API token: 256 random bits.
const apiTokenSecretBytes = 32

// ErrAPITokenNotFound is returned by APITokenStore.FindAPITokenByHash when no
// token has the given hash.
var ErrAPITokenNotFound = errors.New("api token not found")

// APIToken is the stored record of a long-lived API token (ADR 0074). It
// never holds the token itself, only what describes it.
type APIToken struct {
	ID string
	// TenantID is the tenant name of the owner, as in User.TenantID.
	TenantID   string
	UserID     string
	UserEmail  string
	Name       string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// APITokenStore resolves API tokens by the hash of their secret and records
// their use. storage implements it.
type APITokenStore interface {
	// FindAPITokenByHash returns the token whose secret hashes to hash, revoked
	// and expired ones included, or ErrAPITokenNotFound.
	FindAPITokenByHash(ctx context.Context, hash string) (*APIToken, error)
	// TouchAPIToken records that the token was used at at, unless its last
	// recorded use is less than five minutes before at, and reports whether
	// it wrote. The store decides, so concurrent requests write once.
	TouchAPIToken(ctx context.Context, id string, at time.Time) (bool, error)
}

// apiTokenScopeSets maps the scope a caller asks for when creating a token to
// the scopes the token carries. A deploy token carries dexaflow:deploy alone:
// the DAG reads a deploy needs accept that scope, and every other read its
// owner may have stays closed to it (ADR 0074).
var apiTokenScopeSets = map[string][]string{
	"read":   {ScopeRead},
	"deploy": {ScopeDeploy},
}

// APITokenScopes returns the scopes an API token created for scope carries,
// and false when scope is not one a token may be created for.
func APITokenScopes(scope string) ([]string, bool) {
	set, ok := apiTokenScopeSets[scope]
	if !ok {
		return nil, false
	}
	return append([]string(nil), set...), true
}

// NewAPIToken returns a fresh API token and the hash to store for it. The
// token is shown to its owner once and never stored.
func NewAPIToken() (token, hash string, err error) {
	b := make([]byte, apiTokenSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generating api token: %w", err)
	}
	token = APITokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashAPIToken(token), nil
}

// HashAPIToken returns the hex SHA-256 of token, the only form of it the
// server keeps. A fast hash is enough: the secret is 256 random bits, so
// there is no dictionary to try, and the hash is the lookup key.
func HashAPIToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IsAPIToken reports whether token has the API token form.
func IsAPIToken(token string) bool {
	return strings.HasPrefix(token, APITokenPrefix)
}

// APITokenAuthenticator authenticates API tokens and hands every other token
// to the next Authenticator (ADR 0074). An API token resolves to its owner,
// reloaded from the store on every request like a JWT subject, narrowed to
// the token's scopes.
type APITokenAuthenticator struct {
	next   Authenticator
	tokens APITokenStore
	users  UserStore
	now    func() time.Time
}

// NewAPITokenAuthenticator wraps next so that API tokens are checked against
// tokens and their owners reloaded from users.
func NewAPITokenAuthenticator(next Authenticator, tokens APITokenStore, users UserStore) *APITokenAuthenticator {
	return &APITokenAuthenticator{next: next, tokens: tokens, users: users, now: time.Now}
}

// IssueToken delegates to the next authenticator: API tokens are created
// through the API by a signed-in user, never from a password.
func (a *APITokenAuthenticator) IssueToken(ctx context.Context, creds Credentials) (string, error) {
	return a.next.IssueToken(ctx, creds)
}

// Authenticate resolves an API token to its scoped owner, or delegates any
// other token. A token that is unknown, revoked or expired, or whose owner is
// missing, inactive or in another tenant, is ErrInvalidToken. A store failure
// is returned as it is, so the request answers 503 rather than 401.
func (a *APITokenAuthenticator) Authenticate(ctx context.Context, token string) (*User, error) {
	if !IsAPIToken(token) {
		return a.next.Authenticate(ctx, token)
	}
	rec, err := a.tokens.FindAPITokenByHash(ctx, HashAPIToken(token))
	if err != nil {
		if errors.Is(err, ErrAPITokenNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, fmt.Errorf("looking up api token: %w", err)
	}
	now := a.now()
	if rec.RevokedAt != nil || !now.Before(rec.ExpiresAt) {
		return nil, ErrInvalidToken
	}
	user, err := a.owner(ctx, rec)
	if err != nil {
		return nil, err
	}
	user.Scoped = true
	user.Scopes = append([]string(nil), rec.Scopes...)
	a.touch(ctx, rec, now)
	return user, nil
}

// owner reloads the token's owner and checks it may still act: active, and
// in the tenant the token was created in.
func (a *APITokenAuthenticator) owner(ctx context.Context, rec *APIToken) (*User, error) {
	user, active, err := a.users.FindUserByID(ctx, rec.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, errors.Join(ErrInvalidToken, err)
		}
		return nil, fmt.Errorf("loading api token owner: %w", err)
	}
	if !active {
		return nil, ErrInvalidToken
	}
	if user.TenantID == "" || rec.TenantID == "" {
		return nil, errors.Join(ErrInvalidToken, ErrTenantlessToken)
	}
	if user.TenantID != rec.TenantID {
		return nil, ErrInvalidToken
	}
	return user, nil
}

// touch records the use; the store skips the write when the last recorded
// use is recent. It is best effort: a failed write is logged and the request
// goes on.
func (a *APITokenAuthenticator) touch(ctx context.Context, rec *APIToken, now time.Time) {
	if _, err := a.tokens.TouchAPIToken(ctx, rec.ID, now); err != nil {
		slog.WarnContext(ctx, "recording api token use", "token_id", rec.ID, "error", err)
	}
}
