package domain

import "testing"

// TestValidateRefusesSpecIntegersTheDatabaseCannotCast pins the upper bounds of
// the spec fields that SQL reads back with ::integer: max_active_tasks in the
// DAG list and details queries, min_idle_workers in the cross-tenant prewarm
// read. A larger value registered as a dag.json would make those reads fail
// with "out of range for type integer", for the whole tenant's DAG list or, for
// min_idle_workers, for the warm pool reconcile of every tenant.
func TestValidateRefusesSpecIntegersTheDatabaseCannotCast(t *testing.T) {
	base := func() DAGSpec {
		return DAGSpec{
			SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img:v1",
			Tasks: []TaskSpec{{TaskID: "a", Type: TaskTypePython, Entrypoint: "dag:a"}},
		}
	}
	cases := []struct {
		name string
		set  func(*DAGSpec)
		ok   bool
	}{
		{"max_active_tasks at the int4 bound", func(s *DAGSpec) { s.MaxActiveTasks = 2147483647 }, true},
		{"max_active_tasks past the int4 bound", func(s *DAGSpec) { s.MaxActiveTasks = 2147483648 }, false},
		{"min_idle_workers at the dexaflow.yaml bound", func(s *DAGSpec) { s.MinIdleWorkers = 100 }, true},
		{"min_idle_workers past the dexaflow.yaml bound", func(s *DAGSpec) { s.MinIdleWorkers = 101 }, false},
		{"min_idle_workers past the int4 bound", func(s *DAGSpec) { s.MinIdleWorkers = 3000000000 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := base()
			tc.set(&spec)
			err := spec.Validate()
			if tc.ok && err != nil {
				t.Errorf("want it registered, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("want it refused at registration")
			}
		})
	}
}
