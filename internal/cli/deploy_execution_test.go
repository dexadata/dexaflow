package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

// The deploy output says which tasks run where (#1707). These tests drive the
// real register endpoint (api.NewServer) with a fake repository, so the lines
// are read from what the server answers, not from a hand-written body.

// registerRepo is a DagVersionRepository that registers a new version of an
// existing DAG.
type registerRepo struct{}

func (registerRepo) RegisterDagVersionAs(context.Context, string, domain.DagRegistration) (domain.DagRegistered, error) {
	return domain.DagRegistered{Created: true}, nil
}

func (registerRepo) SetCurrentDagVersion(context.Context, string, string, domain.DagVersionSelector, string, func(domain.DAGSpec) error) (domain.CurrentVersionChange, error) {
	return domain.CurrentVersionChange{}, nil
}

const mixedDeploySpec = `{"schema_version":"1.0","dag_id":"etl","dag_version":"v2","image":"img:v2","tasks":[` +
	`{"task_id":"extract","type":"python","entrypoint":"dag:extract"},` +
	`{"task_id":"transform","type":"python","entrypoint":"dag:transform"},` +
	`{"task_id":"load","type":"python","entrypoint":"dag:load","execution":{"node_selector":{"pool":"db"}}}]}`

func registerAgainstAPI(t *testing.T, policy domain.WarmPolicy, pools config.ExecutionSection, spec string) (res deployResult, out string) {
	t.Helper()
	srv := httptest.NewServer(api.NewServer(api.Dependencies{
		Logger:     discardTestLogger(),
		DevNoAuth:  true,
		Versions:   registerRepo{},
		WarmPolicy: policy,
		WarmPools:  pools,
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	res, err := registerDeployedDAG(context.Background(), &sb, registerRequest{serverURL: srv.URL, output: path,
		digestRef: "img@sha256:cafe", version: "v2", origin: labelDefault})
	if err != nil {
		t.Fatalf("registerDeployedDAG: %v", err)
	}
	return res, sb.String()
}

// TestDeployReportsExecutionModes: with warm pools on, the deploy says how
// many tasks are warm-eligible, which task takes a dedicated pod and why,
// and what the version's warm pool does; --output json carries the same
// report.
func TestDeployReportsExecutionModes(t *testing.T) {
	pools := config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 2, MaxPoolSize: 4}
	res, out := registerAgainstAPI(t, domain.WarmPolicy{Enabled: true}, pools, mixedDeploySpec)
	for _, want := range []string{
		"  execution  2 tasks warm-eligible; load on a dedicated pod: node_selector set\n",
		"  warm pool  2 idle warm workers (up to 4) start with this version's first run",
		"the previous version's warm workers drain once its last active run ends\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("deploy output lacks %q:\n%s", want, out)
		}
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Execution *struct {
			WarmPools bool `json:"warm_pools"`
			Tasks     []struct {
				TaskID string `json:"task_id"`
				Mode   string `json:"mode"`
				Reason string `json:"reason"`
			} `json:"tasks"`
			Pool struct {
				State        string `json:"state"`
				PreviousPool string `json:"previous_pool"`
			} `json:"pool"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	e := doc.Execution
	if e == nil || !e.WarmPools || len(e.Tasks) != 3 || e.Tasks[2].Mode != "dedicated" || e.Tasks[2].Reason != "node_selector set" ||
		e.Pool.State != "on" || e.Pool.PreviousPool != "draining" {
		t.Errorf("--output json execution = %s", raw)
	}
}

// TestDeployReportsWarmPoolsOff: with warm pools off the deploy says, in one
// line, that every task runs on a dedicated pod, and prints no pool line.
func TestDeployReportsWarmPoolsOff(t *testing.T) {
	_, out := registerAgainstAPI(t, domain.WarmPolicy{}, config.ExecutionSection{}, mixedDeploySpec)
	if !strings.Contains(out, "  execution  every task runs on a dedicated pod: warm pools off\n") {
		t.Errorf("deploy output lacks the one-line dedicated summary:\n%s", out)
	}
	if strings.Contains(out, "warm pool ") || strings.Count(out, "execution") != 1 {
		t.Errorf("warm pools off printed more than one execution line:\n%s", out)
	}
}

// TestExecutionSummaryLines covers the summary's wording.
func TestExecutionSummaryLines(t *testing.T) {
	task := func(id, reason string) taskExecution {
		if reason == "" {
			return taskExecution{TaskID: id, Mode: "warm"}
		}
		return taskExecution{TaskID: id, Mode: "dedicated", Reason: reason}
	}
	on := executionPool{State: "on", Detail: "2 idle warm workers"}
	cases := []struct {
		name string
		e    executionSummary
		want string
	}{
		{"all warm", executionSummary{WarmPools: true, Tasks: []taskExecution{task("a", ""), task("b", "")}, Pool: on},
			"  execution  2 tasks warm-eligible\n  warm pool  2 idle warm workers\n"},
		{"one task", executionSummary{WarmPools: true, Tasks: []taskExecution{task("a", "")}, Pool: on},
			"  execution  1 task warm-eligible\n  warm pool  2 idle warm workers\n"},
		{"several dedicated", executionSummary{WarmPools: true, Tasks: []taskExecution{task("a", ""), task("b", "labels set"), task("c", "service_account set")}, Pool: on},
			"  execution  1 task warm-eligible; on a dedicated pod: b (labels set), c (service_account set)\n  warm pool  2 idle warm workers\n"},
		{"one version reason", executionSummary{WarmPools: true, Tasks: []taskExecution{task("a", "staging enabled"), task("b", "staging enabled")},
			Pool: executionPool{State: "none", Detail: "no warm pool for this version: staging enabled"}},
			"  execution  every task runs on a dedicated pod: staging enabled\n"},
		{"none warm, mixed reasons", executionSummary{WarmPools: true, Tasks: []taskExecution{task("a", "labels set"), task("b", "annotations set")}, Pool: on},
			"  execution  0 tasks warm-eligible; on a dedicated pod: a (labels set), b (annotations set)\n  warm pool  2 idle warm workers\n"},
	}
	for _, c := range cases {
		if got := executionLines(&c.e); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	if got := executionLines(nil); got != "" {
		t.Errorf("an older server's answer printed %q, want nothing", got)
	}
}
