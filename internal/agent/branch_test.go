package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// branchCmd plays the runtime of a branch task: it writes decision to the
// file LEOFLOW_BRANCH_PATH names (nothing when decision is empty) and exits 0.
type branchCmd struct {
	decision string
	env      []string
}

func (c *branchCmd) Run(_ context.Context, _, env []string, _, _ io.Writer) (int, error) {
	c.env = env
	if c.decision == "" {
		return 0, nil
	}
	for _, kv := range env {
		if p, ok := strings.CutPrefix(kv, "LEOFLOW_BRANCH_PATH="); ok {
			if err := os.WriteFile(p, []byte(c.decision), 0o600); err != nil {
				return 1, err
			}
		}
	}
	return 0, nil
}

func branchRunner(t *testing.T, spec *agentv1.TaskSpec, cmd *branchCmd) (*Runner, *fakeClient) {
	t.Helper()
	dir := t.TempDir()
	client := &fakeClient{spec: spec}
	r := newRunner(client, nil, &recordingSink{})
	r.Cmd = cmd
	r.BranchPath = filepath.Join(dir, "branch.json")
	r.TerminationLogPath = filepath.Join(dir, "termination-log")
	return r, client
}

func branchSpec(kind string) *agentv1.TaskSpec {
	return &agentv1.TaskSpec{
		Operator: "python", Entrypoint: "dag:pick", TaskId: "pick",
		Branch:     &agentv1.BranchSpec{Kind: kind, IgnoreDownstreamTriggerRules: true},
		DagTaskIds: []string{"pick", "a", "b"}, DownstreamTaskIds: []string{"a", "b"},
	}
}

func lastReport(t *testing.T, c *fakeClient) *agentv1.ReportStateRequest {
	t.Helper()
	if len(c.reports) == 0 {
		t.Fatal("no report")
	}
	return c.reports[len(c.reports)-1]
}

func TestBranchTaskGetsTheBranchEnvironment(t *testing.T) {
	cmd := &branchCmd{decision: `{"follow_task_ids":["a"]}`}
	r, _ := branchRunner(t, branchSpec("branch"), cmd)
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"LEOFLOW_BRANCH_KIND":       "branch",
		"LEOFLOW_BRANCH_PATH":       r.BranchPath,
		"LEOFLOW_BRANCH_TASK_IDS":   `["pick","a","b"]`,
		"LEOFLOW_BRANCH_DOWNSTREAM": `["a","b"]`,
	}
	for k, v := range want {
		if got := envValue(cmd.env, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestPlainTaskGetsNoBranchEnvironment(t *testing.T) {
	cmd := &branchCmd{}
	r, _ := branchRunner(t, &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:x"}, cmd)
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, kv := range cmd.env {
		if strings.HasPrefix(kv, "LEOFLOW_BRANCH_") {
			t.Errorf("plain task got %s", kv)
		}
	}
}

func TestBranchDecisionRidesTheSuccessReportAndTheRecord(t *testing.T) {
	r, client := branchRunner(t, branchSpec("branch"), &branchCmd{decision: `{"follow_task_ids":["a","b"]}`})
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	rep := lastReport(t, client)
	if rep.GetState() != agentv1.TaskState_TASK_STATE_SUCCESS || rep.GetBranch() == nil ||
		!reflect.DeepEqual(rep.GetBranch().GetFollowTaskIds(), []string{"a", "b"}) {
		t.Fatalf("report = %+v", rep)
	}
	raw, err := os.ReadFile(r.TerminationLogPath)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := taskoutcome.Decode(string(raw))
	if !ok || rec.Branch == nil || !reflect.DeepEqual(rec.Branch.FollowTaskIDs, []string{"a", "b"}) {
		t.Fatalf("record = %s", raw)
	}
}

func TestShortCircuitDecisionIsReported(t *testing.T) {
	r, client := branchRunner(t, branchSpec("short_circuit"), &branchCmd{decision: `{"short_circuit_falsy":true}`})
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b := lastReport(t, client).GetBranch(); b == nil || !b.GetShortCircuitFalsy() {
		t.Fatalf("branch = %+v, want falsy", b)
	}
}

// A branch task whose runtime wrote no decision reports its success without
// one, and records none: the control plane settles it failed (ADR 0064 D1).
func TestBranchTaskWithoutDecisionReportsNone(t *testing.T) {
	r, client := branchRunner(t, branchSpec("branch"), &branchCmd{})
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b := lastReport(t, client).GetBranch(); b != nil {
		t.Fatalf("branch = %+v, want none", b)
	}
	raw, _ := os.ReadFile(r.TerminationLogPath)
	if rec, ok := taskoutcome.Decode(string(raw)); !ok || rec.Branch != nil {
		t.Fatalf("record = %s, want a success without a decision", raw)
	}
}

// A decision too large for the termination message leaves no record at all,
// so a lost report can never be recovered as a success without its decision.
func TestOversizedBranchDecisionWritesNoRecord(t *testing.T) {
	ids := make([]string, 300)
	for i := range ids {
		ids[i] = strings.Repeat("x", 40)
	}
	dec, _ := json.Marshal(map[string]any{"follow_task_ids": ids})
	r, client := branchRunner(t, branchSpec("branch"), &branchCmd{decision: string(dec)})
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lastReport(t, client).GetBranch().GetFollowTaskIds()) != 300 {
		t.Error("the report must still carry the whole decision")
	}
	if raw, err := os.ReadFile(r.TerminationLogPath); err == nil && len(raw) > 0 {
		t.Fatalf("record = %s, want none", raw)
	}
}

// A decision file that does not decode is reported as no decision.
func TestUndecodableBranchDecisionIsNotReported(t *testing.T) {
	r, client := branchRunner(t, branchSpec("branch"), &branchCmd{decision: `not json`})
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b := lastReport(t, client).GetBranch(); b != nil {
		t.Fatalf("branch = %+v, want none", b)
	}
}
