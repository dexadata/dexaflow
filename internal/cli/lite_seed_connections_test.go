package cli

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestConnectionsToSeed covers the decision half of #1103: WHICH declared
// connections Lite takes from the environment. The write itself is the API's
// existing path; what is new — and what needs pinning — is the judgment about
// when taking a secret from ambient environment is acceptable.
func TestConnectionsToSeed(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}

	t.Run("takes a declared connection present in the environment", func(t *testing.T) {
		got, skipped := connectionsToSeed([]string{"my_db"}, nil,
			env("AIRFLOW_CONN_MY_DB", "postgres://u:p@h:5432/app"))
		if len(got) != 1 || got[0].ConnID != "my_db" {
			t.Fatalf("got %+v, want one connection my_db", got)
		}
		if got[0].ConnType != "postgres" || got[0].Host != "h" {
			t.Errorf("parsed wrong: %+v", got[0])
		}
		if len(skipped) != 0 {
			t.Errorf("nothing should be skipped: %v", skipped)
		}
	})

	t.Run("never overwrites a connection the vault already has", func(t *testing.T) {
		// A stale export must not shadow a value somebody set deliberately —
		// and silently replacing a stored secret from ambient environment is
		// the exact shape this feature must not have.
		got, _ := connectionsToSeed([]string{"my_db"}, map[string]bool{"my_db": true},
			env("AIRFLOW_CONN_MY_DB", "postgres://u:p@h:5432/app"))
		if len(got) != 0 {
			t.Errorf("the vault must win; got %+v", got)
		}
	})

	t.Run("ignores environment variables the DAG did not declare", func(t *testing.T) {
		// Seeding whatever AIRFLOW_CONN_* happens to be exported would copy
		// unrelated credentials from the developer's shell into a database.
		got, _ := connectionsToSeed([]string{"my_db"}, nil,
			env("AIRFLOW_CONN_MY_DB", "postgres://u:p@h:5432/app",
				"AIRFLOW_CONN_SOMEONE_ELSES_PROD", "postgres://root:hunter2@prod:5432/x"))
		if len(got) != 1 || got[0].ConnID != "my_db" {
			t.Errorf("only declared connections may be seeded; got %+v", got)
		}
	})

	t.Run("a declared connection absent from the environment is left alone", func(t *testing.T) {
		got, skipped := connectionsToSeed([]string{"my_db", "other"}, nil,
			env("AIRFLOW_CONN_MY_DB", "postgres://u:p@h:5432/app"))
		if len(got) != 1 {
			t.Errorf("got %+v", got)
		}
		if len(skipped) != 0 {
			t.Errorf("absent is not an error, just nothing to do: %v", skipped)
		}
	})

	t.Run("an unusable URI is reported, not stored", func(t *testing.T) {
		// Storing garbage would replace "the connection is missing" — which
		// registration reports clearly — with a connection that exists and
		// fails inside the task.
		got, skipped := connectionsToSeed([]string{"broken"}, nil,
			env("AIRFLOW_CONN_BROKEN", "h:5432/db"))
		if len(got) != 0 {
			t.Errorf("nothing usable should be stored; got %+v", got)
		}
		if len(skipped) != 1 || !strings.Contains(skipped[0], "broken") {
			t.Errorf("the skip must name the connection; got %v", skipped)
		}
	})

	t.Run("the reported skip never carries the URI", func(t *testing.T) {
		// The value is a secret; the reason it was rejected is not.
		_, skipped := connectionsToSeed([]string{"broken"}, nil,
			env("AIRFLOW_CONN_BROKEN", "postgres://user:hunter2@h:5432/db?x"))
		for _, s := range skipped {
			if strings.Contains(s, "hunter2") {
				t.Errorf("a skip message leaked the secret: %q", s)
			}
		}
	})

	t.Run("the id is upper-cased for the lookup, not for storage", func(t *testing.T) {
		got, _ := connectionsToSeed([]string{"Mixed_Case"}, nil,
			env("AIRFLOW_CONN_MIXED_CASE", "postgres://u:p@h:5432/app"))
		if len(got) != 1 || got[0].ConnID != "Mixed_Case" {
			t.Errorf("the stored id must keep the DAG's spelling; got %+v", got)
		}
	})
}

var _ = domain.Connection{}
