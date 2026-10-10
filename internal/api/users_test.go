package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// fakeUserStore records the arguments it received so the handler tests can assert
// that the plaintext password is delegated to the store (which hashes it) and
// never echoed back in the response.
type fakeUserStore struct {
	created               domain.User
	err                   error
	called                bool
	gotEmail, gotPassword string
	gotRoles              []string
	enforceUnique         bool
	seen                  map[string]bool
	users                 []domain.User
	listErr               error
	listCalled            bool
}

func (f *fakeUserStore) CreateUser(_ context.Context, _, email, password string, roles []string) (domain.User, error) {
	f.called = true
	f.gotEmail, f.gotPassword, f.gotRoles = email, password, roles
	if f.err != nil {
		return domain.User{}, f.err
	}
	if f.enforceUnique {
		if f.seen == nil {
			f.seen = map[string]bool{}
		}
		if f.seen[email] {
			return domain.User{}, domain.ErrConflict
		}
		f.seen[email] = true
	}
	return f.created, nil
}

func (f *fakeUserStore) ListUsers(_ context.Context, _ string, limit, offset int) ([]domain.User, int, error) {
	f.listCalled = true
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	total := len(f.users)
	lo := offset
	if lo > total {
		lo = total
	}
	hi := lo + limit
	if hi > total {
		hi = total
	}
	return f.users[lo:hi], total, nil
}

// fakeUserAudit records the audit entries the handler writes on success.
type fakeUserAudit struct {
	entries []userAuditEntry
	err     error
}

type userAuditEntry struct{ tenant, actorID, createdID, email, roles string }

func (f *fakeUserAudit) RecordUserCreatedAudit(_ context.Context, tenant, actorUserID, createdUserID, email, roles string) error {
	f.entries = append(f.entries, userAuditEntry{tenant, actorUserID, createdUserID, email, roles})
	return f.err
}

func userServer(store UserStore, user *auth.User) *gin.Engine {
	return userServerAudit(store, nil, user)
}

func userServerAudit(store UserStore, audit UserAuditWriter, user *auth.User) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: user},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Users:         store,
		UserAudit:     audit,
	})
}

func adminUser() *auth.User {
	return &auth.User{ID: "admin-1", TenantID: "default", Roles: []string{"admin"}}
}

