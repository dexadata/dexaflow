package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

const defaultMaxActiveRuns = 16

// txBeginner opens a transaction. *pgxpool.Pool satisfies it; tests inject a
// fake so the transactional paths can be exercised without a database.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Repository implements the API resource and auth user-store interfaces over
// Postgres using the sqlc-generated query set.
type Repository struct {
	q           *queries.Queries
	pool        txBeginner
	cipher      secrets.Cipher
	extCoverage externalSecretCoverage
	// tenants caches tenant name -> id (see tenantID). Bounded by the number
	// of tenants that exist, since only successful lookups are stored.
	tenants *sync.Map
}

// externalSecretCoverage reports whether a declared name is served by a
// configured external secret backend (operator config, ADR 0060). The D6
// registration check consults it to accept an externally-backed declared name
// without a provider call. It must be operator-sourced: if a DAG could supply
// its own coverage, it could mark any name covered and defeat D6 (ADR 0060 B1).
type externalSecretCoverage interface {
	CoversVariable(name string) bool
	CoversConnection(name string) bool
}

// NewRepository builds a Repository backed by the given Postgres connection.
func NewRepository(pg *Postgres) *Repository {
	return &Repository{q: pg.Queries, pool: pg.Pool, tenants: &sync.Map{}}
}

// SetCipher attaches the encryption cipher used for connection secrets (ADR
// 0019). Without it, connection writes fail rather than storing plaintext.
func (r *Repository) SetCipher(c secrets.Cipher) { r.cipher = c }

// SetExternalSecretCoverage attaches the operator-configured external-backend
// coverage used by the D6 registration check (ADR 0060 B1). Without it, the check
// is the pre-0060 vault-existence check (a declared name must exist in the
// vault). It must be wired from operator/Helm config, never from a DAG.
func (r *Repository) SetExternalSecretCoverage(c externalSecretCoverage) { r.extCoverage = c }

func toInt32(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n)
	}
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

// pgUniqueViolation is the Postgres SQLSTATE for a unique-constraint violation.
const pgUniqueViolation = "23505"

// mapConflict translates a Postgres unique-constraint violation into
// domain.ErrConflict (which the API maps to 409), leaving other errors as is — so
// a duplicate write (e.g. a second dag run for the same logical date) surfaces as
// a clean conflict rather than a 500. What is at stake is the STATUS, not the
// body: since #961 an untranslated error no longer discloses its SQLSTATE to
// the caller either way, but a 500 for an ordinary duplicate would still send
// the caller (and the on-call) hunting a server fault that never happened.
func mapConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return domain.ErrConflict
	}
	return err
}

// tenantID resolves a tenant name to its id. Nearly every repository method
// starts with it, so a resolved id is cached for the life of the process: a
// tenant is never renamed or deleted (no query does either), so name -> id
// cannot change. A miss is not cached, so a tenant created later is found.
func (r *Repository) tenantID(ctx context.Context, name string) (pgtype.UUID, error) {
	if r.tenants != nil {
		if id, ok := r.tenants.Load(name); ok {
			if uid, ok := id.(pgtype.UUID); ok {
				return uid, nil
			}
		}
	}
	t, err := r.q.GetTenantByName(ctx, name)
	if err != nil {
		return pgtype.UUID{}, mapNotFound(err)
	}
	if r.tenants != nil {
		r.tenants.Store(name, t.ID)
	}
	return t.ID, nil
}

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
// immutable (provider, subject) pair — the trusted link key for a returning SSO
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

// TenantExists reports whether a tenant with this name exists.
//
// It backs the boot-time check on auth.oidc.tenant_claims: a claim value mapped
// to a tenant that does not exist denies every login carrying it, and since the
// only tenant anything creates is "default" (migration 001), a typo there is
// unrecoverable without direct SQL. A lookup failure is returned rather than
// folded into false, so the caller can tell "absent" from "could not ask".
func (r *Repository) TenantExists(ctx context.Context, name string) (bool, error) {
	if _, err := r.tenantID(ctx, name); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
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
// with the prior grants untouched — the login path turns that into a rejected,
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

// ListDags returns a page of DAGs for the tenant and the total count.
func (r *Repository) ListDags(ctx context.Context, tenant string, limit, offset int) ([]domain.DAG, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListDagsWithVersion(ctx, queries.ListDagsWithVersionParams{TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("listing dags: %w", err)
	}
	total, err := r.q.CountDags(ctx, tid)
	if err != nil {
		return nil, 0, fmt.Errorf("counting dags: %w", err)
	}
	out := make([]domain.DAG, 0, len(rows))
	for _, d := range rows {
		out = append(out, mapDagWithVersion(queries.GetDagWithVersionRow(d)))
	}
	return out, int(total), nil
}

// GetDag returns a single DAG by its user-facing id.
func (r *Repository) GetDag(ctx context.Context, tenant, dagID string) (domain.DAG, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DAG{}, err
	}
	d, err := r.q.GetDagWithVersion(ctx, queries.GetDagWithVersionParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return domain.DAG{}, mapNotFound(err)
	}
	return mapDagWithVersion(d), nil
}

// SetPaused toggles the paused flag of a DAG.
func (r *Repository) SetPaused(ctx context.Context, tenant, dagID string, paused bool) (domain.DAG, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DAG{}, err
	}
	d, err := r.q.SetDagPaused(ctx, queries.SetDagPausedParams{TenantID: tid, DagID: dagID, IsPaused: paused})
	if err != nil {
		return domain.DAG{}, mapNotFound(err)
	}
	return mapDag(d), nil
}

func (r *Repository) resolveDag(ctx context.Context, tenant, dagID string) (queries.Dag, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return queries.Dag{}, err
	}
	d, err := r.q.GetDagByDagID(ctx, queries.GetDagByDagIDParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return queries.Dag{}, mapNotFound(err)
	}
	return d, nil
}

// ListDagRuns returns a page of runs for a DAG and the total count.
func (r *Repository) ListDagRuns(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListDagRunsByDagWithVersion(ctx, queries.ListDagRunsByDagWithVersionParams{DagID: dag.ID, Limit: toInt32(limit), Offset: toInt32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("listing dag runs: %w", err)
	}
	total, err := r.q.CountDagRunsByDag(ctx, dag.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("counting dag runs: %w", err)
	}
	out := make([]domain.DagRun, 0, len(rows))
	for _, run := range rows {
		out = append(out, mapDagRunWithVersion(queries.GetDagRunWithVersionRow(run), dagID))
	}
	return out, int(total), nil
}

// GetDagRun returns a single run by its run id.
func (r *Repository) GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return domain.DagRun{}, err
	}
	run, err := r.q.GetDagRunWithVersion(ctx, queries.GetDagRunWithVersionParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return domain.DagRun{}, mapNotFound(err)
	}
	return mapDagRunWithVersion(run, dagID), nil
}

