package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

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
		out = append(out, dagVersionOf(row))
	}
	return out, nil
}

// dagVersionOf maps a listed version row to the domain version.
func dagVersionOf(row queries.ListDagVersionsRow) domain.DagVersion {
	return domain.DagVersion{
		ID:             uuidToString(row.ID),
		VersionNumber:  int(row.VersionNumber),
		CreatedAt:      timeVal(row.CreatedAt),
		Version:        row.Version,
		ImageReference: row.ImageReference,
		SpecHash:       row.SpecHash,
		IsCurrent:      row.IsCurrent,
		CreatedBy:      row.CreatedBy,
		BuildInfo:      buildInfoOf(uuidToString(row.ID), row.BuildInfo),
	}
}

// buildInfoOf decodes the build_info column of version versionID: nil when
// it is NULL. A value that does not decode is logged and read as nil, since
// provenance is shown beside the version and must not make it unlistable.
func buildInfoOf(versionID string, raw []byte) *domain.BuildInfo {
	if len(raw) == 0 {
		return nil
	}
	var b domain.BuildInfo
	if err := json.Unmarshal(raw, &b); err != nil {
		slog.Warn("listing a dag version without its build info: the stored value does not decode",
			"dag_version_id", versionID, "error", err)
		return nil
	}
	if b.IsZero() {
		return nil
	}
	return &b
}

// GetCurrentSpec returns the parsed spec of the DAG's current version, or
// domain.ErrNotFound if the DAG or its current version does not exist.
func (r *Repository) GetCurrentSpec(ctx context.Context, tenant, dagID string) (domain.DAGSpec, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DAGSpec{}, err
	}
	// Only the current version id is read here; the spec itself comes from the
	// shared cache keyed by that immutable id, so a grid or graph poll neither
	// ships the spec JSON over the wire nor decodes it again. The returned spec
	// is shared and must not be mutated (see specCache).
	dag, err := r.q.GetDagByDagID(ctx, queries.GetDagByDagIDParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return domain.DAGSpec{}, mapNotFound(err)
	}
	if !dag.CurrentVersionID.Valid {
		return domain.DAGSpec{}, domain.ErrNotFound
	}
	_, spec, err := r.specs.getCurrent(ctx, r.q, currentSpecKey{tenant: tid, dagID: dagID}, dag.CurrentVersionID)
	if err != nil {
		return domain.DAGSpec{}, fmt.Errorf("loading current spec: %w", err)
	}
	return spec, nil
}

// RegisterDagVersion inserts a version of the DAG keyed by specHash and makes
// it current, creating the DAG on its first registration. It is idempotent: an
// existing hash yields created=false and writes nothing, even when that hash is
// an older version than the current one.
//
// Everything runs in one transaction that holds the DAG's row lock, so
// registers of one DAG are serialized and a refused or failed register leaves
// no partial state (#1702). The dags row's metadata (schedule, catchup,
// start_date, max_active_runs, description, owner, tags) is rewritten only when
// the new version becomes current, so it always describes the current version.
// is_paused is set only when this register creates the DAG (pausedOnCreation);
// a new version of an existing DAG never changes it.
func (r *Repository) RegisterDagVersion(ctx context.Context, tenant string, spec domain.DAGSpec, specHash string) (bool, error) {
	return r.RegisterDagVersionBy(ctx, tenant, "", spec, specHash)
}

// RegisterDagVersionBy is RegisterDagVersion on behalf of the user actorUserID:
// the version's created_by and its audit entry name that user. An actor that is
// not a user id (empty, or Lite's dev principal) leaves both without a user.
func (r *Repository) RegisterDagVersionBy(ctx context.Context, tenant, actorUserID string, spec domain.DAGSpec, specHash string) (bool, error) {
	got, err := r.RegisterDagVersionAs(ctx, tenant, domain.DagRegistration{Spec: spec, SpecHash: specHash, ActorUserID: actorUserID})
	return got.Created, err
}

