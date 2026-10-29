package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// FindUserByLogin loads a user and its bcrypt hash for authentication.
func (r *Repository) FindUserByLogin(ctx context.Context, tenant, username string) (*auth.User, string, error) {
	row, err := r.q.GetUserByEmail(ctx, queries.GetUserByEmailParams{Name: tenant, Email: username})
	if err != nil {
		// A genuine not-found is an auth failure (generic, no enumeration). Any other
		// error is a real backend failure (DB down/unreachable) and MUST propagate so
		// the caller returns a 5xx, not a misleading "invalid credentials" (#843).
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", auth.ErrInvalidCredentials
		}
		return nil, "", fmt.Errorf("looking up user for login: %w", err)
	}
	if !row.IsActive {
		return nil, "", auth.ErrInvalidCredentials
	}
	roles, err := r.q.GetUserRoles(ctx, row.ID)
	if err != nil {
		return nil, "", fmt.Errorf("loading roles: %w", err)
	}
	perms, err := r.q.GetUserPermissions(ctx, row.ID)
	if err != nil {
		return nil, "", fmt.Errorf("loading permissions: %w", err)
	}
	user := &auth.User{ID: uuidToString(row.ID), TenantID: tenant, Email: row.Email, Roles: roles}
	for _, p := range perms {
		user.Permissions = append(user.Permissions, auth.Permission{Action: p.Action, Resource: p.Resource})
	}
	return user, strOrEmpty(row.PasswordHash), nil
}

// FindUserByID reloads a user's current authorization state by id: its tenant,
// roles, and permissions, plus whether the account is active. It is the per-
// request source of truth the authenticator uses on token validation. A subject
// that is not a valid uuid, or that matches no row, yields auth.ErrUserNotFound
// (the trusted in-process minting path has no backing user); any other failure
// is returned as-is so the caller can fail closed.
func (r *Repository) FindUserByID(ctx context.Context, id string) (*auth.User, bool, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return nil, false, auth.ErrUserNotFound
	}
	// One round trip: this runs on every authenticated request, so the user,
	// its roles and its permissions come back from a single statement.
	row, err := r.q.GetUserPrincipalByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, auth.ErrUserNotFound
		}
		return nil, false, fmt.Errorf("loading user by id: %w", err)
	}
	var perms [][2]string
	if err := json.Unmarshal(row.Permissions, &perms); err != nil {
		return nil, false, fmt.Errorf("decoding permissions: %w", err)
	}
	user := &auth.User{ID: uuidToString(row.ID), TenantID: row.Tenant, Email: row.Email, Roles: row.Roles}
	for _, p := range perms {
		user.Permissions = append(user.Permissions, auth.Permission{Action: p[0], Resource: p[1]})
	}
	return user, row.IsActive, nil
}

// FindUserByOIDCSubject resolves an OIDC identity to a Dexaflow user by its
// immutable (provider, subject) pair, the trusted link key for a returning SSO
// login. Like FindUserByID it loads the current tenant, roles, and permissions
// plus the active flag, so the caller reconstructs the same principal the
// credential path would. A pair matching no row yields auth.ErrUserNotFound (the
// signal to consider just-in-time provisioning); any other failure is returned
// as-is so the caller can fail closed.
func (r *Repository) FindUserByOIDCSubject(ctx context.Context, provider, subject string) (*auth.User, bool, error) {
	row, err := r.q.GetUserByOIDCSubject(ctx, queries.GetUserByOIDCSubjectParams{
		OidcProvider: strPtr(provider), OidcSubject: strPtr(subject),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, auth.ErrUserNotFound
		}
		return nil, false, fmt.Errorf("loading user by oidc subject: %w", err)
	}
	roles, err := r.q.GetUserRoles(ctx, row.ID)
	if err != nil {
		return nil, false, fmt.Errorf("loading roles: %w", err)
	}
	perms, err := r.q.GetUserPermissions(ctx, row.ID)
	if err != nil {
		return nil, false, fmt.Errorf("loading permissions: %w", err)
	}
	user := &auth.User{ID: uuidToString(row.ID), TenantID: row.Tenant, Email: row.Email, Roles: roles}
	for _, p := range perms {
		user.Permissions = append(user.Permissions, auth.Permission{Action: p.Action, Resource: p.Resource})
	}
	return user, row.IsActive, nil
}