// DeleteDagRun removes one run (and, by cascade, its task instances and XCom).
// It returns domain.ErrNotFound when no run with that id exists for the DAG, so
// the API can answer 404 rather than a silent 204 for a bad id.
func (r *Repository) DeleteDagRun(ctx context.Context, tenant, dagID, runID string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	n, err := r.q.DeleteDagRun(ctx, queries.DeleteDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return fmt.Errorf("deleting dag run: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// CreateDagRun inserts a new run for a DAG at its current version. The
// per-DAG max_active_runs cap (#200) is enforced here for any caller that
// goes through the repository — manual triggers via the API, scripted
// backfills, and any future programmatic trigger path — so the contract
// is honored in one place. A cap of zero is treated as "unlimited" to
// match the scheduler path (see `Scheduler.hasHeadroom`). The check
// races with concurrent inserts, but the small overshoot window is
// bounded by the number of concurrent writers and lets us avoid an
// advisory lock on the hot path.
func (r *Repository) CreateDagRun(ctx context.Context, tenant, dagID string, run domain.DagRun) (domain.DagRun, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return domain.DagRun{}, err
	}
	if maxActive := int(dag.MaxActiveRuns); maxActive > 0 {
		active, countErr := r.q.CountActiveDagRunsByDagID(ctx, dag.ID)
		if countErr != nil {
			return domain.DagRun{}, fmt.Errorf("counting active runs: %w", countErr)
		}
		if int(active) >= maxActive {
			return domain.DagRun{}, domain.Safef(domain.ErrConflict, "dag %q is at max_active_runs cap of %d", dagID, maxActive)
		}
	}
	// Persist an explicit empty object when the run carries no conf (or an
	// explicit JSON null), so the column is never NULL and downstream params
	// readers see a valid object.
	conf := []byte(run.Conf)
	if len(conf) == 0 || string(conf) == "null" {
		conf = []byte("{}")
	}
	created, err := r.q.CreateDagRun(ctx, queries.CreateDagRunParams{
		TenantID:     dag.TenantID,
		DagID:        dag.ID,
		DagVersionID: dag.CurrentVersionID,
		RunID:        run.RunID,
		LogicalDate:  pgtype.Timestamptz{Time: run.LogicalDate, Valid: true},
		State:        queries.DagRunState(run.State),
		Trigger:      queries.DagRunTrigger(run.RunType),
		Note:         strPtr(run.Note),
		Conf:         conf,
	})
	if err != nil {
		return domain.DagRun{}, fmt.Errorf("creating dag run: %w", mapConflict(err))
	}
	// The trigger's audit entry is written by the API handler, where the acting
	// user is known (so the Audit Log shows the owner).
	return mapDagRun(created, dagID), nil
}

// ListTaskInstanceAttempts returns every attempt for (run, task), oldest first —
// the current task_instances row UNIONed with all archived task_instance_history
// rows. The UI's /tries endpoint needs this to render one navigable tab per
// attempt; without history, a cleared task shows only the latest attempt and the
// user cannot inspect prior failures (Lima bug #241).
func (r *Repository) ListTaskInstanceAttempts(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.TaskInstance, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return nil, mapNotFound(err)
	}
	rows, err := r.q.ListTaskInstanceAttempts(ctx, queries.ListTaskInstanceAttemptsParams{
		DagRunID: run.ID, TaskID: taskID,
	})
	if err != nil {
		return nil, fmt.Errorf("listing task instance attempts: %w", err)
	}
	out := make([]domain.TaskInstance, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.TaskInstance{
			DagID:       dagID,
			RunID:       runID,
			TaskID:      taskID,
			MapIndex:    int(row.MapIndex),
			TryNumber:   int(row.TryNumber),
			MaxTries:    int(row.MaxTries),
			State:       domain.TaskState(row.State),
			Operator:    row.Operator,
			ScheduledAt: timePtr(row.ScheduledAt),
			QueuedAt:    timePtr(row.QueuedAt),
			StartedAt:   timePtr(row.StartedAt),
			EndedAt:     timePtr(row.EndedAt),
			Duration:    row.DurationSeconds,
			Hostname:    strOrEmpty(row.Hostname),
			Note:        strOrEmpty(row.Note),
			// The attempts view is where an operator opens a specific failed try, so
			// it must carry the same cause the run-level list does. The UNION already
			// selects error_message from both history and the live row.
			FailureReason: strOrEmpty(row.ErrorMessage),
		})
	}
	return out, nil
}

// ListTaskInstances returns the task instances of a run.
func (r *Repository) ListTaskInstances(ctx context.Context, tenant, dagID, runID string, _, _ int) ([]domain.TaskInstance, int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return nil, 0, err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return nil, 0, mapNotFound(err)
	}
	rows, err := r.q.ListTaskInstancesByRun(ctx, run.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("listing task instances: %w", err)
	}
	out := make([]domain.TaskInstance, 0, len(rows))
	for _, ti := range rows {
		out = append(out, mapTaskInstance(ti, dagID, runID))
	}
	return out, len(out), nil
}

// ClearTaskInstances resets tasks to none for re-run. When onlyFailed is true,
// only tasks currently in a failed-ish state (failed, upstream_failed,
// up_for_retry) are reset; with an empty taskIDs and onlyFailed, every failed
// task in the run is cleared. It returns the number of task instances actually
// reset.
//
// opts carries the two independent run-level decisions — whether to re-open the
// run, and which version the re-run executes (see domain.ClearOptions).
func (r *Repository) ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, onlyFailed bool, opts domain.ClearOptions) (int, error) {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return 0, err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return 0, mapNotFound(err)
	}
	cleared, err := r.resetTaskInstances(ctx, run.ID, taskIDs, onlyFailed)
	if err != nil {
		return cleared, err
	}
	if opts.ResetDagRun {
		if opts.RunOnLatestVersion {
			// Re-bind the run to the DAG's current version so a clear after a
			// code/yaml fix re-runs against the newest image + config (ADR 0020).
			// In dev the current version is the last hot-reload; in prod, the last
			// deploy. When the version is unchanged this is equivalent to a plain
			// state reset.
			if err := r.q.ResetDagRunToVersion(ctx, queries.ResetDagRunToVersionParams{
				ID:           run.ID,
				DagVersionID: dag.CurrentVersionID,
			}); err != nil {
				return cleared, fmt.Errorf("re-binding dag run to current version: %w", err)
			}
		} else if err := r.q.ReopenDagRunKeepingVersion(ctx, run.ID); err != nil {
			// The run is re-opened but keeps its pinned version, so the re-run
			// executes the image that produced the original attempt.
			return cleared, fmt.Errorf("re-opening dag run: %w", err)
		}
	}
	return cleared, nil
}

// resetTaskInstances applies the clear semantics: a specific task list, or (with
// an empty list and onlyFailed) every failed task in the run.
func (r *Repository) resetTaskInstances(ctx context.Context, runID pgtype.UUID, taskIDs []string, onlyFailed bool) (int, error) {
	if len(taskIDs) == 0 {
		if !onlyFailed {
			return 0, nil
		}
		n, err := r.q.ResetAllFailedTaskInstances(ctx, runID)
		if err != nil {
			return 0, fmt.Errorf("clearing failed tasks: %w", err)
		}
		return int(n), nil
	}
	cleared := 0
	for _, taskID := range taskIDs {
		if onlyFailed {
			n, err := r.q.ResetFailedTaskInstance(ctx, queries.ResetFailedTaskInstanceParams{DagRunID: runID, TaskID: taskID})
			if err != nil {
				return cleared, fmt.Errorf("clearing failed task %q: %w", taskID, err)
			}
			cleared += int(n)
			continue
		}
		if err := r.q.ResetTaskInstanceToNone(ctx, queries.ResetTaskInstanceToNoneParams{DagRunID: runID, TaskID: taskID}); err != nil {
			return cleared, fmt.Errorf("clearing task %q: %w", taskID, err)
		}
		cleared++
	}
	return cleared, nil
}

// SetDagRunState sets a DAG run's state directly, backing the UI's mark run
// success/failed actions. Terminal states stamp ended_at; re-opening to a
// non-terminal state clears it. started_at is preserved.
func (r *Repository) SetDagRunState(ctx context.Context, tenant, dagID, runID, state string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return mapNotFound(err)
	}
	ended := pgtype.Timestamptz{}
	if domain.DagRunState(state).IsTerminal() {
		ended = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	if _, err := r.q.UpdateDagRunState(ctx, queries.UpdateDagRunStateParams{
		ID: run.ID, State: queries.DagRunState(state), StartedAt: run.StartedAt, EndedAt: ended,
	}); err != nil {
		return fmt.Errorf("setting dag run state: %w", err)
	}
	return nil
}