// RegisterDagVersionAs is RegisterDagVersionBy with an optional is_paused
// (reg.IsPaused): when set, the DAG ends the register in that state, whether
// the register creates it, adds a version or finds the content already
// registered. A change to an existing DAG is audited as dag.pause or
// dag.unpause in the same transaction, and the register's own audit entry
// records the requested state. It reports whether a version was created and
// the DAG's is_paused afterwards.
func (r *Repository) RegisterDagVersionAs(ctx context.Context, tenant string, reg domain.DagRegistration) (domain.DagRegistered, error) {
	spec := reg.Spec
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DagRegistered{}, err
	}
	// A DAG may not declare a variable/connection that does not exist for the
	// tenant (ADR 0055 D6). Checked before any write so a bad declaration fails
	// fast with no partial state. An empty declaration is always valid, so no
	// pre-declaration DAG is ever rejected.
	if verr := r.validateDeclaredSecrets(ctx, tid, spec); verr != nil {
		return domain.DagRegistered{}, verr
	}
	// The tenant's limits (max_dags, min_schedule_interval_seconds), also
	// before any write.
	if lerr := checkRegistrationLimits(ctx, r.q, tid, spec); lerr != nil {
		return domain.DagRegistered{}, lerr
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return domain.DagRegistered{}, fmt.Errorf("encoding spec: %w", err)
	}
	paused := r.pausedOnCreation(spec)
	if reg.IsPaused != nil {
		paused = *reg.IsPaused
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.DagRegistered{}, fmt.Errorf("beginning register tx: %w", err)
	}
	// Rollback after a successful commit is a no-op; on any early return (409,
	// identical hash, failed write) it undoes everything this register wrote.
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	got, err := registerDagVersionTx(ctx, r.q.WithTx(tx), tid, registration{
		spec: spec, specHash: reg.SpecHash, specJSON: specJSON,
		paused: paused, pausedOverride: reg.IsPaused != nil, actor: actorUUID(reg.ActorUserID),
		promotedFrom: reg.PromotedFrom, buildInfo: reg.BuildInfo, requireDigest: reg.RequireDigest,
	})
	if err != nil {
		return domain.DagRegistered{}, err
	}
	// A dry run (#1705) stops here, before the commit: the deferred rollback
	// undoes every write, audit entries included, so its outcome and its
	// errors are exactly the register's and nothing remains.
	if !got.wrote || reg.DryRun {
		return got.DagRegistered, nil
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return domain.DagRegistered{}, fmt.Errorf("committing register tx: %w", cerr)
	}
	return got.DagRegistered, nil
}

// registerOutcome is what registerDagVersionTx did: the caller's outcome, and
// whether anything was written that needs a commit.
type registerOutcome struct {
	domain.DagRegistered
	wrote bool
}

// registerDagVersionTx is the body of RegisterDagVersion, run on a transaction:
// create the DAG if missing, lock its row, apply an explicit is_paused, decide
// on the hash, insert the version, then make it current and apply its
// metadata.
//
// reg.paused is is_paused for a DAG this register creates; when the DAG exists
// it is applied only with reg.pausedOverride, so a plain redeploy never pauses
// or unpauses it.
func registerDagVersionTx(ctx context.Context, qtx *queries.Queries, tid pgtype.UUID, reg registration) (registerOutcome, error) {
	spec, specHash := reg.spec, reg.specHash
	meta := dagMetadataOf(spec)
	dag, dagCreated, err := ensureAndLockDag(ctx, qtx, tid, spec.DagID, meta, reg.paused)
	if err != nil {
		return registerOutcome{}, err
	}
	actor, err := tenantActor(ctx, qtx, tid, reg.actor)
	if err != nil {
		return registerOutcome{}, err
	}
	out := registerOutcome{DagRegistered: domain.DagRegistered{IsPaused: dag.IsPaused, DagCreated: dagCreated}}
	if reg.pausedOverride && dag.IsPaused != reg.paused {
		if perr := setPausedByRegister(ctx, qtx, tid, actor, spec, reg.paused); perr != nil {
			return registerOutcome{}, perr
		}
		out.IsPaused, out.wrote = reg.paused, true
	}
	if _, verr := qtx.GetDagVersionByHash(ctx, queries.GetDagVersionByHashParams{DagID: dag.ID, SpecHash: specHash, TenantID: tid}); verr == nil {
		return out, nil
	} else if !errors.Is(verr, pgx.ErrNoRows) {
		return registerOutcome{}, fmt.Errorf("checking existing version: %w", verr)
	}
	buildInfo, err := reg.buildInfoJSON()
	if err != nil {
		return registerOutcome{}, err
	}
	version, err := qtx.InsertDagVersion(ctx, queries.InsertDagVersionParams{
		DagID:          dag.ID,
		TenantID:       tid,
		Version:        spec.DagVersion,
		ImageReference: spec.Image,
		Spec:           reg.specJSON,
		SpecHash:       specHash,
		CreatedBy:      actor,
		BuildInfo:      buildInfo,
	})
	if err != nil {
		// A collision on dag_versions_unique (dag_id, version) means the same
		// version string was pushed with different content, the hash dedup above
		// only short-circuits identical re-pushes. Versions are immutable, so this
		// is a genuine conflict, not an overwrite: map it to domain.ErrConflict
		// so the API answers 409 instead of 500 (#746, where it also leaked the
		// raw 23505, which #961 closed for every code). Bump --dag-version to
		// push new content.
		return registerOutcome{}, fmt.Errorf("inserting version: %w", mapConflict(err))
	}
	meta.ID, meta.TenantID = dag.ID, tid
	if err := qtx.UpdateDagMetadata(ctx, meta); err != nil {
		return registerOutcome{}, fmt.Errorf("updating dag metadata: %w", err)
	}
	if err := qtx.SetCurrentDagVersion(ctx, queries.SetCurrentDagVersionParams{ID: dag.ID, CurrentVersionID: version.ID, TenantID: tid}); err != nil {
		return registerOutcome{}, fmt.Errorf("setting current version: %w", err)
	}
	if err := writeDagAudit(ctx, qtx, tid, actor, "dag.version.register", spec.DagID, reg.auditFields()); err != nil {
		return registerOutcome{}, err
	}
	out.Created, out.wrote = true, true
	return out, nil
}