// CreateOIDCUser just-in-time provisions an OIDC-only account (NULL password),
// linked by (oidc_provider, oidc_subject), and grants it the given roles. It
// mirrors CreateUser's atomicity: every role is resolved BEFORE the insert so an
// unknown role fails cleanly as domain.ErrValidation without leaving an orphaned
// account, and the insert plus the grants run in one transaction so a failed
// grant rolls the account back. An empty role set grants none (default-deny).
// A concurrent double-provision surfaces as domain.ErrConflict via the unique
// (oidc_provider, oidc_subject) constraint. The returned user carries the
// granted role names so the caller can mint a token without a reload.
func (r *Repository) CreateOIDCUser(ctx context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	roleIDs := make([]pgtype.UUID, 0, len(roles))
	for _, role := range roles {
		roleID, rerr := r.q.GetRoleByName(ctx, queries.GetRoleByNameParams{TenantID: tid, Name: role})
		if rerr != nil {
			if errors.Is(rerr, pgx.ErrNoRows) {
				return nil, domain.Safef(domain.ErrValidation, "unknown role %q", role)
			}
			return nil, fmt.Errorf("looking up role: %w", rerr)
		}
		roleIDs = append(roleIDs, roleID)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning create-oidc-user tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := r.q.WithTx(tx)
	row, err := qtx.InsertOIDCUser(ctx, queries.InsertOIDCUserParams{
		TenantID: tid, Email: email, OidcProvider: strPtr(provider), OidcSubject: strPtr(subject),
	})
	if err != nil {
		return nil, mapConflict(err)
	}
	for _, roleID := range roleIDs {
		if aerr := qtx.AssignUserRole(ctx, queries.AssignUserRoleParams{UserID: row.ID, RoleID: roleID}); aerr != nil {
			return nil, fmt.Errorf("assigning role: %w", aerr)
		}
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return nil, fmt.Errorf("committing create-oidc-user tx: %w", cerr)
	}
	return &auth.User{ID: uuidToString(row.ID), TenantID: tenant, Email: row.Email, Roles: roles}, nil
}

// LocalPasswordUserExists reports whether an address has a usable LOCAL password
// login in the tenant: an active user row with a password hash.
//
// It backs the boot-time check on auth.oidc.break_glass_emails. An address on
// that allowlist with no password row is an escape hatch that does not open: the
// gate admits it and the credential lookup then fails like a wrong password.
//
// The hash matters, not just the row. A user created by OIDC just-in-time
// provisioning has a NULL password_hash (the users_has_auth constraint permits
// it because the OIDC subject is the other half), so the row can exist while no
// password can ever verify against it.
func (r *Repository) LocalPasswordUserExists(ctx context.Context, tenant, email string) (bool, error) {
	n, err := r.q.CountLocalPasswordUser(ctx, queries.CountLocalPasswordUserParams{Name: tenant, Lower: email})
	if err != nil {
		return false, fmt.Errorf("checking local password login: %w", err)
	}
	return n > 0, nil
}

// AdminUserState reports whether a user with exactly this email exists in the
// tenant, active or not and whatever its login, and whether it holds the admin
// role. It backs the bootstrap admin resolution (auth.ResolveAdminEmail): an
// install whose admin predates the rename to Dexaflow keeps that admin instead
// of gaining a second one.
func (r *Repository) AdminUserState(ctx context.Context, tenant, email string) (auth.AdminState, error) {
	row, err := r.q.GetUserByEmail(ctx, queries.GetUserByEmailParams{Name: tenant, Email: email})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.UserAbsent, nil
		}
		return auth.UserAbsent, fmt.Errorf("looking up user: %w", err)
	}
	roles, err := r.q.GetUserRoles(ctx, row.ID)
	if err != nil {
		return auth.UserAbsent, fmt.Errorf("loading user roles: %w", err)
	}
	for _, role := range roles {
		if role == "admin" {
			return auth.UserIsAdmin, nil
		}
	}
	return auth.UserWithoutAdminRole, nil
}