// RecordTaskActionAudit logs a task-level action (clear, mark state) with the
// acting user and the run/task/try in metadata, so the Audit Log view shows the
// owner and the task columns. Scoped to the DAG (resource_id = dag_id) so it
// appears on the DAG's Audit Log tab.
func (r *Repository) RecordTaskActionAudit(ctx context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	var uid pgtype.UUID
	if u, perr := parseUUID(userID); perr == nil {
		uid = u
	}
	fields := map[string]any{"run_id": runID}
	if taskID != "" { // run-level events (e.g. trigger) carry no task columns
		fields["task_id"] = taskID
		fields["try_number"] = tryNumber
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, UserID: uid, Action: action,
		ResourceType: strPtr("dag"), ResourceID: strPtr(dagID), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing task action audit: %w", err)
	}
	return nil
}

// RecordUserCreatedAudit logs an account creation with the acting admin as the
// owner and the new account's email and granted roles in metadata, scoped to the
// "user" resource so account-management actions are visible in the Audit Log. The
// roles arrive as a single comma-joined string (empty when none were granted).
func (r *Repository) RecordUserCreatedAudit(ctx context.Context, tenant, actorUserID, createdUserID, email, roles string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	var uid pgtype.UUID
	if u, perr := parseUUID(actorUserID); perr == nil {
		uid = u
	}
	fields := map[string]any{"email": email}
	if roles != "" {
		fields["roles"] = roles
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, UserID: uid, Action: "user.create",
		ResourceType: strPtr("user"), ResourceID: strPtr(createdUserID), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing user-created audit: %w", err)
	}
	return nil
}

// RecordAuthEvent records an authentication event to the audit log (H5): OIDC
// login success/failure, tenant-pin rejection, JIT provisioning, break-glass
// login, and logout. It NEVER records tokens or the client secret — only the
// actor's email, the resolved tenant (best-effort), the outcome, and small
// non-secret detail fields (e.g. the rejection reason, the attempted tenant
// claim). It is best-effort: the caller logs and continues on error so a flaky
// audit sink never turns a security decision (a 403 rejection, a successful
// login) into a 5xx.
//
// The event is scoped to the resolved tenant when known; events that never
// resolved a tenant (a login failure, a tenant-pin rejection) fall back to the
// "default" tenant so the row still lands, with the attempted values in the
// metadata. resourceID carries the email so account-scoped auth activity is
// filterable alongside user.create.
func (r *Repository) RecordAuthEvent(ctx context.Context, tenant, actorUserID, action, email, outcome string, extra map[string]string) error {
	if tenant == "" {
		tenant = "default"
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		// The resolved tenant may not exist (an attacker-supplied claim); fall back
		// to default so the security event is never silently dropped.
		tid, err = r.tenantID(ctx, "default")
		if err != nil {
			return fmt.Errorf("resolving audit tenant: %w", err)
		}
	}
	var uid pgtype.UUID
	if u, perr := parseUUID(actorUserID); perr == nil {
		uid = u
	}
	fields := map[string]any{"outcome": outcome}
	if email != "" {
		fields["email"] = email
	}
	for k, v := range extra {
		fields[k] = v
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	var resourceID *string
	if email != "" {
		resourceID = strPtr(email)
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, UserID: uid, Action: action,
		ResourceType: strPtr("auth"), ResourceID: resourceID, Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing auth-event audit: %w", err)
	}
	return nil
}

// RecordSecretScopeWarning records that a task received the full tenant secret
// set while it declared only a narrower subset (ADR 0045, ADR 0055): under
// secret_scoping: enforce it would receive only its declared set. It is scoped to
// the DAG resource so the event surfaces on the DAG's Audit Log tab, with the
// kind ("variables" or "connections"), the run and task, and the declared/total
// counts in metadata. It records counts only — never secret names or values.
//
// tenantID is the tenant UUID the agent token carries (not the tenant name): the
// caller is the agent RPC path, which passes AgentIdentity.TenantID. Resolving it
// by name silently dropped every row (#722), so mirror the secret-delivery path
// and parse it as a UUID.
func (r *Repository) RecordSecretScopeWarning(ctx context.Context, tenantID, dagID, runID, taskID, kind string, declared, total int) error {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(map[string]any{
		"kind": kind, "run_id": runID, "task_id": taskID,
		"declared": declared, "total": total,
	})
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, Action: "secret.scope_warning",
		ResourceType: strPtr("dag"), ResourceID: strPtr(dagID), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing secret-scope warning audit: %w", err)
	}
	return nil
}

// RecordSecretLivenessDenial records that the secret-path liveness gate fired
// for a task instance whose attempt is no longer live (ADR 0055): a
// would-have-denied in observe mode, or a denial in enforce mode. It is scoped
// to the DAG resource so the event surfaces on the DAG's Audit Log tab, with the
// kind ("variables" or "connections"), the run, task, attempt, and the gate mode
// in metadata. It records identity + kind + mode only — never secret names or
// values.
//
// tenantID is the tenant UUID the agent token carries (not the tenant name): the
// caller is the agent RPC path, which passes AgentIdentity.TenantID. Resolving it
// by name silently dropped every row (#722) — including enforce-mode security
// denials — so mirror the secret-delivery path and parse it as a UUID.
func (r *Repository) RecordSecretLivenessDenial(ctx context.Context, tenantID, dagID, runID, taskID string, tryNumber int, kind, mode string) error {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(map[string]any{
		"kind": kind, "run_id": runID, "task_id": taskID,
		"try_number": tryNumber, "mode": mode,
	})
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	// Distinct actions per mode so an operator can tell an observe-phase
	// would-have-denied from an enforce-phase denial at a glance.
	action := "secret.liveness_would_deny"
	if mode == "enforce" {
		action = "secret.liveness_denied"
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, Action: action,
		ResourceType: strPtr("dag"), ResourceID: strPtr(dagID), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing secret-path liveness audit: %w", err)
	}
	return nil
}

// SetTaskInstanceState sets a task instance's state directly, backing the UI's
// "mark success"/"mark failed" actions. It does not run the task.
func (r *Repository) SetTaskInstanceState(ctx context.Context, tenant, dagID, runID, taskID, state string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{DagID: dag.ID, RunID: runID})
	if err != nil {
		return mapNotFound(err)
	}
	if err := r.q.UpdateTaskInstanceStateByRunTask(ctx, queries.UpdateTaskInstanceStateByRunTaskParams{
		State: queries.TaskState(state), DagRunID: run.ID, TaskID: taskID,
	}); err != nil {
		return fmt.Errorf("setting task %q state: %w", taskID, err)
	}
	return nil
}

