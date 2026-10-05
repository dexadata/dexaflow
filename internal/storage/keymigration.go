package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/secrets"
)

// EncryptedColumn names one column that holds ciphertext sealed with the
// secret key (ADR 0019), with the columns that identify its row.
type EncryptedColumn struct {
	// Table is the table holding the column.
	Table string
	// Column is the ciphertext column.
	Column string
	// IDColumn is the row's primary key, used to rewrite exactly one row.
	IDColumn string
	// LabelColumn is a human-readable name of the row (a conn_id), for messages.
	LabelColumn string
}

// String renders the column as table.column.
func (c EncryptedColumn) String() string { return c.Table + "." + c.Column }

// encryptedColumns is the registry of every column sealed with the secret key.
//
// A key migration may drop a predecessor key only once nothing needs it, and
// "nothing" is exactly the set of columns listed here. A column that is
// encrypted but missing from this list would be left under a key the migration
// then deletes, so TestEveryEncryptedColumnIsInTheRegistry fails when a
// repository write path seals a column that is not here (ADR 0065 section 7).
// Variables join this list when they are encrypted at rest (#507).
var encryptedColumns = []EncryptedColumn{
	{Table: "connections", Column: "password", IDColumn: "id", LabelColumn: "conn_id"},
	{Table: "connections", Column: "extra", IDColumn: "id", LabelColumn: "conn_id"},
}

// EncryptedColumns returns a copy of the encrypted-column registry.
func EncryptedColumns() []EncryptedColumn {
	return append([]EncryptedColumn(nil), encryptedColumns...)
}

// SecretValue is one non-empty encrypted column of one row.
type SecretValue struct {
	// Column is the registry entry the value belongs to.
	Column EncryptedColumn
	// RowID is the row's primary key, as text.
	RowID string
	// Label names the row for an operator (the conn_id).
	Label string
	// Ciphertext is the stored value.
	Ciphertext string
}

// RowQuerier runs a query. *pgx.Conn, pgx.Tx and *pgxpool.Pool satisfy it.
type RowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ReadSecretValues returns every non-empty value of every registered encrypted
// column, across every tenant. A NULL or empty column is not a secret and is
// skipped. A registered table that does not exist yet (a datastore whose schema
// predates it) holds no values.
func ReadSecretValues(ctx context.Context, q RowQuerier) ([]SecretValue, error) {
	var out []SecretValue
	for _, c := range encryptedColumns {
		vals, err := readColumn(ctx, q, c)
		if err != nil {
			return nil, err
		}
		out = append(out, vals...)
	}
	return out, nil
}

func readColumn(ctx context.Context, q RowQuerier, c EncryptedColumn) ([]SecretValue, error) {
	exists, err := tableExists(ctx, q, c.Table)
	if err != nil || !exists {
		return nil, err
	}
	sql := fmt.Sprintf("SELECT %s::text, %s::text, %s FROM %s WHERE %s IS NOT NULL AND %s <> '' ORDER BY %s::text",
		ident(c.IDColumn), ident(c.LabelColumn), ident(c.Column), ident(c.Table),
		ident(c.Column), ident(c.Column), ident(c.IDColumn))
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", c, err)
	}
	defer rows.Close()
	var out []SecretValue
	for rows.Next() {
		v := SecretValue{Column: c}
		if serr := rows.Scan(&v.RowID, &v.Label, &v.Ciphertext); serr != nil {
			return nil, fmt.Errorf("reading %s: %w", c, serr)
		}
		out = append(out, v)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("reading %s: %w", c, rerr)
	}
	return out, nil
}

func tableExists(ctx context.Context, q RowQuerier, table string) (bool, error) {
	rows, err := q.Query(ctx, "SELECT to_regclass($1) IS NOT NULL", table)
	if err != nil {
		return false, fmt.Errorf("looking up table %s: %w", table, err)
	}
	defer rows.Close()
	var exists bool
	if rows.Next() {
		if serr := rows.Scan(&exists); serr != nil {
			return false, serr
		}
	}
	return exists, rows.Err()
}

func ident(s string) string { return pgx.Identifier{s}.Sanitize() }