// RoleExists reports whether a role name exists for the tenant. The OIDC login
// path uses it to fail closed on a misconfigured default_role before minting a
// token for a returning user (the JIT path validates roles inside CreateOIDCUser).
func (r *Repository) RoleExists(ctx context.Context, tenant, role string) (bool, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return false, err
	}
	if _, err := r.q.GetRoleByName(ctx, queries.GetRoleByNameParams{TenantID: tid, Name: role}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("looking up role: %w", err)
	}
	return true, nil
}

// ReconcileUserRoles makes the user's granted roles exactly roleNames, atomically:
// it is how the identity provider stays authoritative over an OIDC user's roles.
// On each OIDC login the caller passes the group-mapped role set, and this sets
// the DB user_roles to precisely that set, so a demotion or deprovisioning at the
// IdP takes effect on the next login and the per-request authz reload sees it.
//
// Every name is resolved to a role id in the user's OWN tenant BEFORE any write,
// so a name that is not a role in that tenant fails closed as domain.ErrValidation
// with the prior grants untouched, the login path turns that into a rejected,
// audited login rather than silently wiping the user's roles. The delete and the
// inserts run in one transaction, making the operation idempotent (reconciling
// the same set yields the same rows) and an empty roleNames a full clear
// (default-deny).
func (r *Repository) ReconcileUserRoles(ctx context.Context, userID string, roleNames []string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning reconcile-user-roles tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := r.q.WithTx(tx)
	// Resolve every role id first so an unknown name aborts before any mutation.
	roleIDs := make([]pgtype.UUID, 0, len(roleNames))
	for _, name := range roleNames {
		roleID, rerr := qtx.GetRoleIDForUserTenant(ctx, queries.GetRoleIDForUserTenantParams{ID: uid, Name: name})
		if rerr != nil {
			if errors.Is(rerr, pgx.ErrNoRows) {
				return domain.Safef(domain.ErrValidation, "unknown role %q", name)
			}
			return fmt.Errorf("looking up role: %w", rerr)
		}
		roleIDs = append(roleIDs, roleID)
	}
	if derr := qtx.DeleteUserRoles(ctx, uid); derr != nil {
		return fmt.Errorf("clearing user roles: %w", derr)
	}
	for _, roleID := range roleIDs {
		if aerr := qtx.AssignUserRole(ctx, queries.AssignUserRoleParams{UserID: uid, RoleID: roleID}); aerr != nil {
			return fmt.Errorf("assigning role: %w", aerr)
		}
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return fmt.Errorf("committing reconcile-user-roles tx: %w", cerr)
	}
	return nil
}

// BootstrapAdmin creates a default admin user with the given password when the
// tenant has no users yet, assigning the seeded admin role. It returns whether
// a user was created (false when users already exist).
func (r *Repository) BootstrapAdmin(ctx context.Context, tenant, email, password string) (bool, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, err
	}
	return r.BootstrapAdminHash(ctx, tenant, email, hash)
}

