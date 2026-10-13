package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// API token lifetimes (ADR 0074). A token lives 90 days unless the caller
// asks otherwise, at least an hour and at most a year.
const (
	defaultAPITokenTTL = 90 * 24 * time.Hour
	minAPITokenTTL     = time.Hour
	maxAPITokenTTL     = 365 * 24 * time.Hour
)

// maxAPITokenBody bounds the create request body.
const maxAPITokenBody = 4 << 10

// apiTokenName is what a token may be called: 1 to 64 ASCII letters, digits,
// dots, dashes and underscores, starting with a letter or digit, so a name is
// safe in a log line, a terminal and a CI variable.
var apiTokenName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// TokenStore keeps the API tokens of a tenant (ADR 0074). The store sees
// only the hash of a token. A user outside the tenant, or one holding too many
// live tokens, is domain.ErrValidation; a live token of the same owner and
// name is domain.ErrConflict; a token not found under the owner limit is
// domain.ErrNotFound.
type TokenStore interface {
	CreateAPIToken(ctx context.Context, tenant, userID, name, hash string, scopes []string, expiresAt time.Time) (auth.APIToken, error)
	ListAPITokens(ctx context.Context, tenant, ownerID string, limit, offset int) ([]auth.APIToken, int, error)
	RevokeAPIToken(ctx context.Context, tenant, id, ownerID string) (auth.APIToken, error)
}

// createAPITokenBody is the POST /api/v2/apiTokens payload.
type createAPITokenBody struct {
	Name             string `json:"name"`
	Scope            string `json:"scope"`
	ExpiresInSeconds *int64 `json:"expires_in_seconds"`
}

// apiTokenDTO describes a token. Token is set only in the create response,
// the one time the secret is shown.
type apiTokenDTO struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	OwnerEmail string   `json:"owner_email,omitempty"`
	CreatedAt  string   `json:"created_at"`
	ExpiresAt  string   `json:"expires_at"`
	LastUsedAt *string  `json:"last_used_at"`
	Token      string   `json:"token,omitempty"`
}

type apiTokenCollectionDTO struct {
	APITokens    []apiTokenDTO `json:"api_tokens"`
	TotalEntries int           `json:"total_entries"`
}

func toAPITokenDTO(t auth.APIToken) apiTokenDTO {
	out := apiTokenDTO{
		ID: t.ID, Name: t.Name, Scopes: t.Scopes, OwnerEmail: t.UserEmail,
		CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt: t.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if out.Scopes == nil {
		out.Scopes = []string{}
	}
	if t.LastUsedAt != nil {
		s := t.LastUsedAt.UTC().Format(time.RFC3339)
		out.LastUsedAt = &s
	}
	return out
}

// registerAPITokens mounts the API token routes (ADR 0074). They check no
// role permission, since every signed-in user manages its own tokens, and no
// scope grants them, so jwtAuth refuses them to every scoped token: a token
// can never mint, list or revoke tokens.
func registerAPITokens(r gin.IRouter, store TokenStore, audit AuthAuditWriter) {
	if store == nil {
		return
	}
	r.POST("/api/v2/apiTokens", createAPITokenHandler(store, audit))
	r.GET("/api/v2/apiTokens", listAPITokensHandler(store))
	r.DELETE("/api/v2/apiTokens/:token_id", revokeAPITokenHandler(store, audit))
}

// managesAllTokens reports whether user sees and revokes every token of its
// tenant rather than only its own: whoever manages the tenant's accounts.
func managesAllTokens(user *auth.User) bool {
	return user.HasPermission("write", "user")
}

// tokenOwnerFilter is the owner a request is limited to: the caller, or no
// one for a caller who manages the tenant's accounts.
func tokenOwnerFilter(user *auth.User) string {
	if managesAllTokens(user) {
		return ""
	}
	return user.ID
}

func createAPITokenHandler(store TokenStore, audit AuthAuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := UserFromContext(c)
		if !ok || user.Scoped {
			AbortProblem(c, http.StatusForbidden, "forbidden", scopedRouteRefusal)
			return
		}
		req, scopes, ttl, err := parseCreateAPIToken(c)
		if err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if !mayHoldScope(user, req.Scope) {
			AbortProblem(c, http.StatusForbidden, "forbidden", "your roles do not allow what a "+req.Scope+" token does")
			return
		}
		plain, hash, err := auth.NewAPIToken()
		if err != nil {
			AbortProblemCause(c, http.StatusInternalServerError, "internal error", "the token could not be generated", err)
			return
		}
		tok, err := store.CreateAPIToken(c.Request.Context(), user.TenantID, user.ID, req.Name, hash, scopes, time.Now().Add(ttl))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		recordTokenEvent(c, audit, user, "api_token.create", tok, map[string]string{
			"token_owner_id": tok.UserID,
			"scopes":         strings.Join(tok.Scopes, " "), "expires_at": tok.ExpiresAt.UTC().Format(time.RFC3339),
		})
		out := toAPITokenDTO(tok)
		out.Token = plain
		c.JSON(http.StatusCreated, out)
	}
}

