package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

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
		out = append(out, r.listVariable(v.Key, v.Value, v.Description))
	}
	return out, int(total), nil
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
		r.deliverVariable(out, v.Key, v.Value)
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
		// connection still fails, correctly, but every other task keeps working.
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
// nothing without a query, enforce's load-bearing [] case. tenantID is the
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
		r.deliverVariable(out, v.Key, v.Value)
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
// whose token is not in the URL). An absent/empty URL is an error, a
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
	return r.toVariable(v.Key, v.Value, v.Description)
}

// SetVariable creates or updates a variable, always overwriting the value and
// preserving description when empty. It is the full-write convenience the
// storage tests and internal callers use; the tri-state API write path goes
// through SetVariablePatch.
func (r *Repository) SetVariable(ctx context.Context, tenant string, v domain.Variable) error {
	stored, err := r.sealVariable(v.Value)
	if err != nil {
		return err
	}
	return r.withTenantWrite(ctx, tenant, func(tid pgtype.UUID) error {
		if err := r.q.UpsertVariable(ctx, queries.UpsertVariableParams{
			TenantID: tid, Key: v.Key, Value: stored, Description: strPtr(v.Description),
		}); err != nil {
			return fmt.Errorf("upserting variable: %w", err)
		}
		return nil
	})
}

// SetVariablePatch upserts a variable with tri-state semantics on description
// (#887): a nil Description is preserved (NULL param → COALESCE), a non-nil one
// is written ("" clears it). The `value` column is NOT NULL, so value cannot be
// preserved at the SQL layer (a NULL param is rejected on the INSERT arm before
// COALESCE); the API handler therefore resolves an omitted/masked value to the
// stored value before calling here, and Value is expected non-nil (a nil Value
// defensively clears to empty rather than panicking).
func (r *Repository) SetVariablePatch(ctx context.Context, tenant string, p domain.VariablePatch) error {
	value := ""
	if p.Value != nil {
		value = *p.Value
	}
	stored, err := r.sealVariable(value)
	if err != nil {
		return err
	}
	return r.withTenantWrite(ctx, tenant, func(tid pgtype.UUID) error {
		if err := r.q.UpsertVariable(ctx, queries.UpsertVariableParams{
			TenantID: tid, Key: p.Key, Value: stored, Description: p.Description,
		}); err != nil {
			return fmt.Errorf("upserting variable: %w", err)
		}
		return nil
	})
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
// credential in plaintext, ADR 0019).
func (r *Repository) SetConnection(ctx context.Context, tenant string, c domain.Connection) error {
	if r.cipher == nil {
		return secrets.ErrNoKey
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
	return r.withTenantWrite(ctx, tenant, func(tid pgtype.UUID) error {
		return r.q.UpsertConnection(ctx, queries.UpsertConnectionParams{
			TenantID: tid, ConnID: c.ConnID, ConnType: c.ConnType,
			Host: strPtr(c.Host), ConnSchema: strPtr(c.Schema), Login: strPtr(c.Login),
			Password: encPass, Port: port, Extra: encExtra, Description: strPtr(c.Description),
		})
	})
}

// encPatch encrypts a tri-state secret param (#887): a nil pointer preserves the
// stored column (NULL), a non-empty value is encrypted, and an explicit empty
// string clears the column to empty (stored as-is, an empty value is not a
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
// no encryption cipher is configured (never stores a credential in plaintext,
// ADR 0019).
func (r *Repository) SetConnectionPatch(ctx context.Context, tenant string, p domain.ConnectionPatch) error {
	if r.cipher == nil {
		return secrets.ErrNoKey
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
	return r.withTenantWrite(ctx, tenant, func(tid pgtype.UUID) error {
		return r.q.UpsertConnection(ctx, queries.UpsertConnectionParams{
			TenantID: tid, ConnID: p.ConnID, ConnType: p.ConnType,
			Host: p.Host, ConnSchema: p.Schema, Login: p.Login,
			Password: encPass, Port: port, Extra: encExtra, Description: p.Description,
		})
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
	return r.toConnection(domain.Connection{
		ConnID: row.ConnID, ConnType: row.ConnType, Host: strOrEmpty(row.Host),
		Schema: strOrEmpty(row.ConnSchema), Login: strOrEmpty(row.Login),
		Port: int32PtrToInt(row.Port), Description: strOrEmpty(row.Description),
	}, row.Extra)
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
		out = append(out, r.listConnection(domain.Connection{
			ConnID: row.ConnID, ConnType: row.ConnType, Host: strOrEmpty(row.Host),
			Schema: strOrEmpty(row.ConnSchema), Login: strOrEmpty(row.Login),
			Port: int32PtrToInt(row.Port), Description: strOrEmpty(row.Description),
		}, row.Extra))
	}
	return out, int(total), nil
}

// toConnection completes a connection read alone with its decrypted extra. An
// extra no configured key opens is a domain.ErrValueUnreadable error whose
// client message names the connection and the way out, the rule Variables
// follow (toVariable).
func (r *Repository) toConnection(c domain.Connection, extra *string) (domain.Connection, error) {
	plain, err := r.decryptExtra(extra)
	if err != nil {
		return domain.Connection{}, fmt.Errorf("%w: %w", domain.Safef(domain.ErrValueUnreadable,
			"connection %q is encrypted with a secret key this server does not have; add that key back as a later secret key entry, or delete the connection and set it again", c.ConnID), err)
	}
	c.Extra = plain
	return c, nil
}

// listConnection completes a connection for a listing. An extra no configured
// key opens does not fail the listing (#1677): the connection is returned
// marked ExtraUnreadable with no extra, and a warning is logged, the rule
// delivery to task pods follows (SecretConnections).
func (r *Repository) listConnection(c domain.Connection, extra *string) domain.Connection {
	got, err := r.toConnection(c, extra)
	if err != nil {
		slog.Warn("listing connection without its extra: decrypt failed (key rotated or wrong DEXAFLOW_SECRET_KEY?)",
			"conn_id", c.ConnID, "error", err)
		c.Extra = ""
		c.ExtraUnreadable = true
		return c
	}
	return got
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