// setPausedByRegister sets is_paused of an existing DAG for a register that
// asked for it, and audits the change as dag.pause or dag.unpause.
func setPausedByRegister(ctx context.Context, qtx *queries.Queries, tid, actor pgtype.UUID, spec domain.DAGSpec, paused bool) error {
	if _, err := qtx.SetDagPaused(ctx, queries.SetDagPausedParams{TenantID: tid, DagID: spec.DagID, IsPaused: paused}); err != nil {
		return fmt.Errorf("setting is_paused: %w", err)
	}
	return writePauseAudit(ctx, qtx, tid, actor, spec.DagID, paused, map[string]any{
		"via": "register", "version": spec.DagVersion,
	})
}

// registration is what one register writes: the spec, its hash and encoding,
// is_paused for a DAG it creates (or for any DAG with pausedOverride), the
// registering user (null when none), for a promoted version the control
// plane it came from (nil otherwise), where the version was built (nil
// when the register sent none) and the deploy.require_digest it was
// admitted under.
type registration struct {
	spec           domain.DAGSpec
	specHash       string
	specJSON       []byte
	paused         bool
	pausedOverride bool
	actor          pgtype.UUID
	promotedFrom   *domain.DagPromotion
	buildInfo      *domain.BuildInfo
	requireDigest  domain.DigestRequirement
}

// buildInfoJSON encodes the build info of a register for its column: nil
// (NULL) when the register sent none or an empty one.
func (reg registration) buildInfoJSON() ([]byte, error) {
	if reg.buildInfo == nil || reg.buildInfo.IsZero() {
		return nil, nil
	}
	out, err := json.Marshal(reg.buildInfo)
	if err != nil {
		return nil, fmt.Errorf("encoding build info: %w", err)
	}
	return out, nil
}

// auditFields is the metadata of the register's audit entry: the version,
// image and hash, and what the register was sent beyond the spec.
func (reg registration) auditFields() map[string]any {
	spec := reg.spec
	fields := map[string]any{"version": spec.DagVersion, "image": spec.Image, "spec_hash": reg.specHash}
	if reg.pausedOverride {
		fields["is_paused"] = reg.paused
	}
	if p := reg.promotedFrom; p != nil {
		fields["promoted_from"] = map[string]string{"target": p.Target, "server": p.Server}
	}
	if b := reg.buildInfo; b != nil && !b.IsZero() {
		fields["build_info"] = *b
	}
	if reg.requireDigest.Active() {
		fields["require_digest"] = string(reg.requireDigest)
		fields["image_pinned"] = domain.IsDigestPinned(spec.Image)
	}
	return fields
}

// actorUUID is the user id of an acting principal, or a null UUID when the
// principal is not a user row (no principal, or Lite's dev principal).
func actorUUID(userID string) pgtype.UUID {
	u, err := parseUUID(userID)
	if err != nil {
		return pgtype.UUID{}
	}
	return u
}