// parseCreateAPIToken reads and validates the create body: a known field set,
// a well-formed name, a scope a token may carry, and a lifetime within bounds.
func parseCreateAPIToken(c *gin.Context) (createAPITokenBody, []string, time.Duration, error) {
	var req createAPITokenBody
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxAPITokenBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, nil, 0, errors.New("the body must be a JSON object with name, scope and optionally expires_in_seconds")
	}
	if dec.More() {
		return req, nil, 0, errors.New("the body must hold one JSON object")
	}
	if !apiTokenName.MatchString(req.Name) {
		return req, nil, 0, errors.New("name must be 1 to 64 letters, digits, dots, dashes or underscores, starting with a letter or digit")
	}
	scopes, ok := auth.APITokenScopes(req.Scope)
	if !ok {
		return req, nil, 0, errors.New(`scope must be "deploy" or "read"`)
	}
	ttl := defaultAPITokenTTL
	if req.ExpiresInSeconds != nil {
		secs := *req.ExpiresInSeconds
		if secs < int64(minAPITokenTTL/time.Second) || secs > int64(maxAPITokenTTL/time.Second) {
			return req, nil, 0, errors.New("expires_in_seconds must be between 3600 (one hour) and 31536000 (365 days)")
		}
		ttl = time.Duration(secs) * time.Second
	}
	return req, scopes, ttl, nil
}

// mayHoldScope reports whether user's roles allow what a token of scope
// does, so a token never names a scope its owner could not use.
func mayHoldScope(user *auth.User, scope string) bool {
	switch scope {
	case "deploy":
		return user.HasPermission("deploy", "dag") || user.HasPermission("write", "dag")
	case "read":
		return user.HasPermission("read", "dag")
	default:
		return false
	}
}

func listAPITokensHandler(store TokenStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := UserFromContext(c)
		if !ok || user.Scoped {
			AbortProblem(c, http.StatusForbidden, "forbidden", scopedRouteRefusal)
			return
		}
		limit, offset := pagination(c)
		toks, total, err := store.ListAPITokens(c.Request.Context(), user.TenantID, tokenOwnerFilter(user), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := apiTokenCollectionDTO{APITokens: make([]apiTokenDTO, 0, len(toks)), TotalEntries: total}
		for _, t := range toks {
			out.APITokens = append(out.APITokens, toAPITokenDTO(t))
		}
		c.JSON(http.StatusOK, out)
	}
}

func revokeAPITokenHandler(store TokenStore, audit AuthAuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := UserFromContext(c)
		if !ok || user.Scoped {
			AbortProblem(c, http.StatusForbidden, "forbidden", scopedRouteRefusal)
			return
		}
		tok, err := store.RevokeAPIToken(c.Request.Context(), user.TenantID, c.Param("token_id"), tokenOwnerFilter(user))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		recordTokenEvent(c, audit, user, "api_token.revoke", tok, map[string]string{"token_owner_id": tok.UserID})
		c.Status(http.StatusNoContent)
	}
}

// recordTokenEvent audits a token change with the acting user, the token's id
// and name, and extra; never the token or its hash. It is best effort, like
// every auth event: an audit failure is logged and the change stands.
func recordTokenEvent(c *gin.Context, audit AuthAuditWriter, user *auth.User, action string, tok auth.APIToken, extra map[string]string) {
	if audit == nil {
		return
	}
	fields := map[string]string{"token_id": tok.ID, "token_name": tok.Name}
	for k, v := range extra {
		fields[k] = v
	}
	if err := audit.RecordAuthEvent(c.Request.Context(), user.TenantID, user.ID, action, user.Email, "success", fields); err != nil {
		slog.WarnContext(c.Request.Context(), "recording api token audit", "action", action, "token_id", tok.ID, "error", err)
	}
}
