package main

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/storage"
)

// pingOnlyChecker stands in for the decorator this test exists to catch — a
// metrics wrapper, a circuit breaker, a tenant router. It satisfies
// api.HealthChecker, so healthChecks would still compile with it in the map.
type pingOnlyChecker struct{ api.HealthChecker }

// TestHealthChecksPostgresCarriesTheSchemaAssertion pins #1023 where it can
// actually break, which is not where a compile-time interface pin can see it.
//
// The readiness handler discovers the schema assertion with a runtime type
// assertion on the VALUE in the checks map. `var _ api.SchemaChecker =
// (*storage.Postgres)(nil)` pins the TYPE, so it catches a rename on either
// side and nothing else: wrap the value in any decorator that implements only
// Ping and the pin still compiles, every test stays green, and /readyz silently
// returns to reporting ready over an empty database — the exact regression
// #1023 was. The value is what the handler sees, so the value is what to assert.
func TestHealthChecksPostgresCarriesTheSchemaAssertion(t *testing.T) {
	// A nil Pool is fine: nothing here reaches the database, and constructing
	// the value is the whole point — it is the map entry that must keep the
	// capability, not the declared type of the field it came from.
	pg := &storage.Postgres{}

	checks := healthChecks(pg, nil)

	entry, ok := checks["postgres"]
	if !ok {
		t.Fatalf(`no "postgres" entry in the readiness checks: %v`, keys(checks))
	}
	if _, ok := entry.(api.SchemaChecker); !ok {
		t.Fatalf("the postgres readiness check (%T) does not implement api.SchemaChecker, "+
			"so /readyz and /api/v2/monitor/health silently fall back to Ping-only and report "+
			"ready over a database with no schema (#1023)", entry)
	}
}

// TestHealthChecksDecoratorLosingSchemaIsCaught is the negative control: it
// proves the assertion above can fail, by building the map shape a Ping-only
// decorator would produce.
func TestHealthChecksDecoratorLosingSchemaIsCaught(t *testing.T) {
	wrapped := api.HealthChecker(pingOnlyChecker{&storage.Postgres{}})
	if _, ok := wrapped.(api.SchemaChecker); ok {
		t.Fatal("pingOnlyChecker must NOT satisfy api.SchemaChecker, or the guard above proves nothing")
	}
}

// TestHealthChecksOmitsRedisWhenAbsent keeps the map's other contract honest:
// Redis is schema-less and is registered only when it is the active datastore.
func TestHealthChecksOmitsRedisWhenAbsent(t *testing.T) {
	if _, ok := healthChecks(&storage.Postgres{}, nil)["redis"]; ok {
		t.Error("redis registered as a readiness dependency in the embedded edition")
	}
	redis := api.HealthChecker(pingOnlyChecker{})
	checks := healthChecks(&storage.Postgres{}, redis)
	if _, ok := checks["redis"]; !ok {
		t.Error("redis not registered when it is the active datastore")
	}
	if _, ok := checks["redis"].(api.SchemaChecker); ok {
		t.Error("a schema-less dependency must keep the Ping-only contract")
	}
}

func keys(m map[string]api.HealthChecker) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