// tenantActor returns actor when it is a user of the tenant, else a null UUID:
// a token whose user row is gone, or names another tenant, records no user
// instead of failing the write on a foreign key.
func tenantActor(ctx context.Context, qtx *queries.Queries, tid, actor pgtype.UUID) (pgtype.UUID, error) {
	if !actor.Valid {
		return pgtype.UUID{}, nil
	}
	id, err := qtx.GetTenantUserID(ctx, queries.GetTenantUserIDParams{ID: actor, TenantID: tid})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, nil
	}
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("looking up the acting user: %w", err)
	}
	return id, nil
}

// writeDagAudit records action on dagID by actor, with fields as metadata.
func writeDagAudit(ctx context.Context, qtx *queries.Queries, tid, actor pgtype.UUID, action, dagID string, fields map[string]any) error {
	meta, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding audit metadata: %w", err)
	}
	if err := qtx.CreateAuditLog(ctx, queries.CreateAuditLogParams{
		TenantID: tid, UserID: actor, Action: action,
		ResourceType: strPtr("dag"), ResourceID: strPtr(dagID), Metadata: meta,
	}); err != nil {
		return fmt.Errorf("writing audit log: %w", err)
	}
	return nil
}

// pausedOnCreation is whether spec's DAG starts paused if this register creates
// it: the spec's is_paused_upon_creation when set, else the server's
// dags_are_paused_at_creation, as in Airflow (#1705).
func (r *Repository) pausedOnCreation(spec domain.DAGSpec) bool {
	if spec.IsPausedUponCreation != nil {
		return *spec.IsPausedUponCreation
	}
	return r.pausedAtCreation
}

// ensureAndLockDag creates the DAG's row when it is missing and locks it. A
// DeleteDag that commits between the two statements leaves nothing to lock;
// the pair is then tried once more, which creates the DAG again. A DAG that is
// gone a second time is reported as a conflict for the caller to retry.
// paused is is_paused for a DAG this call creates; created reports whether
// it created the row.
func ensureAndLockDag(ctx context.Context, qtx *queries.Queries, tid pgtype.UUID, dagID string, meta queries.UpdateDagMetadataParams, paused bool) (dag queries.Dag, created bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		inserted, eerr := qtx.EnsureDag(ctx, queries.EnsureDagParams{
			TenantID: tid, DagID: dagID,
			Description: meta.Description, Owner: meta.Owner, Tags: meta.Tags,
			Schedule: meta.Schedule, ScheduleTimezone: meta.ScheduleTimezone, StartDate: meta.StartDate,
			MaxActiveRuns: meta.MaxActiveRuns, Catchup: meta.Catchup, IsPaused: paused,
		})
		if eerr != nil {
			return queries.Dag{}, false, fmt.Errorf("creating dag: %w", eerr)
		}
		created = created || inserted > 0
		dag, err = qtx.LockDagByDagID(ctx, queries.LockDagByDagIDParams{TenantID: tid, DagID: dagID})
		if err == nil {
			return dag, created, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return queries.Dag{}, false, fmt.Errorf("locking dag: %w", err)
		}
	}
	return queries.Dag{}, false, domain.Safef(domain.ErrConflict,
		"dag %q was deleted while this version was being registered; push it again", dagID)
}

// dagMetadataOf maps a spec to the dags-row columns it owns while it is the
// current version. ID and TenantID are left for the caller.
func dagMetadataOf(spec domain.DAGSpec) queries.UpdateDagMetadataParams {
	maxRuns := spec.MaxActiveRuns
	if maxRuns == 0 {
		maxRuns = defaultMaxActiveRuns
	}
	return queries.UpdateDagMetadataParams{
		Description:      strPtr(spec.Description),
		Owner:            strPtr(spec.Owner),
		Tags:             spec.Tags,
		Schedule:         spec.Schedule,
		ScheduleTimezone: strPtr(spec.ScheduleTZ),
		StartDate:        parseTimestamptz(spec.StartDate),
		MaxActiveRuns:    toInt32(maxRuns),
		Catchup:          spec.Catchup,
	}
}

// validateDeclaredSecrets rejects a spec that declares a variable or connection
// name that does not exist for the tenant (ADR 0055 D6). It gathers every
// declared name, DAG-level and per-task narrowing, and confirms each with one
// existence query per kind. A name renamed in the UI but not in the DAG surfaces
// here at registration rather than silently at run time; the error names both
// the DAG and the offending names so the author knows which side to fix.
//
// An empty declaration is always valid. Because declaration is new, no existing
// DAG declares anything, so this never rejects a pre-declaration DAG, the
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
// provider call, confirming it in the provider would be exactly the
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
