package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const tokenIssuer = "leoflow"

// DevTokenSubject is the subject of the in-process token that `dexaflow lite` mints
// for its admin. It intentionally has no user row, so Authenticate trusts its
// signed claims as the ONLY subject exempt from the per-request DB authz reload.
const DevTokenSubject = "leoflow-dev"

// audienceUser scopes tokens minted for human and API users, distinguishing them
// from agent identity tokens (see audienceAgent).
const audienceUser = "leoflow-user"

// jwtClaims is the Dexaflow JWT payload.
type jwtClaims struct {
	TenantID string   `json:"tenant_id"`
	Email    string   `json:"email,omitempty"`
	Roles    []string `json:"roles"`
	// OriginIssuedAt records when this session's credential was FIRST minted (at
	// login). Unlike iat, it is preserved verbatim across renewals (RenewUserToken)
	// so the control plane can bound a session's TOTAL lifetime no matter how many
	// times its token is transparently re-minted, mirroring the agent token's oiat
	// (ADR 0055 Fix #4). Omitempty so a token minted before this field existed is
	// byte-identical; renewal then falls back to iat for such a token.
	OriginIssuedAt *jwt.NumericDate `json:"oiat,omitempty"`
	jwt.RegisteredClaims
}

// JWTAuthenticator issues and validates HS256 JWTs against a UserStore.
type JWTAuthenticator struct {
	store  UserStore
	secret []byte
	ttl    time.Duration
	// now is the clock used when stamping agent-token iat/exp/origin. It defaults
	// to time.Now and is overridden only in tests, so renewal exp/ceiling math is
	// deterministic.
	now func() time.Time
}

// NewJWTAuthenticator builds a JWTAuthenticator with the given user store,
// HS256 secret, and token lifetime.
func NewJWTAuthenticator(store UserStore, secret string, ttl time.Duration) *JWTAuthenticator {
	return &JWTAuthenticator{store: store, secret: []byte(secret), ttl: ttl, now: time.Now}
}

// clock returns the authenticator's time source, tolerating a zero value on an
// authenticator built as a struct literal (e.g. MintUserToken) rather than via
// the constructor.
func (a *JWTAuthenticator) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// MintUserToken signs a user JWT directly, without checking credentials against
// a store. It is for trusted in-process callers only — notably `dexaflow lite`,
// which runs its own control plane and must register DAGs without a login
// round-trip. The token validates under Authenticate using the same secret.
func MintUserToken(secret string, ttl time.Duration, user User) (string, error) {
	a := &JWTAuthenticator{secret: []byte(secret), ttl: ttl}
	return a.sign(&user)
}

// IssueToken validates the credentials against the store and returns a signed JWT.
func (a *JWTAuthenticator) IssueToken(ctx context.Context, creds Credentials) (string, error) {
	user, hash, err := a.store.FindUserByLogin(ctx, creds.Tenant, creds.Username)
	if err != nil {
		// Propagate as-is: the store returns ErrInvalidCredentials for a genuine
		// not-found/inactive user (→ 401), and a real backend error otherwise (→ the
		// handler's 5xx). Collapsing everything to ErrInvalidCredentials would report
		// a DB outage as "invalid credentials" (#843).
		return "", err
	}
	if !VerifyPassword(hash, creds.Password) {
		return "", ErrInvalidCredentials
	}
	return a.sign(user)
}

// Authenticate validates a bearer token and resolves the current principal.
// After the signature and registered claims check out, it reloads the user's
// roles, permissions, and active flag from the store keyed by the token subject
// (the user id). The store — not the token — is the source of truth for
// authorization: this is what makes non-admin role grants gate anything (the
// token carries roles but never permissions) and lets deactivating a user
// revoke their live tokens within the token TTL.
//
// Two cases fall back to the signed claims instead of the reload: a nil store
// (no data plane bound — the trusted in-process minting context) and a subject
// with no backing row (a directly-minted token, e.g. `dexaflow lite`). Any other
// store failure fails closed, so a flaky database cannot silently disable
// revocation.
func (a *JWTAuthenticator) Authenticate(ctx context.Context, token string) (*User, error) {
	var c jwtClaims
	parsed, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceUser), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return nil, errors.Join(ErrInvalidToken, err)
	}
	claimed := &User{ID: c.Subject, TenantID: c.TenantID, Email: c.Email, Roles: c.Roles}
	if a.store == nil {
		return claimed, nil
	}
	user, active, err := a.store.FindUserByID(ctx, c.Subject)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// A subject with no user row is trusted from its signed claims ONLY for
			// the in-process dev token (which has no DB row by design); any other
			// missing subject fails closed, so a hard-deleted user cannot keep
			// claimed roles until the token expires — deletion revokes at once, like
			// is_active=false.
			if c.Subject == DevTokenSubject {
				return claimed, nil
			}
			return nil, errors.Join(ErrInvalidToken, err)
		}
		// Any OTHER store failure means we could not DETERMINE whether this token
		// is valid — not that it is invalid. Propagating it unchanged is the rule
		// #843 set for IssueToken, and it applies here for the same reason: a
		// database outage answered as "invalid token" tells every client to
		// re-authenticate against the database that is already down, and tells
		// whoever is on call that they have an auth incident (#1087).
		return nil, err
	}
	if !active {
		return nil, ErrInvalidToken
	}
	return user, nil
}

