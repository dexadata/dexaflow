package cli

import (
	"strings"
	"testing"
)

// Lite registers a DAG active (#1705): the server pauses new DAGs by default,
// as Airflow does, but a DAG someone runs locally, and every hot reload of it,
// must keep running without an unpause first.
func TestLiteServerRegistersNewDagsActive(t *testing.T) {
	t.Parallel()
	for _, admin := range []string{"", "$2a$12$hash"} {
		env := sharedServerEnv(liteEnvParams{host: "127.0.0.1", port: 8088, adminHash: admin, adminEmail: "a@x"})
		if !strings.Contains(strings.Join(env, "\n"), "\nLEOFLOW_DAGS_ARE_PAUSED_AT_CREATION=false\n") {
			t.Errorf("admin %q: Lite server env must set LEOFLOW_DAGS_ARE_PAUSED_AT_CREATION=false", admin)
		}
	}
}