// SecretValuesNotUnder returns every non-empty encrypted value that c ALONE does
// not open. It is the post-commit re-check of a key migration (ADR 0065 step
// 5): a predecessor may leave the config only when this is empty.
func SecretValuesNotUnder(ctx context.Context, q RowQuerier, c secrets.Cipher) ([]SecretValue, error) {
	vals, err := ReadSecretValues(ctx, q)
	if err != nil {
		return nil, err
	}
	var left []SecretValue
	for _, v := range vals {
		if _, derr := c.Decrypt(v.Ciphertext); derr != nil {
			left = append(left, v)
		}
	}
	return left, nil
}

// KeyMigrationResult counts what one migration transaction did.
type KeyMigrationResult struct {
	// Migrated is the number of values rewritten under the encrypting key.
	Migrated int
	// Skipped is the number of rewrites the optimistic guard matched to zero
	// rows. Under the table lock it cannot happen, so it fails the pass.
	Skipped int
	// Unreadable is the number of values no given key opens.
	Unreadable int
	// Verified is the number of values that opened under the encrypting key
	// alone, with the plaintext read at the start, before COMMIT.
	Verified int
}

// Clean reports whether the pass left nothing behind: no value skipped and none
// unreadable. Anything else is an incomplete pass, never "nothing happened".
func (r KeyMigrationResult) Clean() bool { return r.Skipped == 0 && r.Unreadable == 0 }

// KeyMigrationHooks are named step boundaries inside the migration
// transaction. Production leaves them nil; the crash-injection tests stop the
// process at each one (ADR 0065 section 9).
type KeyMigrationHooks struct {
	// AfterFirstUpdate runs once, after the first UPDATE.
	AfterFirstUpdate func()
	// AfterUpdates runs after every UPDATE, before the in-transaction check.
	AfterUpdates func()
	// AfterVerify runs after the in-transaction check, before COMMIT.
	AfterVerify func()
}

func runHook(f func()) {
	if f != nil {
		f()
	}
}

// UnreadableSecretsError lists values that no given key opens. Their
// ciphertext is the only copy of a credential, so the pass that found them
// changes nothing.
type UnreadableSecretsError struct {
	// Values are the columns no key opened.
	Values []SecretValue
}

func (e *UnreadableSecretsError) Error() string {
	names := make([]string, 0, len(e.Values))
	for _, v := range e.Values {
		names = append(names, fmt.Sprintf("%s of %q", v.Column.Column, v.Label))
	}
	return fmt.Sprintf("%d stored secret(s) open under no recorded key: %s", len(e.Values), strings.Join(names, ", "))
}

// ErrKeyMigrationGuard reports that an UPDATE of the migration matched no row,
// which the table lock makes impossible, so an assumption is broken.
var ErrKeyMigrationGuard = errors.New("a row changed under the key migration's table lock")