// sign mints a fresh user token for a login: TTL is the authenticator's
// configured lifetime and the session origin (oiat) is anchored at now.
func (a *JWTAuthenticator) sign(user *User) (string, error) {
	return a.mintUserToken(user, a.ttl, a.clock())
}

// mintUserToken signs a user token whose iat/exp are anchored at now and whose
// preserved session origin (oiat) is `origin`. Login passes now as the origin; a
// renewal passes the incoming token's origin so it never advances. exp is always
// now+ttl — never accumulated onto the previous exp. It is the single write side
// shared by login and RenewUserToken, mirroring mintAgentToken.
func (a *JWTAuthenticator) mintUserToken(user *User, ttl time.Duration, origin time.Time) (string, error) {
	now := a.clock()
	c := jwtClaims{
		TenantID:       user.TenantID,
		Email:          user.Email,
		Roles:          user.Roles,
		OriginIssuedAt: jwt.NewNumericDate(origin),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			Issuer:    tokenIssuer,
			Audience:  jwt.ClaimStrings{audienceUser},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(a.secret)
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}
	return signed, nil
}

// RenewUserToken validates a still-valid user bearer token and re-mints it for the
// SAME principal (subject, tenant, email, roles) with a fresh short TTL, without
// ever changing what Authenticate verifies (signature, issuer, leoflow-user
// audience, HS256). It is the server half of transparent CLI token renewal (EKS
// validation aresta #5): the short access-token TTL still bounds a stolen token,
// while renewal keeps a genuinely live session working so a long dev session
// never has to `dexaflow auth login` again on the hour. It is modeled directly on
// RenewAgentToken.
//
// The session's original login time is preserved across every renewal (the oiat
// claim, falling back to iat for a token minted before that claim existed).
// maxLifetime is a hard ceiling on that total age: once the session has been
// alive longer than maxLifetime since first login, renewal is refused (ok=false,
// empty token, no error) so the user must re-authenticate. A non-positive
// maxLifetime disables the ceiling. exp is always now+ttl — never accumulated.
//
// An invalid incoming token (bad signature, wrong audience, expired) returns an
// error and is never re-minted.
//
// Renewal RE-PROVES the principal: after the claims check and the ceiling check
// it reloads the user from the store, exactly as Authenticate does, and refuses
// (ErrInvalidToken) when the account is inactive or its row is gone. Issuing a
// credential is not weaker than using one, so a deactivated user must stop
// receiving fresh tokens, not merely stop being able to spend them. The same two
// carve-outs Authenticate makes apply verbatim: a nil store (no data plane bound
// — the trusted in-process minting context) and the dev-token subject, which has
// no backing row by design. Any other store failure fails closed; that adds no
// failure mode, because a database blip that refuses this renewal would refuse
// the following request anyway, and the client's refresh is best-effort. Roles on
// the re-minted token come from the reload, so a role revoked mid-session is
// reflected at once rather than carried forward from the old claims.
func (a *JWTAuthenticator) RenewUserToken(ctx context.Context, token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error) {
	var c jwtClaims
	parsed, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceUser), jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(a.clock))
	if err != nil || !parsed.Valid {
		return "", false, errors.Join(ErrInvalidToken, err)
	}
	// Preserve the session's first-login origin across renewals; fall back to iat
	// for a token minted before the origin claim existed.
	origin := time.Time{}
	if c.OriginIssuedAt != nil {
		origin = c.OriginIssuedAt.Time
	} else if c.IssuedAt != nil {
		origin = c.IssuedAt.Time
	}
	if maxLifetime > 0 && !origin.IsZero() && a.clock().Sub(origin) > maxLifetime {
		return "", false, nil // past the ceiling: let the credential lapse
	}
	user, err := a.reloadForRenewal(ctx, &c)
	if err != nil {
		return "", false, err
	}
	renewed, err = a.mintUserToken(user, ttl, origin)
	if err != nil {
		return "", false, err
	}
	return renewed, true, nil
}

// reloadForRenewal resolves the principal a renewal will re-mint, preferring the
// store over the incoming token's claims. It is the read side that makes renewal
// obey the same revocation rule as request authentication, and it mirrors
// Authenticate's branches one for one — nil store and the dev-token subject fall
// back to the signed claims, an inactive or otherwise-missing user is refused,
// and any other store error fails closed (propagated unchanged, so the handler
// can tell "rejected" apart from "could not be determined").
func (a *JWTAuthenticator) reloadForRenewal(ctx context.Context, c *jwtClaims) (*User, error) {
	claimed := &User{ID: c.Subject, TenantID: c.TenantID, Email: c.Email, Roles: c.Roles}
	if a.store == nil {
		return claimed, nil
	}
	user, active, err := a.store.FindUserByID(ctx, c.Subject)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			if c.Subject == DevTokenSubject {
				return claimed, nil
			}
			return nil, errors.Join(ErrInvalidToken, err)
		}
		// Same split Authenticate makes, for the same reason: a store we could not
		// reach did not tell us the token is invalid, only that we cannot say. The
		// renewal is still refused — the caller denies on any error — but the
		// handler can now answer 503 instead of sending the client to log in
		// against the database that is already down.
		return nil, err
	}
	if !active {
		return nil, ErrInvalidToken
	}
	return user, nil
}
