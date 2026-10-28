// Command uuid_keys is Load Experiment 9 (#525): what a random UUIDv4 primary
// key costs the hot tables against a time-ordered UUIDv7, at insert time.
//
// It creates two scratch tables of the width of a task instance row, one keyed
// by a random UUIDv4 (what gen_random_uuid(), the default of every table
// today, returns) and one by a UUIDv7, both generated in the client, and fills
// them in turns, --batch rows per transaction through COPY, so both see the
// same checkpoints and the same cache pressure. For each key it reports the
// insert rate overall and over the last tenth of the rows (once the primary
// key index outgrows the cache), the WAL written, the size of the primary key
// index and of the table, and the rate of point lookups by key. The tables are
// dropped at the end.
//
// Usage:
//
//	DATABASE_URL='postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable' \
//	  go run ./test/load/uuid_keys [--rows 2000000] [--batch 1000]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "uuid_keys: %v\n", err)
		os.Exit(1)
	}
}

// keyKind is one way of keying the scratch table.
type keyKind struct {
	name  string
	table string
	// newID returns the id of the next row. Both kinds are generated in the
	// client, so the two runs send the same bytes and differ only in key order.
	newID func() (any, error)
}

// result is what one key kind cost.
type result struct {
	total, tail     time.Duration
	tailRows        int
	walBytes        int64
	indexBytes      int64
	tableBytes      int64
	rows            int
	lookupPerSecond float64
}

func run() error {
	dbURL := flag.String("db", os.Getenv("DATABASE_URL"), "Postgres URL (falls back to $DATABASE_URL)")
	rows := flag.Int("rows", 2_000_000, "rows inserted per key kind")
	batch := flag.Int("batch", 1000, "rows per transaction")
	lookups := flag.Int("lookups", 20000, "point lookups by primary key timed per key kind")
	flag.Parse()
	if *dbURL == "" {
		return errors.New("set --db or $DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	kinds := []keyKind{
		{name: "UUIDv4", table: "load_uuid_v4", newID: func() (any, error) { return uuid.New(), nil }},
		{name: "UUIDv7", table: "load_uuid_v7", newID: func() (any, error) {
			return uuid.NewV7()
		}},
	}
	for _, k := range kinds {
		if _, err = pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %[1]s;
CREATE TABLE %[1]s (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dag_run_id uuid NOT NULL,
  task_id text NOT NULL,
  state text NOT NULL,
  try_number int NOT NULL,
  operator text NOT NULL,
  started_at timestamptz,
  ended_at timestamptz,
  hostname text
)`, k.table)); err != nil {
			return fmt.Errorf("creating %s: %w", k.table, err)
		}
	}
	defer func() {
		for _, k := range kinds {
			_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+k.table) //nolint:errcheck // best effort cleanup
		}
	}()

	res := make([]result, len(kinds))
	tailFrom := *rows - *rows/10
	for done := 0; done < *rows; done += *batch {
		n := min(*batch, *rows-done)
		for i, k := range kinds {
			d, wal, batchErr := insertBatch(ctx, pool, k, n)
			if batchErr != nil {
				return fmt.Errorf("%s: %w", k.table, batchErr)
			}
			res[i].total += d
			res[i].walBytes += wal
			res[i].rows += n
			if done+n > tailFrom {
				res[i].tail += d
				res[i].tailRows += n
			}
		}
	}
	for i, k := range kinds {
		err = pool.QueryRow(ctx, `SELECT pg_relation_size($1::regclass), pg_relation_size($2::regclass)`,
			k.table+"_pkey", k.table).Scan(&res[i].indexBytes, &res[i].tableBytes)
		if err != nil {
			return err
		}
		if res[i].lookupPerSecond, err = timeLookups(ctx, pool, k.table, *lookups); err != nil {
			return err
		}
	}

	fmt.Printf("| Key | Rows | Insert rows/s | Last tenth rows/s | WAL per row | PK index | Table | PK lookups/s |\n")
	fmt.Printf("|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for i, k := range kinds {
		r := res[i]
		fmt.Printf("| %s | %d | %.0f | %.0f | %d B | %.0f MiB | %.0f MiB | %.0f |\n", k.name, r.rows,
			float64(r.rows)/r.total.Seconds(), float64(r.tailRows)/r.tail.Seconds(),
			r.walBytes/int64(r.rows), float64(r.indexBytes)/(1<<20), float64(r.tableBytes)/(1<<20), r.lookupPerSecond)
	}
	return nil
}

// insertBatch inserts n rows into k's table in one transaction through COPY,
// and returns how long it took and the WAL it wrote.
func insertBatch(ctx context.Context, pool *pgxpool.Pool, k keyKind, n int) (time.Duration, int64, error) {
	now := time.Now()
	run := uuid.New()
	data := make([][]any, n)
	for j := range data {
		id, err := k.newID()
		if err != nil {
			return 0, 0, err
		}
		data[j] = []any{id, run, fmt.Sprintf("task_%04d", j), "success", 1, "PythonOperator", now, now, "worker-0"}
	}
	cols := []string{"id", "dag_run_id", "task_id", "state", "try_number", "operator", "started_at", "ended_at", "hostname"}
	var before string
	if err := pool.QueryRow(ctx, `SELECT pg_current_wal_insert_lsn()::text`).Scan(&before); err != nil {
		return 0, 0, err
	}
	start := time.Now()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{k.table}, cols, pgx.CopyFromRows(data)); err != nil {
		return 0, 0, err
	}
	err = tx.Commit(ctx)
	if err != nil {
		return 0, 0, err
	}
	d := time.Since(start)
	var wal int64
	err = pool.QueryRow(ctx, `SELECT pg_wal_lsn_diff(pg_current_wal_insert_lsn(), $1::pg_lsn)::bigint`, before).Scan(&wal)
	if err != nil {
		return 0, 0, err
	}
	return d, wal, nil
}

// timeLookups reads n random rows of table by primary key, one query each, and
// returns the lookups per second.
func timeLookups(ctx context.Context, pool *pgxpool.Pool, table string, n int) (float64, error) {
	rows, err := pool.Query(ctx, fmt.Sprintf(`SELECT id FROM %s ORDER BY random() LIMIT $1`, table), n)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, errors.New("no rows sampled")
	}
	q := fmt.Sprintf(`SELECT state FROM %s WHERE id = $1`, table)
	start := time.Now()
	for _, id := range ids {
		var state string
		err = pool.QueryRow(ctx, q, id).Scan(&state)
		if err != nil {
			return 0, err
		}
	}
	return float64(len(ids)) / time.Since(start).Seconds(), nil
}