// TxBeginner opens a transaction. *pgx.Conn and *pgxpool.Pool satisfy it.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// MigrateSecretValues moves every encrypted value that a predecessor opens onto
// enc, in ONE transaction that commits only after a clean, verified pass (ADR
// 0065 step 4).
//
// Under SHARE ROW EXCLUSIVE locks on every registered table, it reads every
// value, opens each with enc and then the predecessors, rewrites the ones a
// predecessor opened, then re-reads every value and requires each to open
// under enc ALONE with the plaintext read at the start. Any of: a value no key
// opens, an UPDATE that matched no row, a value that fails the check, rolls
// the transaction back and returns an error with the counts so far. Nothing is
// ever committed from a partial pass.
func MigrateSecretValues(ctx context.Context, db TxBeginner, enc secrets.Cipher, predecessors []secrets.Cipher, hooks KeyMigrationHooks) (KeyMigrationResult, error) {
	var res KeyMigrationResult
	tx, err := db.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("beginning the key migration: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }() //nolint:errcheck,contextcheck // no-op after COMMIT; a dead session rolls back by itself

	if lerr := lockEncryptedTables(ctx, tx); lerr != nil {
		return res, lerr
	}
	plain, moves, err := openAll(ctx, tx, enc, predecessors, &res)
	if err != nil {
		return res, err
	}
	for i, v := range moves {
		if merr := moveValue(ctx, tx, enc, v, plain[valueKey(v)], &res); merr != nil {
			return res, merr
		}
		if i == 0 {
			runHook(hooks.AfterFirstUpdate)
		}
	}
	runHook(hooks.AfterUpdates)
	if verr := verifyUnder(ctx, tx, enc, plain, &res); verr != nil {
		return res, verr
	}
	runHook(hooks.AfterVerify)
	if cerr := tx.Commit(ctx); cerr != nil {
		return res, fmt.Errorf("committing the key migration: %w", cerr)
	}
	return res, nil
}

func lockEncryptedTables(ctx context.Context, tx pgx.Tx) error {
	seen := map[string]bool{}
	for _, c := range encryptedColumns {
		if seen[c.Table] {
			continue
		}
		seen[c.Table] = true
		exists, err := tableExists(ctx, tx, c.Table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if _, lerr := tx.Exec(ctx, "LOCK TABLE "+ident(c.Table)+" IN SHARE ROW EXCLUSIVE MODE"); lerr != nil {
			return fmt.Errorf("locking %s: %w", c.Table, lerr)
		}
	}
	return nil
}

func valueKey(v SecretValue) string { return v.Column.String() + "\x00" + v.RowID }

// openAll opens every value with enc then the predecessors. It returns each
// plaintext and the values a predecessor opened, or an UnreadableSecretsError.
func openAll(ctx context.Context, tx pgx.Tx, enc secrets.Cipher, predecessors []secrets.Cipher, res *KeyMigrationResult) (map[string]string, []SecretValue, error) {
	vals, err := ReadSecretValues(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	all := append([]secrets.Cipher{enc}, predecessors...)
	plain := make(map[string]string, len(vals))
	var moves, unreadable []SecretValue
	for _, v := range vals {
		p, idx := secrets.OpenWith(all, v.Ciphertext)
		switch {
		case idx < 0:
			unreadable = append(unreadable, v)
		case idx > 0:
			moves = append(moves, v)
		}
		plain[valueKey(v)] = p
	}
	if len(unreadable) > 0 {
		res.Unreadable = len(unreadable)
		return nil, nil, &UnreadableSecretsError{Values: unreadable}
	}
	return plain, moves, nil
}

// moveValue rewrites one value under enc, guarded on the ciphertext it read.
func moveValue(ctx context.Context, tx pgx.Tx, enc secrets.Cipher, v SecretValue, plain string, res *KeyMigrationResult) error {
	sealed, err := enc.Encrypt(plain)
	if err != nil {
		return fmt.Errorf("re-encrypting %s of %q: %w", v.Column.Column, v.Label, err)
	}
	c := v.Column
	sql := fmt.Sprintf("UPDATE %s SET %s = $1 WHERE %s::text = $2 AND %s = $3",
		ident(c.Table), ident(c.Column), ident(c.IDColumn), ident(c.Column))
	tag, err := tx.Exec(ctx, sql, sealed, v.RowID, v.Ciphertext)
	if err != nil {
		return fmt.Errorf("rewriting %s of %q: %w", c.Column, v.Label, err)
	}
	if tag.RowsAffected() != 1 {
		res.Skipped++
		return fmt.Errorf("rewriting %s of %q: %w", c.Column, v.Label, ErrKeyMigrationGuard)
	}
	res.Migrated++
	return nil
}

// verifyUnder re-reads every value inside the transaction and requires the
// same set of values, each opening under enc alone with its original
// plaintext.
func verifyUnder(ctx context.Context, tx pgx.Tx, enc secrets.Cipher, plain map[string]string, res *KeyMigrationResult) error {
	vals, err := ReadSecretValues(ctx, tx)
	if err != nil {
		return err
	}
	if len(vals) != len(plain) {
		return fmt.Errorf("verification found %d encrypted values, the pass read %d", len(vals), len(plain))
	}
	var bad []string
	for _, v := range vals {
		want, ok := plain[valueKey(v)]
		got, derr := enc.Decrypt(v.Ciphertext)
		if !ok || derr != nil || got != want {
			bad = append(bad, fmt.Sprintf("%s of %q", v.Column.Column, v.Label))
			continue
		}
		res.Verified++
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("verification under the new key alone failed for %s", strings.Join(bad, ", "))
	}
	return nil
}
