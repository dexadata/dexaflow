package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

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
	return r.SetPausedBy(ctx, tenant, "", dagID, paused)
}

// SetPausedBy is SetPaused on behalf of the user actorUserID: a change of
// is_paused is audited as dag.pause or dag.unpause in the same transaction;
// a request for the state the DAG is already in records nothing.
func (r *Repository) SetPausedBy(ctx context.Context, tenant, actorUserID, dagID string, paused bool) (domain.DAG, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DAG{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.DAG{}, fmt.Errorf("beginning pause tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	qtx := r.q.WithTx(tx)
	before, err := qtx.LockDagByDagID(ctx, queries.LockDagByDagIDParams{TenantID: tid, DagID: dagID})
	if err != nil {
		return domain.DAG{}, mapNotFound(err)
	}
	d, err := qtx.SetDagPaused(ctx, queries.SetDagPausedParams{TenantID: tid, DagID: dagID, IsPaused: paused})
	if err != nil {
		return domain.DAG{}, mapNotFound(err)
	}
	if before.IsPaused != paused {
		actor, aerr := tenantActor(ctx, qtx, tid, actorUUID(actorUserID))
		if aerr != nil {
			return domain.DAG{}, aerr
		}
		if werr := writePauseAudit(ctx, qtx, tid, actor, dagID, paused, map[string]any{"via": "api"}); werr != nil {
			return domain.DAG{}, werr
		}
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return domain.DAG{}, fmt.Errorf("committing pause tx: %w", cerr)
	}
	return mapDag(d), nil
}

// writePauseAudit records a change of is_paused on dagID as dag.pause or
// dag.unpause, with fields added to the metadata.
func writePauseAudit(ctx context.Context, qtx *queries.Queries, tid, actor pgtype.UUID, dagID string, paused bool, fields map[string]any) error {
	action := "dag.unpause"
	if paused {
		action = "dag.pause"
	}
	fields["is_paused"] = paused
	return writeDagAudit(ctx, qtx, tid, actor, action, dagID, fields)
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

// DeleteDag removes a DAG and (via ON DELETE CASCADE) its versions, runs, task
// instances, and XCom index rows. It returns ErrNotFound when no DAG matched.
func (r *Repository) DeleteDag(ctx context.Context, tenant, dagID string) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	// The cascade covers every version, run and task instance of the DAG, so
	// it runs without the API statement timeout.
	var rows int64
	err = withoutStatementTimeout(ctx, r.pool, r.q, func(q *queries.Queries) error {
		var derr error
		rows, derr = q.DeleteDag(ctx, queries.DeleteDagParams{TenantID: tid, DagID: dagID})
		return derr
	})
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

// ClearDagHistory deletes a DAG's runs (cascading task instances and XCom index
// rows) while keeping the DAG and its versions registered, the safe "clear"
// the UI trash maps to (ADR 0020). Returns ErrNotFound when the DAG is absent.
func (r *Repository) ClearDagHistory(ctx context.Context, tenant, dagID string) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	// Like DeleteDag, the cascade covers every run and task instance of the
	// DAG, so it runs without the API statement timeout.
	err = withoutStatementTimeout(ctx, r.pool, r.q, func(q *queries.Queries) error {
		_, cerr := q.ClearDagRuns(ctx, queries.ClearDagRunsParams{TenantID: dag.TenantID, DagID: dag.ID})
		return cerr
	})
	if err != nil {
		return fmt.Errorf("clearing dag history: %w", err)
	}
	return nil
}

// ListDagsWithFilter returns a page of active DAGs matching the given filter
// criteria and the total count of matching DAGs.
func (r *Repository) ListDagsWithFilter(ctx context.Context, tenant string, filter domain.DagFilter) ([]domain.DAG, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	var rs *queries.DagRunState
	if filter.LastDagRunState != "" {
		s := queries.DagRunState(filter.LastDagRunState)
		rs = &s
	}
	rows, err := r.q.ListDagsWithFilter(ctx, queries.ListDagsWithFilterParams{
		TenantID:                    tid,
		Limit:                       toInt32(filter.Limit),
		Offset:                      toInt32(filter.Offset),
		Paused:                      filter.Paused,
		RunState:                    rs,
		DagIDPattern:                strPtr(filter.DagIDPattern),
		DagIDPrefixPattern:          strPtr(filter.DagIDPrefixPattern),
		DagDisplayNamePattern:       strPtr(filter.DagDisplayNamePattern),
		DagDisplayNamePrefixPattern: strPtr(filter.DagDisplayNamePrefixPattern),
		Owners:                      filter.Owners,
		Tags:                        filter.Tags,
		TagsMatchMode:               strPtr(filter.TagsMatchMode),
		IsFavorite:                  filter.IsFavorite,
		UserID:                      strPtr(filter.UserID),
		OrderBy:                     strPtr(filter.OrderBy),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing dags with filter: %w", err)
	}
	total, err := r.q.CountDagsWithFilter(ctx, queries.CountDagsWithFilterParams{
		TenantID:                    tid,
		Paused:                      filter.Paused,
		RunState:                    rs,
		DagIDPattern:                strPtr(filter.DagIDPattern),
		DagIDPrefixPattern:          strPtr(filter.DagIDPrefixPattern),
		DagDisplayNamePattern:       strPtr(filter.DagDisplayNamePattern),
		DagDisplayNamePrefixPattern: strPtr(filter.DagDisplayNamePrefixPattern),
		Owners:                      filter.Owners,
		Tags:                        filter.Tags,
		TagsMatchMode:               strPtr(filter.TagsMatchMode),
		IsFavorite:                  filter.IsFavorite,
		UserID:                      strPtr(filter.UserID),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("counting dags with filter: %w", err)
	}
	out := make([]domain.DAG, 0, len(rows))
	for _, d := range rows {
		out = append(out, mapDagWithFilter(d))
	}
	return out, int(total), nil
}

// ListDagTags returns a page of the tenant's distinct DAG tags, narrowed by an
// optional pattern (anywhere in the name) and prefix pattern, and the total.
func (r *Repository) ListDagTags(ctx context.Context, tenant, pattern, prefixPattern string, limit, offset int) (tags []string, total int, err error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	tList, err := r.q.ListTenantDagTags(ctx, queries.ListTenantDagTagsParams{
		TenantID:             tid,
		Limit:                toInt32(limit),
		Offset:               toInt32(offset),
		TagNamePattern:       strPtr(pattern),
		TagNamePrefixPattern: strPtr(prefixPattern),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing dag tags: %w", err)
	}
	count, err := r.q.CountTenantDagTags(ctx, queries.CountTenantDagTagsParams{
		TenantID:             tid,
		TagNamePattern:       strPtr(pattern),
		TagNamePrefixPattern: strPtr(prefixPattern),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("counting dag tags: %w", err)
	}
	return tList, int(count), nil
}
