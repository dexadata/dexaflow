package storage

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// The agent-facing task spec carries the declared secret set (ADR 0045, ADR
// 0055) so a later increment can scope delivery to it. Resolution is
// inheritance: a task that narrows uses its own declaration, otherwise it
// inherits the DAG-level declaration. This carries data only — nothing here
// filters what secrets the agent receives.
func TestDeclaredSecretResolution(t *testing.T) {
	cases := []struct {
		name     string
		task     domain.TaskSpec
		spec     domain.DAGSpec
		wantVars []string
		wantConn []string
	}{
		{
			name:     "inherits dag level when task declares nothing",
			task:     domain.TaskSpec{},
			spec:     domain.DAGSpec{Variables: []string{"greeting"}, Connections: []string{"warehouse"}},
			wantVars: []string{"greeting"},
			wantConn: []string{"warehouse"},
		},
		{
			name:     "task narrows the dag declaration",
			task:     domain.TaskSpec{Variables: []string{"greeting"}, Connections: []string{"warehouse"}},
			spec:     domain.DAGSpec{Variables: []string{"greeting", "farewell"}, Connections: []string{"warehouse", "lake"}},
			wantVars: []string{"greeting"},
			wantConn: []string{"warehouse"},
		},
		{
			name:     "empty when neither declares",
			task:     domain.TaskSpec{},
			spec:     domain.DAGSpec{},
			wantVars: nil,
			wantConn: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := declaredVariables(tc.task, tc.spec); !equalStrings(got, tc.wantVars) {
				t.Errorf("declaredVariables = %v, want %v", got, tc.wantVars)
			}
			if got := declaredConnections(tc.task, tc.spec); !equalStrings(got, tc.wantConn) {
				t.Errorf("declaredConnections = %v, want %v", got, tc.wantConn)
			}
		})
	}
}

// declaredSecretNames gathers the DAG-level declaration and every task's
// narrowing declaration into one deduplicated, order-preserving set — the input
// to the registration existence check (ADR 0055 D6).
func TestDeclaredSecretNamesCollectsAndDedupes(t *testing.T) {
	spec := domain.DAGSpec{
		Variables: []string{"greeting", "farewell"},
		Tasks: []domain.TaskSpec{
			{TaskID: "a", Variables: []string{"greeting", "extra"}},
			{TaskID: "b", Variables: []string{"only_b"}},
		},
	}
	got := declaredSecretNames(spec.Variables, spec.Tasks, func(t domain.TaskSpec) []string { return t.Variables })
	want := []string{"greeting", "farewell", "extra", "only_b"}
	if !equalStrings(got, want) {
		t.Errorf("declaredSecretNames = %v, want %v (deduped, first-occurrence order)", got, want)
	}
}

func TestDeclaredSecretNamesEmpty(t *testing.T) {
	got := declaredSecretNames(nil, []domain.TaskSpec{{TaskID: "a"}}, func(t domain.TaskSpec) []string { return t.Variables })
	if len(got) != 0 {
		t.Errorf("declaredSecretNames with no declarations = %v, want empty", got)
	}
}

// missingNames returns the declared names absent from the existing set, in
// declaration order — these are what the registration check reports.
func TestMissingNames(t *testing.T) {
	declared := []string{"greeting", "nope", "warehouse", "gone"}
	existing := []string{"greeting", "warehouse"}
	got := missingNames(declared, existing)
	want := []string{"nope", "gone"}
	if !equalStrings(got, want) {
		t.Errorf("missingNames = %v, want %v", got, want)
	}
	if len(missingNames([]string{"a", "b"}, []string{"a", "b"})) != 0 {
		t.Error("missingNames should be empty when all declared names exist")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