// LatestRunsForDags returns up to perDag most-recent runs for each named DAG,
// keyed by dag_id, in a single windowed query (no per-DAG round trips).
func (r *Repository) LatestRunsForDags(ctx context.Context, tenant string, dagIDs []string, perDag int) (map[string][]domain.DagRun, error) {
	if len(dagIDs) == 0 {
		return map[string][]domain.DagRun{}, nil
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.LatestRunsForDags(ctx, queries.LatestRunsForDagsParams{
		TenantID: tid, Column2: dagIDs, Limit: toInt32(perDag),
	})
	if err != nil {
		return nil, fmt.Errorf("latest runs for dags: %w", err)
	}
	out := make(map[string][]domain.DagRun, len(dagIDs))
	for _, row := range rows {
		out[row.DagIDText] = append(out[row.DagIDText], domain.DagRun{
			DagID:       row.DagIDText,
			RunID:       row.RunID,
			LogicalDate: timeVal(row.LogicalDate),
			State:       domain.DagRunState(row.State),
			RunType:     string(row.Trigger),
			QueuedAt:    timeVal(row.QueuedAt),
			StartedAt:   timePtr(row.StartedAt),
			EndedAt:     timePtr(row.EndedAt),
		})
	}
	return out, nil
}

// TaskInstancesForRuns returns the task instances of the given runs of a DAG in
// one query, ordered by run_id, task_id, try_number, for the grid summaries.
func (r *Repository) TaskInstancesForRuns(ctx context.Context, tenant, dagID string, runIDs []string) ([]domain.TaskInstance, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.TaskInstancesForDagRuns(ctx, queries.TaskInstancesForDagRunsParams{
		TenantID: tid, DagID: dagID, Column3: runIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("task instances for runs: %w", err)
	}
	out := make([]domain.TaskInstance, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.TaskInstance{
			DagID:     dagID,
			RunID:     row.RunID,
			TaskID:    row.TaskID,
			TryNumber: int(row.TryNumber),
			State:     domain.TaskState(row.State),
			StartedAt: timePtr(row.StartedAt),
			EndedAt:   timePtr(row.EndedAt),
		})
	}
	return out, nil
}

// ListDagVersions returns the DAG's versions, newest first, with a 1-based
// version_number the UI uses to query version-scoped structure.
func (r *Repository) ListDagVersions(ctx context.Context, tenant, dagID string) ([]domain.DagVersion, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListDagVersions(ctx, queries.ListDagVersionsParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return nil, fmt.Errorf("listing dag versions: %w", err)
	}
	out := make([]domain.DagVersion, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.DagVersion{
			ID:            uuidToString(row.ID),
			VersionNumber: int(row.VersionNumber),
			CreatedAt:     timeVal(row.CreatedAt),
			Version:       row.Version,
		})
	}
	return out, nil
}

// GetCurrentSpec returns the parsed spec of the DAG's current version, or
// domain.ErrNotFound if the DAG or its current version does not exist.
func (r *Repository) GetCurrentSpec(ctx context.Context, tenant, dagID string) (domain.DAGSpec, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DAGSpec{}, err
	}
	raw, err := r.q.GetCurrentDagSpec(ctx, queries.GetCurrentDagSpecParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return domain.DAGSpec{}, mapNotFound(err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return domain.DAGSpec{}, fmt.Errorf("decoding current spec: %w", err)
	}
	return spec, nil
}

// RegisterDagVersion upserts the DAG and inserts a version keyed by specHash,
// setting it as current. It is idempotent: an existing hash yields created=false.
func (r *Repository) RegisterDagVersion(ctx context.Context, tenant string, spec domain.DAGSpec, specHash string) (bool, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return false, err
	}
	// A DAG may not declare a variable/connection that does not exist for the
	// tenant (ADR 0055 D6). Checked before any write so a bad declaration fails
	// fast with no partial state. An empty declaration is always valid, so no
	// pre-declaration DAG is ever rejected.
	if verr := r.validateDeclaredSecrets(ctx, tid, spec); verr != nil {
		return false, verr
	}
	maxRuns := spec.MaxActiveRuns
	if maxRuns == 0 {
		maxRuns = defaultMaxActiveRuns
	}
	dag, err := r.q.UpsertDag(ctx, queries.UpsertDagParams{
		TenantID:         tid,
		DagID:            spec.DagID,
		Description:      strPtr(spec.Description),
		Owner:            strPtr(spec.Owner),
		Tags:             spec.Tags,
		Schedule:         spec.Schedule,
		ScheduleTimezone: strPtr(spec.ScheduleTZ),
		StartDate:        parseTimestamptz(spec.StartDate),
		MaxActiveRuns:    toInt32(maxRuns),
		Catchup:          spec.Catchup,
	})
	if err != nil {
		return false, fmt.Errorf("upserting dag: %w", err)
	}
	if _, verr := r.q.GetDagVersionByHash(ctx, queries.GetDagVersionByHashParams{DagID: dag.ID, SpecHash: specHash}); verr == nil {
		return false, nil
	} else if !errors.Is(verr, pgx.ErrNoRows) {
		return false, fmt.Errorf("checking existing version: %w", verr)
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return false, fmt.Errorf("encoding spec: %w", err)
	}
	version, err := r.q.InsertDagVersion(ctx, queries.InsertDagVersionParams{
		DagID:          dag.ID,
		Version:        spec.DagVersion,
		ImageReference: spec.Image,
		Spec:           specJSON,
		SpecHash:       specHash,
		CreatedBy:      pgtype.UUID{},
	})
	if err != nil {
		// A collision on dag_versions_unique (dag_id, version) means the same
		// version string was pushed with different content — the hash dedup above
		// only short-circuits identical re-pushes. Versions are immutable, so this
		// is a genuine conflict, not an overwrite: map it to domain.ErrConflict
		// so the API answers 409 instead of 500 (#746 — where it also leaked the
		// raw 23505, which #961 closed for every code). Bump --dag-version to
		// push new content.
		return false, fmt.Errorf("inserting version: %w", mapConflict(err))
	}
	if err := r.q.SetCurrentDagVersion(ctx, queries.SetCurrentDagVersionParams{ID: dag.ID, CurrentVersionID: version.ID}); err != nil {
		return false, fmt.Errorf("setting current version: %w", err)
	}
	if err := r.q.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID:     tid,
		Action:       "dag.version.register",
		ResourceType: strPtr("dag"),
		ResourceID:   strPtr(spec.DagID),
	}); err != nil {
		return false, fmt.Errorf("writing audit log: %w", err)
	}
	return true, nil
}

// validateDeclaredSecrets rejects a spec that declares a variable or connection
// name that does not exist for the tenant (ADR 0055 D6). It gathers every
// declared name — DAG-level and per-task narrowing — and confirms each with one
// existence query per kind. A name renamed in the UI but not in the DAG surfaces
// here at registration rather than silently at run time; the error names both
// the DAG and the offending names so the author knows which side to fix.
//
// An empty declaration is always valid. Because declaration is new, no existing
// DAG declares anything, so this never rejects a pre-declaration DAG — the
// Lite/back-compat safety.
func (r *Repository) validateDeclaredSecrets(ctx context.Context, tid pgtype.UUID, spec domain.DAGSpec) error {
	// A declared name that is not in the vault but IS covered by a configured
	// external backend is accepted here without any provider call (ADR 0060 B1);
	// existence is proven pod-side at resolve time. coveredVar/coveredConn are nil
	// unless the operator wired an external backend, giving the pre-0060 check.
	var coveredVar, coveredConn func(string) bool
	if r.extCoverage != nil {
		coveredVar = r.extCoverage.CoversVariable
		coveredConn = r.extCoverage.CoversConnection
	}
	varNames := declaredSecretNames(spec.Variables, spec.Tasks, func(t domain.TaskSpec) []string { return t.Variables })
	if len(varNames) > 0 {
		existing, err := r.q.ExistingVariableKeys(ctx, queries.ExistingVariableKeysParams{TenantID: tid, Keys: varNames})
		if err != nil {
			return fmt.Errorf("checking declared variables: %w", err)
		}
		if unknown := unknownDeclaredNames(varNames, existing, coveredVar); len(unknown) > 0 {
			return domain.Safef(domain.ErrValidation,
				"dag %q declares unknown variable(s) %s; define them (dexaflow variables set) or remove them from the DAG's variables: declaration",
				spec.DagID, strings.Join(unknown, ", "))
		}
	}
	connNames := declaredSecretNames(spec.Connections, spec.Tasks, func(t domain.TaskSpec) []string { return t.Connections })
	if len(connNames) > 0 {
		existing, err := r.q.ExistingConnectionIDs(ctx, queries.ExistingConnectionIDsParams{TenantID: tid, ConnIds: connNames})
		if err != nil {
			return fmt.Errorf("checking declared connections: %w", err)
		}
		if unknown := unknownDeclaredNames(connNames, existing, coveredConn); len(unknown) > 0 {
			return domain.Safef(domain.ErrValidation,
				"dag %q declares unknown connection(s) %s; define them (dexaflow connections set) or remove them from the DAG's connections: declaration",
				spec.DagID, strings.Join(unknown, ", "))
		}
	}
	return nil
}

