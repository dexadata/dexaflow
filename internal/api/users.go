package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// minPasswordLength is the minimum length the create-user API accepts. It is a
// deliberately conservative floor, not a full password policy — the maintainer
// may tighten it later without breaking the endpoint's contract.
const minPasswordLength = 8

// emailPattern is a deliberately basic sanity check — one "@", a dotted domain,
// and no whitespace. It rejects obvious garbage; it is not a full RFC 5322
// validator (real deliverability is out of scope for account creation).
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// normalizeEmail lowercases and trims the address so uniqueness (which is
// exact-text at the database) treats "Alice@X.com" and "alice@x.com" as one
// account rather than two.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// UserStore creates control-plane accounts for the admin create-user API. The
// store hashes the plaintext password (reusing the bootstrap admin's bcrypt
// scheme) and returns the created user without any secret. A duplicate email
// must surface as domain.ErrConflict and an unknown role as domain.ErrValidation.
type UserStore interface {
	CreateUser(ctx context.Context, tenant, email, password string, roles []string) (domain.User, error)
	ListUsers(ctx context.Context, tenant string, limit, offset int) ([]domain.User, int, error)
}

// UserAuditWriter records account-creation events for the Audit Log. It is a
// separate, narrow interface (not the task-shaped AuditWriter) so account
// management writes a "user" resource entry with the acting admin as owner. The
// granted roles are passed as a single joined string so the record captures the
// full set.
type UserAuditWriter interface {
	RecordUserCreatedAudit(ctx context.Context, tenant, actorUserID, createdUserID, email, roles string) error
}

// createUserBody is the POST /api/v2/users payload. Password is write-only.
type createUserBody struct {
	Email    string   `json:"email"`
	Password string   `json:"password"`
	Roles    []string `json:"roles"`
}

// userDTO is the create-user response. It never includes the password or hash.
type userDTO struct {
	ID        string   `json:"id"`
	Email     string   `json:"email"`
	Roles     []string `json:"roles"`
	IsActive  bool     `json:"is_active"`
	CreatedAt string   `json:"created_at"`
}

func toUserDTO(u domain.User) userDTO {
	roles := u.Roles
	if roles == nil {
		roles = []string{}
	}
	return userDTO{
		ID:        u.ID,
		Email:     u.Email,
		Roles:     roles,
		IsActive:  u.IsActive,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// userListItemDTO is one row of the user list. Unlike the Airflow FAB users API
// (which is username-keyed with first_name/last_name columns Dexaflow does not
// have), Dexaflow accounts are email-keyed and carry a set of RBAC roles, so the
// list is expressed in that native shape rather than the Airflow one. The
// password and its hash are write-only and never appear here.
type userListItemDTO struct {
	ID        string   `json:"id"`
	Email     string   `json:"email"`
	Roles     []string `json:"roles"`
	IsActive  bool     `json:"is_active"`
	CreatedAt string   `json:"created_at"`
}

// userCollectionDTO is the paged list response: the page of users plus the
// unpaged total, matching the {items, total_entries} shape of the other
// /api/v2 collections.
type userCollectionDTO struct {
	Users        []userListItemDTO `json:"users"`
	TotalEntries int               `json:"total_entries"`
}

func toUserListItemDTO(u domain.User) userListItemDTO {
	roles := u.Roles
	if roles == nil {
		roles = []string{}
	}
	return userListItemDTO{
		ID:        u.ID,
		Email:     u.Email,
		Roles:     roles,
		IsActive:  u.IsActive,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func listUsersHandler(store UserStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		users, total, err := store.ListUsers(c.Request.Context(), tenantOf(c), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := userCollectionDTO{Users: make([]userListItemDTO, 0, len(users)), TotalEntries: total}
		for _, u := range users {
			out.Users = append(out.Users, toUserListItemDTO(u))
		}
		c.JSON(http.StatusOK, out)
	}
}

func createUserHandler(store UserStore, audit UserAuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body createUserBody
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		email := normalizeEmail(body.Email)
		if email == "" || body.Password == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "email and password are required")
			return
		}
		if !emailPattern.MatchString(email) {
			AbortProblem(c, http.StatusBadRequest, "bad request", "email is not a valid address")
			return
		}
		if len(body.Password) < minPasswordLength {
			AbortProblem(c, http.StatusBadRequest, "bad request",
				fmt.Sprintf("password must be at least %d characters", minPasswordLength))
			return
		}
		user, err := store.CreateUser(c.Request.Context(), tenantOf(c), email, body.Password, body.Roles)
		if err != nil {
			handleUserWriteError(c, err)
			return
		}
		recordUserCreatedAudit(c, audit, user)
		c.JSON(http.StatusCreated, toUserDTO(user))
	}
}

// recordUserCreatedAudit writes a best-effort audit entry for a successful
// account creation; an audit failure must not fail the create the admin
// requested, so it is logged rather than surfaced.
func recordUserCreatedAudit(c *gin.Context, audit UserAuditWriter, u domain.User) {
	if audit == nil {
		return
	}
	actorID := ""
	if actor, ok := UserFromContext(c); ok {
		actorID = actor.ID
	}
	if err := audit.RecordUserCreatedAudit(c.Request.Context(), tenantOf(c), actorID, u.ID, u.Email, strings.Join(u.Roles, ",")); err != nil {
		slog.Warn("recording user-created audit", "user", u.ID, "error", err)
	}
}

// handleUserWriteError maps a rejected create to the right status: a bad role (or
// other input the caller can fix) is a 400, and everything else flows through
// handleRepoError (duplicate email -> 409, cancellation -> 499, else 500).
func handleUserWriteError(c *gin.Context, err error) {
	if errors.Is(err, domain.ErrValidation) {
		// Same redaction as the funnel: only a phrase the storage layer composed
		// with domain.Safef reaches the caller (#961).
		AbortProblemCause(c, http.StatusBadRequest, "bad request", safeDetail(err, detailInvalidInput), err)
		return
	}
	handleRepoError(c, err)
}

// registerUsers mounts the admin user-management surface. The list is gated with
// read:user and the create with write:user (both admin-only in practice — the
// admin role short-circuits every permission check). With no store wired the
// list still renders an empty collection (matching the other Admin resources, so
// the page never 404s), while the create route is left unregistered.
func registerUsers(r gin.IRouter, store UserStore, audit UserAuditWriter) {
	if store == nil {
		r.GET("/api/v2/users", apiEmptyCollection("users"))
		return
	}
	r.GET("/api/v2/users", RequirePermission("read", "user"), listUsersHandler(store))
	r.POST("/api/v2/users", RequirePermission("write", "user"), createUserHandler(store, audit))
}
