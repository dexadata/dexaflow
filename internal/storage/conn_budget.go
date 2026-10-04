package storage

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// sqlstateTooManyConnections is Postgres' too_many_connections. The server
// answers it both as "sorry, too many clients already" and, once only the
// reserved slots are left, as "remaining connection slots are reserved for
// ...". Either way the server is out of connection slots.
const sqlstateTooManyConnections = "53300"

// perPodExtraConns is what every control-plane process opens on top of the
// main pool: healthPoolConns for the readiness checks, plus one for the
// scheduler's leader lock session (NewLeaderPool).
const perPodExtraConns = healthPoolConns + 1

// explainConnBudget turns a too_many_connections failure into an error that
// names the setting behind it (#1088).
//
// Postgres reports the symptom ("remaining connection slots are reserved"),
// which reads as a database problem: the operator goes looking for whatever is
// holding connections, or tries to raise max_connections on a tier that caps
// it. The usual cause is the pool size times the replica count, and nothing in
// the server's message says so. maxConns is the main pool's effective size.
// The original error stays wrapped, so errors.Is and errors.As still reach the
// *pgconn.PgError. Every other error is returned unchanged.
func explainConnBudget(err error, maxConns int32) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != sqlstateTooManyConnections {
		return err
	}
	return fmt.Errorf("%w: "+
		"Postgres has no free connection slots (SQLSTATE 53300). Each control-plane replica "+
		"opens up to database.max_open_conns=%d connections (Helm database.maxOpenConns, "+
		"env DEXAFLOW_DATABASE_MAX_OPEN_CONNS) + %d (health checks and the scheduler leader lock), "+
		"and the total across every replica, rolling-update surge pod and the migration Job "+
		"must fit the server's max_connections minus its reserved slots. Lower "+
		"database.max_open_conns, run fewer replicas, or raise max_connections (see #1088)",
		err, maxConns, perPodExtraConns)
}