// declaredSecretNames collects the deduplicated set of declared names across the
// DAG-level declaration and every task's narrowing declaration, preserving first
// occurrence order so error messages are stable.
func declaredSecretNames(dagLevel []string, tasks []domain.TaskSpec, taskLevel func(domain.TaskSpec) []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(dagLevel))
	add := func(names []string) {
		for _, n := range names {
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}
	add(dagLevel)
	for _, t := range tasks {
		add(taskLevel(t))
	}
	return out
}

// missingNames returns the declared names absent from the existing set, in
// declaration order.
func missingNames(declared, existing []string) []string {
	have := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		have[e] = struct{}{}
	}
	var missing []string
	for _, d := range declared {
		if _, ok := have[d]; !ok {
			missing = append(missing, d)
		}
	}
	return missing
}

// unknownDeclaredNames is the ADR 0055 D6 existence check extended for ADR 0060
// B1: a declared name is unknown only if it is neither present in the vault
// (existing) nor covered by a configured external backend. A name covered by an
// operator-configured external backend is accepted at registration WITHOUT any
// provider call — confirming it in the provider would be exactly the
// author-named, core-identity network request ADR 0048 forecloses; existence is
// instead proven pod-side at resolve time, fail-closed on miss. covered may be
// nil (no external backend configured), giving the pre-0060 existence check.
func unknownDeclaredNames(declared, existing []string, covered func(string) bool) []string {
	missing := missingNames(declared, existing)
	if covered == nil {
		return missing
	}
	var unknown []string
	for _, name := range missing {
		if !covered(name) {
			unknown = append(unknown, name)
		}
	}
	return unknown
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
// an orphaned account; an empty set grants none — the most restrictive default,
// leaving the user with no permissions until an admin grants a role.
//
// The insert and the role grants run in a single transaction, so a failure in
// any grant rolls the user insert back. Without that atomicity a failed grant
// would leave an account the (tenant_id, email) UNIQUE makes impossible to
// recreate — every retry would 409 forever with no recovery path.
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
// full set of role names it holds. It never reads or returns password_hash — the
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
// always logs in — even against a pre-existing or stale database — without anyone
// having to wipe Docker volumes. The only sanctioned way to change the password,
// `reset-password`, also writes the config, so the two never drift. Returns true
// only when the admin was newly created (false when an existing one was
// reconciled). See cmd/dexaflow-server bootstrapAdmin.
func (r *Repository) BootstrapAdminHash(ctx context.Context, tenant, email, hash string) (bool, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return false, err
	}
	// Reconcile an existing admin to the configured hash.
	updated, err := r.q.UpdateUserPassword(ctx, queries.UpdateUserPasswordParams{
		Name: tenant, Email: email, PasswordHash: strPtr(hash),
	})
	if err != nil {
		return false, fmt.Errorf("reconciling admin password: %w", err)
	}
	if updated > 0 {
		return false, nil
	}
	// No such admin yet — create it and grant the admin role.
	uid, err := r.q.CreateUser(ctx, queries.CreateUserParams{TenantID: tid, Email: email, PasswordHash: strPtr(hash)})
	if err != nil {
		// Concurrent bootstrap race: between the reconcile check above and this
		// insert, another process created the admin. This is expected under ADR
		// 0049 (the api and scheduler roles boot at once) and with multiple
		// active-active api replicas — every replica runs bootstrap on startup.
		// It is not a failure: reconcile to the configured hash and report "not
		// newly created", so no process crashes on the unique-constraint 23505.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			if _, uerr := r.q.UpdateUserPassword(ctx, queries.UpdateUserPasswordParams{
				Name: tenant, Email: email, PasswordHash: strPtr(hash),
			}); uerr != nil {
				return false, fmt.Errorf("reconciling admin after concurrent create: %w", uerr)
			}
			return false, nil
		}
		return false, fmt.Errorf("creating admin user: %w", err)
	}
	roleID, err := r.q.GetRoleByName(ctx, queries.GetRoleByNameParams{TenantID: tid, Name: "admin"})
	if err != nil {
		return false, fmt.Errorf("loading admin role: %w", err)
	}
	if err := r.q.AssignUserRole(ctx, queries.AssignUserRoleParams{UserID: uid, RoleID: roleID}); err != nil {
		return false, fmt.Errorf("assigning admin role: %w", err)
	}
	return true, nil
}

// compile-time assurance that Repository satisfies the auth user store.
var _ auth.UserStore = (*Repository)(nil)

// ListAuditLogs returns a page of audit-log entries for the tenant, newest
// first, optionally filtered to a single DAG (dagID == "" means no filter).
func (r *Repository) ListAuditLogs(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.AuditLogEntry, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	var dagFilter *string
	if dagID != "" {
		dagFilter = &dagID
	}
	rows, err := r.q.ListAuditLogs(ctx, queries.ListAuditLogsParams{
		TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset), DagID: dagFilter,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing audit logs: %w", err)
	}
	total, err := r.q.CountAuditLogs(ctx, queries.CountAuditLogsParams{TenantID: tid, DagID: dagFilter})
	if err != nil {
		return nil, 0, fmt.Errorf("counting audit logs: %w", err)
	}
	out := make([]domain.AuditLogEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.AuditLogEntry{
			ID:           row.ID,
			When:         timeVal(row.OccurredAt),
			Action:       row.Action,
			ResourceType: strOrEmpty(row.ResourceType),
			ResourceID:   strOrEmpty(row.ResourceID),
			Owner:        row.Owner,
			Extra:        string(row.Metadata),
		})
	}
	return out, int(total), nil
}

