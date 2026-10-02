package dbt

import (
	"slices"
	"testing"
)

// TestCompileCarriesDeclaredSecrets is the regression for #997. Compile builds
// its DAGSpec from an explicit field list, and that list had no Connections or
// Variables — so a dexaflow.yaml declaring them produced a dag.json without them
// on the dbt-only path, while the dag.py path emitted both.
//
// Under ADR 0055 secret scoping the consequence is not cosmetic: the task pod
// receives NOTHING, because what it may see is derived from what the spec
// declares. The DAG that asked for a warehouse connection runs without it, and
// the failure appears inside the task.
func TestCompileCarriesDeclaredSecrets(t *testing.T) {
	manifest := []byte(`{"nodes":{"model.shop.orders":{"resource_type":"model","name":"orders","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["shop","orders"]}}}`)

	t.Run("declared connections and variables reach the spec", func(t *testing.T) {
		spec, err := Compile(manifest, Meta{
			DagID:       "shop",
			Connections: []string{"warehouse", "reporting"},
			Variables:   []string{"env"},
		})
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if len(spec.Connections) != 2 || spec.Connections[0] != "warehouse" {
			t.Errorf("spec.Connections = %v, want the declared pair", spec.Connections)
		}
		if len(spec.Variables) != 1 || spec.Variables[0] != "env" {
			t.Errorf("spec.Variables = %v, want [env]", spec.Variables)
		}
	})

	t.Run("nothing declared leaves both absent", func(t *testing.T) {
		// The fields are omitempty, and an empty slice would serialize
		// differently from an absent one — a needless diff in every dag.json.
		spec, err := Compile(manifest, Meta{DagID: "shop"})
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		if spec.Connections != nil || spec.Variables != nil {
			t.Errorf("want both nil when nothing is declared; got %v / %v", spec.Connections, spec.Variables)
		}
	})

	t.Run("the caller's slice is not aliased", func(t *testing.T) {
		// Compile's result outlives the Meta it was built from; sharing the
		// backing array lets the caller mutate the spec afterwards.
		//
		// The mutation has to land IN RANGE. Appending past the length writes a
		// slot the spec's own header does not cover, so the assertion holds
		// whether Compile copies or aliases — the first version of this test
		// did exactly that and stayed green with the copy reverted.
		conns := []string{"warehouse"}
		vars := []string{"env"}
		spec, err := Compile(manifest, Meta{DagID: "shop", Connections: conns, Variables: vars})
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		conns[0] = "sneaked"
		vars[0] = "sneaked"
		if len(spec.Connections) != 1 || spec.Connections[0] != "warehouse" {
			t.Errorf("the spec changed under the caller: %v", spec.Connections)
		}
		if len(spec.Variables) != 1 || spec.Variables[0] != "env" {
			t.Errorf("the spec changed under the caller: %v", spec.Variables)
		}
	})
}

// TestManagedConnectionDoesNotShadowTheDagsOwn: with dbt.connection set, every
// task gets a task-level connection list — and downstream (declaredConnections,
// internal/storage) a non-empty task list is what reaches the pod, the DAG-level
// list never consulted. So stamping the managed connection alone silently drops
// every connection the DAG declared: a pre-hook's warehouse missing under
// enforce scoping or an external secrets backend, failing inside the task.
//
// Carrying the fields into the spec (#997) is not enough on its own; the spec
// field is the half nothing reads when tasks carry their own.
func TestManagedConnectionDoesNotShadowTheDagsOwn(t *testing.T) {
	spec, err := Compile(manifestFixture(t), Meta{
		DagID:       "shop",
		Connection:  "managed_wh",
		Profile:     "shop",
		Connections: []string{"warehouse_pg", "reporting"},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(spec.Tasks) == 0 {
		t.Fatal("no tasks rendered; the assertions below would be vacuous")
	}
	for _, task := range spec.Tasks {
		if !slices.Contains(task.Connections, "managed_wh") {
			t.Errorf("task %s: managed connection missing from %v", task.TaskID, task.Connections)
		}
		for _, want := range []string{"warehouse_pg", "reporting"} {
			if !slices.Contains(task.Connections, want) {
				t.Errorf("task %s: the DAG declared %q and the task list shadows it: %v", task.TaskID, want, task.Connections)
			}
		}
		if n := countOccurrences(task.Connections, "managed_wh"); n != 1 {
			t.Errorf("task %s: managed connection listed %d times: %v", task.TaskID, n, task.Connections)
		}
	}
}

// TestManagedConnectionDedupesTheDagsOwn: a DAG that also declares the id used
// as dbt.connection must not have it listed twice.
func TestManagedConnectionDedupesTheDagsOwn(t *testing.T) {
	spec, err := Compile(manifestFixture(t), Meta{
		DagID:       "shop",
		Connection:  "warehouse_pg",
		Profile:     "shop",
		Connections: []string{"warehouse_pg"},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(spec.Tasks) == 0 {
		t.Fatal("no tasks rendered")
	}
	for _, task := range spec.Tasks {
		if n := countOccurrences(task.Connections, "warehouse_pg"); n != 1 {
			t.Errorf("task %s: %q listed %d times: %v", task.TaskID, "warehouse_pg", n, task.Connections)
		}
	}
}

// manifestFixture is the one-model manifest the secrets tests render from.
func manifestFixture(t *testing.T) []byte {
	t.Helper()
	return []byte(`{"nodes":{"model.shop.orders":{"resource_type":"model","name":"orders","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["shop","orders"]}}}`)
}

func countOccurrences(list []string, want string) int {
	n := 0
	for _, s := range list {
		if s == want {
			n++
		}
	}
	return n
}
