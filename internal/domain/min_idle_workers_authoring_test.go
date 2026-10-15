package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func minIdleCfg(n int, staging bool) *LeoflowConfig {
	c := &LeoflowConfig{SchemaVersion: "1.0", DagID: "sales", MinIdleWorkers: n}
	if staging {
		c.Staging = &StagingConfig{Enabled: true}
	}
	c.ApplyDefaults()
	return c
}

// dexaflow.yaml declares min_idle_workers at the top level, the same key
// dag.json carries, so the prewarm on register (ADR 0082) is reachable from
// the authoring file. The schema bounds it to [0, 100]; the server still clamps
// it to execution.max_pool_size.
func TestValidateBoundsMinIdleWorkers(t *testing.T) {
	for _, tc := range []struct {
		n       int
		wantErr bool
	}{
		{0, false}, {1, false}, {100, false}, {-1, true}, {101, true},
	} {
		err := minIdleCfg(tc.n, false).Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("min_idle_workers %d: err = %v, want error %v", tc.n, err, tc.wantErr)
		}
	}
}

// A raw dexaflow.yaml with the key validates against the authoring schema
// itself, not only through the struct, and a non-integer is refused.
func TestAuthoringSchemaAcceptsMinIdleWorkers(t *testing.T) {
	s, err := schemas()
	if err != nil {
		t.Fatalf("compile schemas: %v", err)
	}
	if verr := validateAgainst(s.leoflow, map[string]any{"dag_id": "sales", "min_idle_workers": 2}); verr != nil {
		t.Errorf("authoring schema refused min_idle_workers: 2: %v", verr)
	}
	if verr := validateAgainst(s.leoflow, map[string]any{"dag_id": "sales", "min_idle_workers": "2"}); verr == nil {
		t.Error("authoring schema accepted min_idle_workers: \"2\"")
	}
}

// A staging DAG never gets a warm pool (ADR 0058 D5), so declaring warmth on
// one would be accepted and do nothing: the compile refuses it instead.
func TestValidateRefusesMinIdleWorkersWithStaging(t *testing.T) {
	err := minIdleCfg(2, true).Validate()
	if err == nil || !strings.Contains(err.Error(), "staging") || !strings.Contains(err.Error(), "min_idle_workers") {
		t.Fatalf("min_idle_workers with staging.enabled: err = %v, want a refusal naming both", err)
	}
	if err := minIdleCfg(0, true).Validate(); err != nil {
		t.Errorf("staging without min_idle_workers: %v", err)
	}
}

// A dag.json without the key, as every DAG that declares no warmth compiles,
// reads as 0, which the reconciler treats as not prewarmed.
func TestMinIdleWorkersAbsentFromCompiledArtifactIsZero(t *testing.T) {
	const compiled = `{"schema_version":"1.0","dag_id":"d","dag_version":"v1","image":"i:1",
		"tasks":[{"task_id":"t","type":"python","entrypoint":"d:t"}]}`
	var spec DAGSpec
	if err := json.Unmarshal([]byte(compiled), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if spec.MinIdleWorkers != 0 {
		t.Fatalf("MinIdleWorkers = %d, want 0", spec.MinIdleWorkers)
	}
}