// DeleteDag removes a DAG and (via ON DELETE CASCADE) its versions, runs, task
// instances, and XCom index rows. It returns ErrNotFound when no DAG matched.
func (r *Repository) DeleteDag(ctx context.Context, tenant, dagID string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	rows, err := r.q.DeleteDag(ctx, queries.DeleteDagParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return fmt.Errorf("deleting dag: %w", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListDagsFiltered returns a page of active DAGs for the tenant, optionally
// filtered by paused state and/or latest-run state, with the matching total.
// An empty runState or nil paused disables that filter.
func (r *Repository) ListDagsFiltered(ctx context.Context, tenant, runState string, paused *bool, limit, offset int) ([]domain.DAG, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	var rs *queries.DagRunState
	if runState != "" {
		s := queries.DagRunState(runState)
		rs = &s
	}
	rows, err := r.q.ListDagsFiltered(ctx, queries.ListDagsFilteredParams{
		TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset), Paused: paused, RunState: rs,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing filtered dags: %w", err)
	}
	total, err := r.q.CountDagsFiltered(ctx, queries.CountDagsFilteredParams{TenantID: tid, Paused: paused, RunState: rs})
	if err != nil {
		return nil, 0, fmt.Errorf("counting filtered dags: %w", err)
	}
	out := make([]domain.DAG, 0, len(rows))
	for _, d := range rows {
		out = append(out, mapDag(d))
	}
	return out, int(total), nil
}

// ListVariables returns a page of variables for the tenant and the total count.
func (r *Repository) ListVariables(ctx context.Context, tenant string, limit, offset int) ([]domain.Variable, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListVariables(ctx, queries.ListVariablesParams{TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("listing variables: %w", err)
	}
	total, err := r.q.CountVariables(ctx, tid)
	if err != nil {
		return nil, 0, fmt.Errorf("counting variables: %w", err)
	}
	out := make([]domain.Variable, 0, len(rows))
	for _, v := range rows {
		out = append(out, domain.Variable{Key: v.Key, Value: v.Value, Description: strOrEmpty(v.Description)})
	}
	return out, int(total), nil
}

// TenantUUID resolves a tenant name to its UUID string — the form the agent
// token carries and that the secret-delivery methods expect.
func (r *Repository) TenantUUID(ctx context.Context, name string) (string, error) {
	tid, err := r.tenantID(ctx, name)
	if err != nil {
		return "", err
	}
	return uuidToString(tid), nil
}

// SecretVariables returns the tenant's variables as key→value, for delivering to
// task pods (ADR 0021). The agent exports them as AIRFLOW_VAR_<KEY>. tenantID is
// the tenant UUID carried by the agent token (not the tenant name).
func (r *Repository) SecretVariables(ctx context.Context, tenantID string) (map[string]string, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return nil, err
	}
	// A high limit; tenants have far fewer variables than this in practice.
	rows, err := r.q.ListVariables(ctx, queries.ListVariablesParams{TenantID: tid, Limit: 10000, Offset: 0})
	if err != nil {
		return nil, fmt.Errorf("listing variables: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, v := range rows {
		out[v.Key] = v.Value
	}
	return out, nil
}

// SecretConnectionURIs returns the tenant's connections as conn_id→Airflow URI
// (password decrypted), for delivering to task pods (ADR 0021). The agent exports
// them as AIRFLOW_CONN_<CONN_ID>. tenantID is the tenant UUID carried by the
// agent token. Never expose these in UI/API responses.
func (r *Repository) SecretConnectionURIs(ctx context.Context, tenantID string) (map[string]string, error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListConnectionSecrets(ctx, tid)
	if err != nil {
		return nil, fmt.Errorf("listing connection secrets: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		// One undecryptable connection (key rotated, or a row encrypted under a
		// different LEOFLOW_SECRET_KEY) must NOT blind the whole tenant: skip it
		// with a warning and deliver the rest. The task that needs the bad
		// connection still fails — correctly — but every other task keeps working.
		pass, derr := r.decryptExtra(row.Password)
		if derr != nil {
			slog.Warn("skipping connection: password decrypt failed (key rotated or wrong LEOFLOW_SECRET_KEY?)",
				"conn_id", row.ConnID, "error", derr)
			continue
		}
		extra, eerr := r.decryptExtra(row.Extra)
		if eerr != nil {
			slog.Warn("skipping connection: extra decrypt failed (key rotated or wrong LEOFLOW_SECRET_KEY?)",
				"conn_id", row.ConnID, "error", eerr)
			continue
		}
		out[row.ConnID] = airflowConnURI(domain.Connection{
			ConnID: row.ConnID, ConnType: row.ConnType, Host: strOrEmpty(row.Host),
			Schema: strOrEmpty(row.ConnSchema), Login: strOrEmpty(row.Login),
			Password: pass, Port: int32PtrToInt(row.Port), Extra: extra,
		})
	}
	return out, nil
}

// SecretVariablesScoped returns only the named subset of the tenant's variables,
// filtered in the query (ADR 0055 D1: scope in the SQL, never post-filter the
// decrypted whole vault in the handler). It backs secret_scoping: enforce, where
// a task receives only the Variables it declared. An empty name set returns
// nothing without a query — enforce's load-bearing [] case. tenantID is the
// tenant UUID carried by the agent token.
func (r *Repository) SecretVariablesScoped(ctx context.Context, tenantID string, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return map[string]string{}, nil
	}
	tid, err := parseUUID(tenantID)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListVariablesScoped(ctx, queries.ListVariablesScopedParams{TenantID: tid, Keys: names})
	if err != nil {
		return nil, fmt.Errorf("listing scoped variables: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, v := range rows {
		out[v.Key] = v.Value
	}
	return out, nil
}

// SecretConnectionURIsScoped returns only the named subset of the tenant's
// connections as Airflow URIs (password decrypted), filtered in the query
// (ADR 0055 D1). It backs secret_scoping: enforce. An empty name set returns
// nothing without a query. Never expose these in UI/API responses. It shares the
// per-connection decrypt-and-skip-on-failure semantics of SecretConnectionURIs:
// one undecryptable connection is skipped with a warning, never blinding the
// rest of the declared set.
func (r *Repository) SecretConnectionURIsScoped(ctx context.Context, tenantID string, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return map[string]string{}, nil
	}
	tid, err := parseUUID(tenantID)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListConnectionSecretsScoped(ctx, queries.ListConnectionSecretsScopedParams{TenantID: tid, ConnIds: names})
	if err != nil {
		return nil, fmt.Errorf("listing scoped connection secrets: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		pass, derr := r.decryptExtra(row.Password)
		if derr != nil {
			slog.Warn("skipping connection: password decrypt failed (key rotated or wrong LEOFLOW_SECRET_KEY?)",
				"conn_id", row.ConnID, "error", derr)
			continue
		}
		extra, eerr := r.decryptExtra(row.Extra)
		if eerr != nil {
			slog.Warn("skipping connection: extra decrypt failed (key rotated or wrong LEOFLOW_SECRET_KEY?)",
				"conn_id", row.ConnID, "error", eerr)
			continue
		}
		out[row.ConnID] = airflowConnURI(domain.Connection{
			ConnID: row.ConnID, ConnType: row.ConnType, Host: strOrEmpty(row.Host),
			Schema: strOrEmpty(row.ConnSchema), Login: strOrEmpty(row.Login),
			Password: pass, Port: int32PtrToInt(row.Port), Extra: extra,
		})
	}
	return out, nil
}

// AlertEndpoint resolves an alert channel connection (#424) to its endpoint for a
// tenant UUID: the decrypted password is the channel URL (the full webhook URL,
// kept encrypted at rest), and an optional `headers` object in the connection's
// extra becomes request headers (e.g. an Authorization header for an endpoint
// whose token is not in the URL). An absent/empty URL is an error — a
// misconfigured alert connection must fail loud. Never expose these in UI/API.
func (r *Repository) AlertEndpoint(ctx context.Context, tenantID, connID string) (endpointURL string, headers map[string]string, err error) {
	tid, err := parseUUID(tenantID)
	if err != nil {
		return "", nil, err
	}
	row, err := r.q.GetConnection(ctx, queries.GetConnectionParams{TenantID: tid, ConnID: connID})
	if err != nil {
		return "", nil, mapNotFound(err)
	}
	if row.Password == nil {
		return "", nil, fmt.Errorf("connection %q has no secret to resolve", connID)
	}
	endpointURL, err = r.decryptExtra(row.Password)
	if err != nil {
		return "", nil, fmt.Errorf("decrypting connection %q secret: %w", connID, err)
	}
	if endpointURL == "" {
		return "", nil, fmt.Errorf("connection %q secret is empty", connID)
	}
	headers, err = r.alertHeaders(connID, row.Extra)
	if err != nil {
		return "", nil, err
	}
	return endpointURL, headers, nil
}

// alertHeaders decrypts a connection's extra and extracts its optional `headers`
// object (a string→string map), the conventional Airflow HTTP-connection shape.
// A nil/empty or headerless extra yields an empty map (not an error), so the
// caller can range over it unconditionally.
func (r *Repository) alertHeaders(connID string, enc *string) (map[string]string, error) {
	headers := map[string]string{}
	extra, err := r.decryptExtra(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypting connection %q extra: %w", connID, err)
	}
	if extra == "" {
		return headers, nil
	}
	var parsed struct {
		Headers map[string]string `json:"headers"`
	}
	if uerr := json.Unmarshal([]byte(extra), &parsed); uerr != nil {
		return nil, fmt.Errorf("parsing connection %q extra: %w", connID, uerr)
	}
	for k, v := range parsed.Headers {
		headers[k] = v
	}
	return headers, nil
}

// AddFavorite marks a DAG as a favorite for the user (idempotent).
func (r *Repository) AddFavorite(ctx context.Context, tenant, userID, dagID string) error {
	if err := r.q.AddFavorite(ctx, queries.AddFavoriteParams{Tenant: tenant, UserID: userID, DagID: dagID}); err != nil {
		return fmt.Errorf("adding favorite: %w", err)
	}
	return nil
}

// RemoveFavorite clears a DAG's favorite mark for the user (idempotent).
func (r *Repository) RemoveFavorite(ctx context.Context, tenant, userID, dagID string) error {
	if err := r.q.RemoveFavorite(ctx, queries.RemoveFavoriteParams{Tenant: tenant, UserID: userID, DagID: dagID}); err != nil {
		return fmt.Errorf("removing favorite: %w", err)
	}
	return nil
}

// FavoriteDagIDs returns the set of DAG ids the user has favorited.
func (r *Repository) FavoriteDagIDs(ctx context.Context, tenant, userID string) (map[string]bool, error) {
	ids, err := r.q.ListFavoriteDagIDs(ctx, queries.ListFavoriteDagIDsParams{Tenant: tenant, UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("listing favorites: %w", err)
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// GetVariable returns one variable by key, or ErrNotFound.
func (r *Repository) GetVariable(ctx context.Context, tenant, key string) (domain.Variable, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.Variable{}, err
	}
	v, err := r.q.GetVariable(ctx, queries.GetVariableParams{TenantID: tid, Key: key})
	if err != nil {
		return domain.Variable{}, mapNotFound(err)
	}
	return domain.Variable{Key: v.Key, Value: v.Value, Description: strOrEmpty(v.Description)}, nil
}

// SetVariable creates or updates a variable, always overwriting the value and
// preserving description when empty. It is the full-write convenience the
// storage tests and internal callers use; the tri-state API write path goes
// through SetVariablePatch.
func (r *Repository) SetVariable(ctx context.Context, tenant string, v domain.Variable) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	if err := r.q.UpsertVariable(ctx, queries.UpsertVariableParams{
		TenantID: tid, Key: v.Key, Value: v.Value, Description: strPtr(v.Description),
	}); err != nil {
		return fmt.Errorf("upserting variable: %w", err)
	}
	return nil
}

// SetVariablePatch upserts a variable with tri-state semantics on description
// (#887): a nil Description is preserved (NULL param → COALESCE), a non-nil one
// is written ("" clears it). The `value` column is NOT NULL, so value cannot be
// preserved at the SQL layer (a NULL param is rejected on the INSERT arm before
// COALESCE); the API handler therefore resolves an omitted/masked value to the
// stored value before calling here, and Value is expected non-nil (a nil Value
// defensively clears to empty rather than panicking).
func (r *Repository) SetVariablePatch(ctx context.Context, tenant string, p domain.VariablePatch) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	value := ""
	if p.Value != nil {
		value = *p.Value
	}
	if err := r.q.UpsertVariable(ctx, queries.UpsertVariableParams{
		TenantID: tid, Key: p.Key, Value: value, Description: p.Description,
	}); err != nil {
		return fmt.Errorf("upserting variable: %w", err)
	}
	return nil
}

// DeleteVariable removes a variable, returning ErrNotFound when none matched.
func (r *Repository) DeleteVariable(ctx context.Context, tenant, key string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	rows, err := r.q.DeleteVariable(ctx, queries.DeleteVariableParams{TenantID: tid, Key: key})
	if err != nil {
		return fmt.Errorf("deleting variable: %w", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// encOrEmpty encrypts a non-empty value, returning a nil pointer for empty input.
func (r *Repository) encOrEmpty(plain string) (*string, error) {
	if plain == "" {
		return nil, nil //nolint:nilnil // empty secret maps to a NULL column; no value and no error is correct
	}
	enc, err := r.cipher.Encrypt(plain)
	if err != nil {
		return nil, err
	}
	return &enc, nil
}

// SetConnection creates or updates a connection, encrypting password and extra
// at rest. It fails if no encryption cipher is configured (never stores a
// credential in plaintext — ADR 0019).
func (r *Repository) SetConnection(ctx context.Context, tenant string, c domain.Connection) error {
	if r.cipher == nil {
		return secrets.ErrNoKey
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	encPass, err := r.encOrEmpty(c.Password)
	if err != nil {
		return fmt.Errorf("encrypting password: %w", err)
	}
	encExtra, err := r.encOrEmpty(c.Extra)
	if err != nil {
		return fmt.Errorf("encrypting extra: %w", err)
	}
	var port *int32
	if c.Port != nil {
		p := toInt32(*c.Port)
		port = &p
	}
	return r.q.UpsertConnection(ctx, queries.UpsertConnectionParams{
		TenantID: tid, ConnID: c.ConnID, ConnType: c.ConnType,
		Host: strPtr(c.Host), ConnSchema: strPtr(c.Schema), Login: strPtr(c.Login),
		Password: encPass, Port: port, Extra: encExtra, Description: strPtr(c.Description),
	})
}

// encPatch encrypts a tri-state secret param (#887): a nil pointer preserves the
// stored column (NULL), a non-empty value is encrypted, and an explicit empty
// string clears the column to empty (stored as-is — an empty value is not a
// secret, so there is nothing to encrypt, and decryptExtra returns "" for it).
// This differs from encOrEmpty, which collapses "" to NULL (preserve) and so
// cannot express a clear.
func (r *Repository) encPatch(p *string) (*string, error) {
	if p == nil {
		return nil, nil //nolint:nilnil // nil param preserves the stored value via COALESCE
	}
	if *p == "" {
		empty := ""
		return &empty, nil // explicit clear: overwrite with empty, not preserve
	}
	enc, err := r.cipher.Encrypt(*p)
	if err != nil {
		return nil, err
	}
	return &enc, nil
}

// SetConnectionPatch upserts a connection with tri-state field semantics (#887):
// a nil field is preserved (NULL param → COALESCE keeps the stored value), a
// non-nil field is written exactly ("" clears it). Password and extra are
// encrypted when set. It is how the API write path distinguishes an omitted
// field from an explicit empty one without reintroducing the password-wipe, and
// where a masked secret round-trip is preserved (the handler maps a masked
// password to a nil field, and unmasks extra, before calling here). It fails if
// no encryption cipher is configured (never stores a credential in plaintext —
// ADR 0019).
func (r *Repository) SetConnectionPatch(ctx context.Context, tenant string, p domain.ConnectionPatch) error {
	if r.cipher == nil {
		return secrets.ErrNoKey
	}
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	encPass, err := r.encPatch(p.Password)
	if err != nil {
		return fmt.Errorf("encrypting password: %w", err)
	}
	encExtra, err := r.encPatch(p.Extra)
	if err != nil {
		return fmt.Errorf("encrypting extra: %w", err)
	}
	var port *int32
	if p.Port != nil {
		pp := toInt32(*p.Port)
		port = &pp
	}
	return r.q.UpsertConnection(ctx, queries.UpsertConnectionParams{
		TenantID: tid, ConnID: p.ConnID, ConnType: p.ConnType,
		Host: p.Host, ConnSchema: p.Schema, Login: p.Login,
		Password: encPass, Port: port, Extra: encExtra, Description: p.Description,
	})
}

// GetConnection returns a connection with extra decrypted; the password is not
// returned (write-only). Returns ErrNotFound when absent.
func (r *Repository) GetConnection(ctx context.Context, tenant, connID string) (domain.Connection, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.Connection{}, err
	}
	row, err := r.q.GetConnection(ctx, queries.GetConnectionParams{TenantID: tid, ConnID: connID})
	if err != nil {
		return domain.Connection{}, mapNotFound(err)
	}
	extra, err := r.decryptExtra(row.Extra)
	if err != nil {
		return domain.Connection{}, err
	}
	return domain.Connection{
		ConnID: row.ConnID, ConnType: row.ConnType, Host: strOrEmpty(row.Host),
		Schema: strOrEmpty(row.ConnSchema), Login: strOrEmpty(row.Login),
		Port: int32PtrToInt(row.Port), Extra: extra, Description: strOrEmpty(row.Description),
	}, nil
}

// ListConnections returns a page of connections (no passwords) and the total.
func (r *Repository) ListConnections(ctx context.Context, tenant string, limit, offset int) ([]domain.Connection, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListConnections(ctx, queries.ListConnectionsParams{TenantID: tid, Limit: toInt32(limit), Offset: toInt32(offset)})
	if err != nil {
		return nil, 0, fmt.Errorf("listing connections: %w", err)
	}
	total, err := r.q.CountConnections(ctx, tid)
	if err != nil {
		return nil, 0, fmt.Errorf("counting connections: %w", err)
	}
	out := make([]domain.Connection, 0, len(rows))
	for _, row := range rows {
		extra, derr := r.decryptExtra(row.Extra)
		if derr != nil {
			return nil, 0, derr
		}
		out = append(out, domain.Connection{
			ConnID: row.ConnID, ConnType: row.ConnType, Host: strOrEmpty(row.Host),
			Schema: strOrEmpty(row.ConnSchema), Login: strOrEmpty(row.Login),
			Port: int32PtrToInt(row.Port), Extra: extra, Description: strOrEmpty(row.Description),
		})
	}
	return out, int(total), nil
}

// decryptExtra decrypts a stored extra blob, tolerating a nil cipher (returns
// empty) and an empty value.
func (r *Repository) decryptExtra(enc *string) (string, error) {
	if enc == nil || *enc == "" || r.cipher == nil {
		return "", nil
	}
	plain, err := r.cipher.Decrypt(*enc)
	if err != nil {
		return "", fmt.Errorf("decrypting extra: %w", err)
	}
	return plain, nil
}

// DeleteConnection removes a connection, returning ErrNotFound when none matched.
func (r *Repository) DeleteConnection(ctx context.Context, tenant, connID string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	rows, err := r.q.DeleteConnection(ctx, queries.DeleteConnectionParams{TenantID: tid, ConnID: connID})
	if err != nil {
		return fmt.Errorf("deleting connection: %w", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// int32PtrToInt converts a nullable int32 column to a *int.
func int32PtrToInt(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

// ClearDagHistory deletes a DAG's runs (cascading task instances and XCom index
// rows) while keeping the DAG and its versions registered — the safe "clear"
// the UI trash maps to (ADR 0020). Returns ErrNotFound when the DAG is absent.
func (r *Repository) ClearDagHistory(ctx context.Context, tenant, dagID string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	if _, err := r.q.ClearDagRuns(ctx, dag.ID); err != nil {
		return fmt.Errorf("clearing dag history: %w", err)
	}
	return nil
}

// ListImportErrors returns the tenant's DAG parse/compile errors, newest first.
func (r *Repository) ListImportErrors(ctx context.Context, tenant string) ([]domain.ImportError, error) {
	rows, err := r.q.ListImportErrors(ctx, tenant)
	if err != nil {
		return nil, fmt.Errorf("listing import errors: %w", err)
	}
	out := make([]domain.ImportError, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.ImportError{
			ID:         uuidToString(row.ID),
			Filename:   row.Filename,
			StackTrace: row.Stacktrace,
			BundleName: strOrEmpty(row.BundleName),
			Timestamp:  timeVal(row.CreatedAt),
		})
	}
	return out, nil
}

// SetImportError records (or replaces) the parse/compile error for a file.
func (r *Repository) SetImportError(ctx context.Context, tenant string, e domain.ImportError) error {
	var bundle *string
	if e.BundleName != "" {
		bundle = &e.BundleName
	}
	if err := r.q.UpsertImportError(ctx, queries.UpsertImportErrorParams{
		Tenant: tenant, Filename: e.Filename, Stacktrace: e.StackTrace, BundleName: bundle,
	}); err != nil {
		return fmt.Errorf("upserting import error: %w", err)
	}
	return nil
}

// ClearImportError removes any recorded error for a file (a good re-import).
func (r *Repository) ClearImportError(ctx context.Context, tenant, filename string) error {
	if err := r.q.DeleteImportError(ctx, queries.DeleteImportErrorParams{Tenant: tenant, Filename: filename}); err != nil {
		return fmt.Errorf("clearing import error: %w", err)
	}
	return nil
}

// EnsureTenant creates the tenant name with the built-in roles, their
// permissions and the default pool copied from the "default" tenant, all in
// one transaction (#1283). It is idempotent: for an existing tenant it fills in
// anything missing and reports created=false.
func (r *Repository) EnsureTenant(ctx context.Context, name, displayName string) (created bool, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning ensure-tenant tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := r.q.WithTx(tx)
	n, err := qtx.InsertTenantIfMissing(ctx, queries.InsertTenantIfMissingParams{Name: name, DisplayName: strPtr(displayName)})
	if err != nil {
		return false, fmt.Errorf("inserting tenant: %w", err)
	}
	t, err := qtx.GetTenantByName(ctx, name)
	if err != nil {
		return false, fmt.Errorf("loading tenant: %w", err)
	}
	if err := qtx.CopyDefaultSystemRoles(ctx, t.ID); err != nil {
		return false, fmt.Errorf("seeding roles: %w", err)
	}
	if err := qtx.CopyDefaultRolePermissions(ctx, t.ID); err != nil {
		return false, fmt.Errorf("seeding role permissions: %w", err)
	}
	if err := qtx.InsertDefaultPool(ctx, t.ID); err != nil {
		return false, fmt.Errorf("seeding default pool: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing ensure-tenant tx: %w", err)
	}
	return n > 0, nil
}

// TenantRolePermissions lists a tenant's built-in role grants as
// "role:action:resource", so a caller can compare two tenants' ladders.
func (r *Repository) TenantRolePermissions(ctx context.Context, tenant string) ([]string, error) {
	return r.q.ListTenantRolePermissions(ctx, tenant)
}

// TenantHasDefaultPool reports whether the tenant has its default pool.
func (r *Repository) TenantHasDefaultPool(ctx context.Context, tenant string) (bool, error) {
	return r.q.TenantHasDefaultPool(ctx, tenant)
}

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
