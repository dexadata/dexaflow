package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// cappedConn is a white-box double for the run-creation transaction of a tenant
// with a daily run cap, in the shape of fakeConn (users_test.go), whose fakeRow
// and fakeBeginner it reuses: GetTenantLimits answers max_runs_per_day = 2,
// DagRunExistsByDagID answers runExists, every Exec is recorded,
// ReserveTenantDailyRun reports reserveRows, and Commit and Rollback are
// recorded.
type cappedConn struct {
	pgx.Tx      // embedded so the fake satisfies pgx.Tx; only the methods below are called
	reserveRows int64
	runExists   bool
	execs       []string
	committed   bool
	rolledBack  bool
}

func (c *cappedConn) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "min_schedule_interval_seconds"): // GetTenantLimits
		return fakeRow{func(d ...any) error {
			*(d[0].(*int32)), *(d[1].(*int32)), *(d[2].(*int32)) = 0, 2, 0
			return nil
		}}
	case strings.Contains(sql, "run_exists"): // DagRunExistsByDagID
		return fakeRow{func(d ...any) error {
			*(d[0].(*bool)) = c.runExists
			return nil
		}}
	}
	return fakeRow{func(...any) error { return errors.New("unexpected QueryRow: " + sql) }}
}

func (c *cappedConn) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	c.execs = append(c.execs, sql)
	if strings.Contains(sql, "runs_day_count") { // ReserveTenantDailyRun
		return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", c.reserveRows)), nil
	}
	return pgconn.CommandTag{}, nil
}

func (c *cappedConn) Commit(context.Context) error   { c.committed = true; return nil }
func (c *cappedConn) Rollback(context.Context) error { c.rolledBack = true; return nil }

// createCappedRun runs createRunWithinLimits for a tenant whose daily cap
// answers conn.reserveRows.
func createCappedRun(conn *cappedConn, run runCreation) error {
	if run.exists == nil {
		run.exists = func(q *queries.Queries) (bool, error) {
			return q.DagRunExistsByDagID(context.Background(), queries.DagRunExistsByDagIDParams{})
		}
	}
	return createRunWithinLimits(context.Background(), queries.New(conn), fakeBeginner{tx: conn}, validUUID(), run)
}

// TestDailyRunCapRefusalWritesNothing: when the conditional UPDATE takes no
// row, the run is refused before its INSERT is attempted, so the retries of a
// capped tenant never leave a rolled-back row behind.
func TestDailyRunCapRefusalWritesNothing(t *testing.T) {
	conn := &cappedConn{reserveRows: 0}
	inserted := false

	err := createCappedRun(conn, runCreation{insert: func(*queries.Queries) (bool, error) { inserted = true; return true, nil }})

	if !errors.Is(err, domain.ErrLimitExceeded) || !strings.Contains(err.Error(), "max_runs_per_day of 2") {
		t.Fatalf("err = %v, want ErrLimitExceeded naming the limit", err)
	}
	if inserted {
		t.Error("the run was inserted although the cap refused it")
	}
	if conn.committed || !conn.rolledBack {
		t.Errorf("committed = %v, rolled back = %v; want a rollback only", conn.committed, conn.rolledBack)
	}
}

// TestDailyRunCapKeepsTheAnswerOfARunThatExists: a run that already exists is
// not refused at the cap; it gets the answer its insert gives (a no-op for a
// scheduled slot, a conflict for a manual run id), and still nothing is
// written.
func TestDailyRunCapKeepsTheAnswerOfARunThatExists(t *testing.T) {
	conflict := errors.New("duplicate run id")
	for name, existsErr := range map[string]error{"scheduled slot": nil, "manual run id": conflict} {
		t.Run(name, func(t *testing.T) {
			conn := &cappedConn{reserveRows: 0, runExists: true}
			inserted := false

			err := createCappedRun(conn, runCreation{
				insert:    func(*queries.Queries) (bool, error) { inserted = true; return true, nil },
				existsErr: existsErr,
			})

			if !errors.Is(err, existsErr) {
				t.Errorf("err = %v, want %v", err, existsErr)
			}
			if inserted {
				t.Error("the run was inserted although it exists")
			}
			if conn.committed || !conn.rolledBack {
				t.Errorf("committed = %v, rolled back = %v; want a rollback only", conn.committed, conn.rolledBack)
			}
		})
	}
}

// TestDailyRunCapChargeFollowsTheRun: the charge is taken before the INSERT and
// shares its fate: committed with a created run, rolled back when the insert
// creates nothing (a scheduled slot that already exists) or fails.
func TestDailyRunCapChargeFollowsTheRun(t *testing.T) {
	insertErr := errors.New("insert failed")
	cases := map[string]struct {
		created    bool
		err        error
		wantCommit bool
	}{
		"created":       {created: true, wantCommit: true},
		"existing slot": {created: false},
		"insert error":  {created: false, err: insertErr},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			conn := &cappedConn{reserveRows: 1}
			chargedFirst := false

			err := createCappedRun(conn, runCreation{insert: func(*queries.Queries) (bool, error) {
				chargedFirst = len(conn.execs) == 1 && strings.Contains(conn.execs[0], "runs_day_count")
				return tc.created, tc.err
			}})

			if !errors.Is(err, tc.err) {
				t.Errorf("err = %v, want %v", err, tc.err)
			}
			if !chargedFirst {
				t.Errorf("statements before the insert = %q, want the daily cap charge alone", conn.execs)
			}
			if conn.committed != tc.wantCommit {
				t.Errorf("committed = %v, want %v", conn.committed, tc.wantCommit)
			}
			if !tc.wantCommit && !conn.rolledBack {
				t.Error("the transaction was not rolled back, so the charge stayed")
			}
		})
	}
}

// TestApplyTenantLimitsRefusesAValueOutsideTheColumn: a limit below 0 or above
// 2147483647 is ErrValidation and reaches no query, instead of being clamped
// to the column; a limit inside the column is written.
func TestApplyTenantLimitsRefusesAValueOutsideTheColumn(t *testing.T) {
	tooBig := math.MaxInt32
	tooBig++
	for _, n := range []int{-1, tooBig} {
		conn := &cappedConn{}

		err := applyTenantLimits(context.Background(), queries.New(conn), validUUID(), domain.TenantLimitsUpdate{MaxRunsPerDay: &n})

		if !errors.Is(err, domain.ErrValidation) {
			t.Errorf("limit %d: err = %v, want ErrValidation", n, err)
		}
		if len(conn.execs) != 0 {
			t.Errorf("limit %d: statements run = %q, want none", n, conn.execs)
		}
	}
	zero, conn := 0, &cappedConn{}

	err := applyTenantLimits(context.Background(), queries.New(conn), validUUID(), domain.TenantLimitsUpdate{MaxDags: &zero})

	if err != nil || len(conn.execs) != 1 || !strings.Contains(conn.execs[0], "max_dags = COALESCE") {
		t.Errorf("clearing max_dags: err = %v, statements = %q; want one UPDATE of the limits", err, conn.execs)
	}
}