// CreateUser provisions a new account in the tenant and grants it the given set
// of roles, returning the created user (never its password or hash). It reuses
// the same bcrypt hashing as the bootstrap admin path (auth.HashPassword) and the
// same email uniqueness guarantee, so a duplicate email surfaces as
// domain.ErrConflict (the API maps it to 409). Every role is resolved BEFORE the
// insert so an unknown role fails cleanly as domain.ErrValidation without leaving
// an orphaned account; an empty set grants none, the most restrictive default,
// leaving the user with no permissions until an admin grants a role.
//
// The insert and the role grants run in a single transaction, so a failure in
// any grant rolls the user insert back. Without that atomicity a failed grant
// would leave an account the (tenant_id, email) UNIQUE makes impossible to
// recreate, every retry would 409 forever with no recovery path.
//
// This backs `dexaflow auth create-user` (ADR 0008) and is purely additive: it
// does not touch the bootstrap/reconcile path.
func (r *Repository) CreateUser(ctx context.Context, tenant, email, password string, roles []string) (domain.User, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.User{}, err
	}
	roleIDs := make([]pgtype.UUID, 0, len(roles))
	for _, role := range roles {
		roleID, rerr := r.q.GetRoleByName(ctx, queries.GetRoleByNameParams{TenantID: tid, Name: role})
		if rerr != nil {
			if errors.Is(rerr, pgx.ErrNoRows) {
				return domain.User{}, domain.Safef(domain.ErrValidation, "unknown role %q", role)
			}
			return domain.User{}, fmt.Errorf("looking up role: %w", rerr)
		}
		roleIDs = append(roleIDs, roleID)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return domain.User{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("beginning create-user tx: %w", err)
	}
	// Rollback after a successful commit is a no-op; on any early return it undoes
	// the user insert so a failed role grant leaves nothing behind.
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := r.q.WithTx(tx)
	row, err := qtx.InsertUser(ctx, queries.InsertUserParams{TenantID: tid, Email: email, PasswordHash: strPtr(hash)})
	if err != nil {
		return domain.User{}, mapConflict(err)
	}
	for _, roleID := range roleIDs {
		if aerr := qtx.AssignUserRole(ctx, queries.AssignUserRoleParams{UserID: row.ID, RoleID: roleID}); aerr != nil {
			return domain.User{}, fmt.Errorf("assigning role: %w", aerr)
		}
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return domain.User{}, fmt.Errorf("committing create-user tx: %w", cerr)
	}
	return domain.User{
		ID:        uuidToString(row.ID),
		Email:     row.Email,
		Roles:     roles,
		IsActive:  row.IsActive,
		CreatedAt: timeVal(row.CreatedAt),
	}, nil
}