func TestCreateUserReturnsCreatedUserWithoutSecret(t *testing.T) {
	store := &fakeUserStore{created: domain.User{
		ID: "u-1", Email: "alice@example.com", Roles: []string{"admin"}, IsActive: true,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}
	srv := userServer(store, adminUser())

	body := `{"email":"alice@example.com","password":"s3cret-pass","roles":["admin"]}`
	rec := authGet(srv, http.MethodPost, "/api/v2/users", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	// The store received the plaintext password (to hash), but the response must
	// never echo it or any hash.
	if store.gotPassword != "s3cret-pass" {
		t.Errorf("store should receive the plaintext password, got %q", store.gotPassword)
	}
	if len(store.gotRoles) != 1 || store.gotRoles[0] != "admin" {
		t.Errorf("store should receive the requested roles, got %v", store.gotRoles)
	}
	if strings.Contains(rec.Body.String(), "s3cret-pass") ||
		strings.Contains(rec.Body.String(), "password") ||
		strings.Contains(rec.Body.String(), "hash") {
		t.Errorf("response leaked a secret: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"roles":["admin"]`) {
		t.Errorf("response missing roles: %s", rec.Body.String())
	}
	var dto userDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.ID != "u-1" || dto.Email != "alice@example.com" ||
		len(dto.Roles) != 1 || dto.Roles[0] != "admin" || !dto.IsActive {
		t.Errorf("unexpected user dto: %+v", dto)
	}
	if dto.CreatedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("created_at = %q, want RFC3339", dto.CreatedAt)
	}
}

func TestCreateUserDuplicateEmailConflict(t *testing.T) {
	store := &fakeUserStore{err: domain.ErrConflict}
	rec := authGet(userServer(store, adminUser()), http.MethodPost, "/api/v2/users",
		`{"email":"dupe@example.com","password":"pw-12345678"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate email = %d, want 409", rec.Code)
	}
}

func TestCreateUserUnknownRoleIsBadRequest(t *testing.T) {
	store := &fakeUserStore{err: domain.ErrValidation}
	rec := authGet(userServer(store, adminUser()), http.MethodPost, "/api/v2/users",
		`{"email":"bob@example.com","password":"pw-12345678","roles":["wizard"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown role = %d, want 400", rec.Code)
	}
}

func TestCreateUserRequiresAdmin(t *testing.T) {
	// A non-admin principal (no write:user permission, no admin short-circuit) is
	// forbidden by the write:user gate on POST.
	viewer := &auth.User{ID: "v-1", TenantID: "default", Roles: []string{"viewer"}}
	store := &fakeUserStore{}
	rec := authGet(userServer(store, viewer), http.MethodPost, "/api/v2/users",
		`{"email":"x@example.com","password":"pw-12345678"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin create = %d, want 403", rec.Code)
	}
	if store.called {
		t.Error("store must not be reached when the caller lacks permission")
	}
}

func TestCreateUserValidatesRequiredFields(t *testing.T) {
	cases := map[string]string{
		"missing email":    `{"password":"pw-12345678"}`,
		"missing password": `{"email":"a@example.com"}`,
		"short password":   `{"email":"a@example.com","password":"short"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeUserStore{}
			rec := authGet(userServer(store, adminUser()), http.MethodPost, "/api/v2/users", body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400", name, rec.Code)
			}
			if store.called {
				t.Errorf("%s: store must not be reached on invalid input", name)
			}
		})
	}
}

func TestCreateUserWritesAuditOnSuccess(t *testing.T) {
	store := &fakeUserStore{created: domain.User{ID: "u-7", Email: "alice@example.com", Roles: []string{"admin"}, IsActive: true}}
	audit := &fakeUserAudit{}
	rec := authGet(userServerAudit(store, audit, adminUser()), http.MethodPost, "/api/v2/users",
		`{"email":"alice@example.com","password":"pw-12345678","roles":["admin"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(audit.entries) != 1 {
		t.Fatalf("audit entries = %d, want exactly 1", len(audit.entries))
	}
	e := audit.entries[0]
	if e.actorID != "admin-1" || e.createdID != "u-7" || e.email != "alice@example.com" || e.roles != "admin" || e.tenant != "default" {
		t.Errorf("unexpected audit entry: %+v", e)
	}
}

func TestCreateUserAuditFailureDoesNotFailRequest(t *testing.T) {
	store := &fakeUserStore{created: domain.User{ID: "u-8", Email: "z@example.com"}}
	audit := &fakeUserAudit{err: errors.New("audit sink down")}
	rec := authGet(userServerAudit(store, audit, adminUser()), http.MethodPost, "/api/v2/users",
		`{"email":"z@example.com","password":"pw-12345678"}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("a best-effort audit failure must not fail the request, got %d", rec.Code)
	}
}

func TestCreateUserNormalizesEmailForUniqueness(t *testing.T) {
	// Uniqueness is exact-text at the DB, so the handler must lowercase-normalize
	// before the store sees the email — otherwise "A@X.COM" and "a@x.com" would
	// become two distinct accounts.
	store := &fakeUserStore{enforceUnique: true, created: domain.User{ID: "u-1", Email: "a@x.com"}}
	srv := userServer(store, adminUser())

	if rec := authGet(srv, http.MethodPost, "/api/v2/users",
		`{"email":"A@X.COM","password":"pw-12345678"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first create = %d (%s)", rec.Code, rec.Body.String())
	}
	if store.gotEmail != "a@x.com" {
		t.Errorf("store saw %q, want normalized a@x.com", store.gotEmail)
	}
	if rec := authGet(srv, http.MethodPost, "/api/v2/users",
		`{"email":"a@x.com","password":"pw-12345678"}`); rec.Code != http.StatusConflict {
		t.Errorf("normalized duplicate = %d, want 409", rec.Code)
	}
}

func TestCreateUserRejectsMalformedEmail(t *testing.T) {
	store := &fakeUserStore{}
	rec := authGet(userServer(store, adminUser()), http.MethodPost, "/api/v2/users",
		`{"email":"not-an-email","password":"pw-12345678"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed email = %d, want 400", rec.Code)
	}
	if store.called {
		t.Error("store must not be reached on a malformed email")
	}
}

func TestCreateUserWithoutStoreNotRegistered(t *testing.T) {
	// With no user store wired, the route is absent (404), never a 500.
	rec := authGet(userServer(nil, adminUser()), http.MethodPost, "/api/v2/users",
		`{"email":"a@example.com","password":"pw-12345678"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("nil store = %d, want 404", rec.Code)
	}
}

func TestListUsersReturnsCollectionWithoutSecret(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{{
		ID: "u-1", Email: "alice@example.com", Roles: []string{"admin"}, IsActive: true,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}}
	rec := authGet(userServer(store, adminUser()), http.MethodGet, "/api/v2/users", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", rec.Code, rec.Body.String())
	}
	// The list must never expose a password or hash column.
	if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "hash") {
		t.Errorf("list leaked a secret: %s", rec.Body.String())
	}
	var col userCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.TotalEntries != 1 || len(col.Users) != 1 {
		t.Fatalf("unexpected collection: %+v", col)
	}
	u := col.Users[0]
	if u.ID != "u-1" || u.Email != "alice@example.com" || !u.IsActive ||
		len(u.Roles) != 1 || u.Roles[0] != "admin" || u.CreatedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("unexpected user item: %+v", u)
	}
}

func TestListUsersRequiresPermission(t *testing.T) {
	// A non-admin principal without read:user is forbidden and must not reach
	// the store.
	viewer := &auth.User{ID: "v-1", TenantID: "default", Roles: []string{"viewer"}}
	store := &fakeUserStore{}
	rec := authGet(userServer(store, viewer), http.MethodGet, "/api/v2/users", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin list = %d, want 403", rec.Code)
	}
	if store.listCalled {
		t.Error("store must not be reached when the caller lacks permission")
	}
}

func TestListUsersEmptyStubWithoutStore(t *testing.T) {
	// With no user store wired, the list still renders an empty collection (200)
	// rather than 404, matching the other Admin resources.
	rec := authGet(userServer(nil, adminUser()), http.MethodGet, "/api/v2/users", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("nil store list = %d", rec.Code)
	}
	var col map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &col)
	if col["total_entries"].(float64) != 0 {
		t.Errorf("nil store should yield empty collection, got %v", col)
	}
}
