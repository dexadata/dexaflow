package storage

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// reservedSlotsError is what a small managed Postgres (Cloud SQL db-f1-micro,
// max_connections 25) answers once every non-reserved slot is taken: the exact
// failure from #1088, where two replicas at the chart default asked for more
// connections than the server has.
func reservedSlotsError() *pgconn.PgError {
	return &pgconn.PgError{
		Severity:            "FATAL",
		SeverityUnlocalized: "FATAL",
		Code:                "53300",
		Message:             `remaining connection slots are reserved for roles with privileges of the "pg_use_reserved_connections" role`,
		File:                "postinit.c",
		Routine:             "InitPostgres",
	}
}

// TestExplainConnBudgetNamesTheSetting: SQLSTATE 53300 at boot used to read as
// a database problem ("remaining connection slots are reserved"), and nothing
// in it pointed at database.max_open_conns times the replica count. The boot
// error must name the setting, its effective value and the per-replica
// arithmetic, so the operator lowers the pool instead of hunting for a leak.
func TestExplainConnBudgetNamesTheSetting(t *testing.T) {
	pgErr := reservedSlotsError()
	boot := fmt.Errorf("postgres unreachable after 30s: %w", pgErr)

	got := explainConnBudget(boot, 20)
	if got == nil {
		t.Fatal("explainConnBudget returned nil for a 53300 error")
	}
	msg := got.Error()
	for _, want := range []string{
		"SQLSTATE 53300",
		"database.max_open_conns",
		"database.maxOpenConns",
		"max_open_conns=20",
		"+ 3",
		"replica",
		"max_connections",
		"postgres unreachable after 30s",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("boot error must mention %q; got:\n%s", want, msg)
		}
	}
	if !errors.Is(got, boot) {
		t.Error("the explained error must wrap the original boot error")
	}
	var unwrapped *pgconn.PgError
	if !errors.As(got, &unwrapped) || unwrapped.Code != "53300" {
		t.Error("the Postgres error must stay reachable with errors.As")
	}
}

// TestExplainConnBudgetCoversTooManyClients: stock Postgres reports the same
// SQLSTATE as "sorry, too many clients already" when no reserved slots are
// configured. Same cause, same explanation.
func TestExplainConnBudgetCoversTooManyClients(t *testing.T) {
	pgErr := &pgconn.PgError{Severity: "FATAL", Code: "53300", Message: "sorry, too many clients already"}
	got := explainConnBudget(pgErr, 8)
	if got == nil || !strings.Contains(got.Error(), "max_open_conns=8") {
		t.Fatalf("too many clients must be explained with the effective pool size; got: %v", got)
	}
}

// TestExplainConnBudgetCoversRoleConnectionLimit: a per-role CONNECTION LIMIT
// (common on managed providers) is reported with the same SQLSTATE. Raising
// max_connections would not help there, so the explanation must name the role
// and database limits as well.
func TestExplainConnBudgetCoversRoleConnectionLimit(t *testing.T) {
	pgErr := &pgconn.PgError{Severity: "FATAL", Code: "53300", Message: `too many connections for role "dexaflow"`}
	got := explainConnBudget(pgErr, 6)
	if got == nil {
		t.Fatal("explainConnBudget returned nil for a 53300 error")
	}
	for _, want := range []string{"max_open_conns=6", "CONNECTION LIMIT", "max_connections"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("role limit error must mention %q; got:\n%s", want, got)
		}
	}
}

// TestExplainConnBudgetLeavesOtherErrorsAlone: a wrong password or an
// unreachable host has nothing to do with the pool size, and saying otherwise
// would send the operator after the wrong knob.
func TestExplainConnBudgetLeavesOtherErrorsAlone(t *testing.T) {
	authErr := fmt.Errorf("postgres unreachable after 30s: %w",
		&pgconn.PgError{Severity: "FATAL", Code: "28P01", Message: "password authentication failed"})
	plain := errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")

	for _, err := range []error{authErr, plain} {
		if got := explainConnBudget(err, 20); got != err { //nolint:errorlint // identity is the contract
			t.Errorf("non-53300 error must be returned unchanged; got: %v", got)
		}
	}
	if got := explainConnBudget(nil, 20); got != nil {
		t.Errorf("nil must stay nil; got: %v", got)
	}
}