// ListUsers returns a page of the tenant's accounts, newest first, each with the
// full set of role names it holds. It never reads or returns password_hash, the
// list must not expose secrets. The second result is the unpaged total, so the
// caller can render total_entries independent of the page size.
func (r *Repository) ListUsers(ctx context.Context, tenant string, limit, offset int) ([]domain.User, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListUsers(ctx, queries.ListUsersParams{TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("listing users: %w", err)
	}
	total, err := r.q.CountUsers(ctx, tid)
	if err != nil {
		return nil, 0, fmt.Errorf("counting users: %w", err)
	}
	out := make([]domain.User, 0, len(rows))
	for _, u := range rows {
		out = append(out, domain.User{
			ID:        uuidToString(u.ID),
			Email:     u.Email,
			Roles:     u.Roles,
			IsActive:  u.IsActive,
			CreatedAt: timeVal(u.CreatedAt),
		})
	}
	return out, int(total), nil
}

// SetUserActive activates or deactivates one user of the tenant and records
// user.activate or user.deactivate in the audit log, in one transaction, so the
// change and its record land together (#960). actorUserID is the admin acting.
// An unknown user, a malformed id or a user of another tenant is
// domain.ErrNotFound. The per-request authz reload reads the flag, so a
// deactivated user's sessions and API tokens stop working at their next request.
func (r *Repository) SetUserActive(ctx context.Context, tenant, actorUserID, userID string, active bool) (domain.User, error) {
	id, err := parseUUID(userID)
	if err != nil {
		return domain.User{}, domain.Safef(domain.ErrNotFound, "user %q not found", userID)
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.User{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("beginning set-user-active tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := r.q.WithTx(tx)
	row, err := qtx.SetUserActive(ctx, queries.SetUserActiveParams{Name: tenant, ID: id, IsActive: active})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.Safef(domain.ErrNotFound, "user %q not found", userID)
		}
		return domain.User{}, fmt.Errorf("setting user active: %w", err)
	}
	action := "user.deactivate"
	if active {
		action = "user.activate"
	}
	var actor pgtype.UUID
	if u, perr := parseUUID(actorUserID); perr == nil {
		actor = u
	}
	meta, err := json.Marshal(map[string]any{"email": row.Email})
	if err != nil {
		return domain.User{}, fmt.Errorf("encoding audit metadata: %w", err)
	}
	if err := qtx.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, UserID: actor, Action: action,
		ResourceType: strPtr("user"), ResourceID: strPtr(userID), Metadata: meta,
	}); err != nil {
		return domain.User{}, fmt.Errorf("writing %s audit: %w", action, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("committing set-user-active tx: %w", err)
	}
	return domain.User{
		ID:        uuidToString(row.ID),
		Email:     row.Email,
		Roles:     row.Roles,
		IsActive:  row.IsActive,
		CreatedAt: timeVal(row.CreatedAt),
	}, nil
}

// SetUserPassword sets a user's bcrypt hash by email, returning whether a user
// was updated (false when no such user exists). Used by `dexaflow lite
// reset-password`.
func (r *Repository) SetUserPassword(ctx context.Context, tenant, email, hash string) (bool, error) {
	n, err := r.q.UpdateUserPassword(ctx, queries.UpdateUserPasswordParams{
		Name: tenant, Email: email, PasswordHash: strPtr(hash),
	})
	if err != nil {
		return false, fmt.Errorf("updating password: %w", err)
	}
	return n > 0, nil
}

// BootstrapAdminHash provisions the Lite admin from a precomputed bcrypt hash
// (so the plaintext never reaches the control plane). It RECONCILES: if the admin
// already exists, its password is reset to this hash. The Lite config
// (admin_password_hash) is the source of truth, so the password the setup printed
// always logs in, even against a pre-existing or stale database, without anyone
// having to wipe Docker volumes. The only sanctioned way to change the password,
// `reset-password`, also writes the config, so the two never drift. Every path
// also grants the admin role of the admin's own tenant, idempotently: a new admin
// is created and granted in one transaction, and an existing admin without the
// role (left by a crash between the two steps in an older release, #513) gets it
// back. Returns true only when the admin was newly created (false when an
// existing one was reconciled). See cmd/dexaflow-server bootstrapAdmin.
func (r *Repository) BootstrapAdminHash(ctx context.Context, tenant, email, hash string) (bool, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return false, err
	}
	roleID, err := r.q.GetRoleByName(ctx, queries.GetRoleByNameParams{TenantID: tid, Name: "admin"})
	if err != nil {
		return false, fmt.Errorf("loading admin role: %w", err)
	}
	created, err := r.bootstrapAdminTx(ctx, tid, roleID, tenant, email, hash)
	if isUniqueViolation(err) {
		// Concurrent bootstrap race: between the reconcile check and the insert,
		// another process created the admin. This is expected under ADR 0049 (the
		// api and scheduler roles boot at once) and with multiple active-active api
		// replicas, since every replica runs bootstrap on startup. It is not a
		// failure: the insert waited for the other transaction to commit, so a
		// second pass finds the admin, reconciles its hash, ensures its role and
		// reports "not newly created".
		if _, rerr := r.bootstrapAdminTx(ctx, tid, roleID, tenant, email, hash); rerr != nil {
			return false, fmt.Errorf("reconciling admin after concurrent create: %w", rerr)
		}
		return false, nil
	}
	return created, err
}

// bootstrapAdminTx runs one bootstrap pass in a single transaction. It
// reconciles the admin's hash when the admin exists and creates the admin
// otherwise, then grants roleID (the tenant's admin role) either way; the grant
// is ON CONFLICT DO NOTHING, so a repeat is a no-op. A crash anywhere in the pass
// rolls all of it back, so no admin is left without its role. A unique violation
// from the insert is returned unwrapped for the caller's concurrent-create path.
func (r *Repository) bootstrapAdminTx(ctx context.Context, tid, roleID pgtype.UUID, tenant, email, hash string) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning bootstrap-admin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := r.q.WithTx(tx)
	updated, err := qtx.UpdateUserPassword(ctx, queries.UpdateUserPasswordParams{
		Name: tenant, Email: email, PasswordHash: strPtr(hash),
	})
	if err != nil {
		return false, fmt.Errorf("reconciling admin password: %w", err)
	}
	created := updated == 0
	var uid pgtype.UUID
	if created {
		uid, err = qtx.CreateUser(ctx, queries.CreateUserParams{TenantID: tid, Email: email, PasswordHash: strPtr(hash)})
		if isUniqueViolation(err) {
			return false, err
		}
		if err != nil {
			return false, fmt.Errorf("creating admin user: %w", err)
		}
	} else {
		row, gerr := qtx.GetUserByEmail(ctx, queries.GetUserByEmailParams{Name: tenant, Email: email})
		if gerr != nil {
			return false, fmt.Errorf("loading admin user: %w", gerr)
		}
		uid = row.ID
	}
	if err := qtx.AssignUserRole(ctx, queries.AssignUserRoleParams{UserID: uid, RoleID: roleID}); err != nil {
		return false, fmt.Errorf("assigning admin role: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing bootstrap-admin tx: %w", err)
	}
	return created, nil
}

// compile-time assurance that Repository satisfies the auth user store.
var _ auth.UserStore = (*Repository)(nil)

// EnsureIssuerUser makes sure a passwordless user linked to (provider,
// subject) exists in tenant with exactly roles (#1283). A new user is created
// with them; an existing one keeps its id and gets its roles reconciled. A
// subject already linked in another tenant is domain.ErrConflict, an unknown
// tenant domain.ErrNotFound and an unknown role domain.ErrValidation.
func (r *Repository) EnsureIssuerUser(ctx context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, bool, error) {
	existing, _, err := r.FindUserByOIDCSubject(ctx, provider, subject)
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		if _, terr := r.tenantID(ctx, tenant); terr != nil {
			return nil, false, terr
		}
		u, cerr := r.CreateOIDCUser(ctx, tenant, email, provider, subject, roles)
		return u, cerr == nil, cerr
	case err != nil:
		return nil, false, err
	case existing.TenantID != tenant:
		return nil, false, domain.Safef(domain.ErrConflict, "subject is already linked in another tenant")
	}
	if err := r.ReconcileUserRoles(ctx, existing.ID, roles); err != nil {
		return nil, false, err
	}
	existing.Roles = roles
	return existing, false, nil
}

// RolePermissions returns the grants of the role called role in tenant, for
// a CI OIDC session that acts with that role alone (ADR 0092). A tenant
// without such a role, or no such tenant, is auth.ErrRoleNotFound.
func (r *Repository) RolePermissions(ctx context.Context, tenant, role string) ([]auth.Permission, error) {
	tid, err := r.tenantID(ctx, tenant)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, auth.ErrRoleNotFound
	}
	if err != nil {
		return nil, err
	}
	roleID, err := r.q.GetRoleByName(ctx, queries.GetRoleByNameParams{TenantID: tid, Name: role})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, auth.ErrRoleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("looking up role: %w", err)
	}
	rows, err := r.q.GetRolePermissionsByID(ctx, roleID)
	if err != nil {
		return nil, fmt.Errorf("loading role permissions: %w", err)
	}
	perms := make([]auth.Permission, 0, len(rows))
	for _, p := range rows {
		perms = append(perms, auth.Permission{Action: p.Action, Resource: p.Resource})
	}
	return perms, nil
}
