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

func (registerRepo) SetCurrentDagVersionAs(context.Context, string, domain.DagSetCurrent) (domain.CurrentVersionChange, error) {
	return domain.CurrentVersionChange{}, nil
}

const mixedDeploySpec = `{"schema_version":"1.0","dag_id":"etl","dag_version":"v2","image":"img:v2","min_idle_workers":2,"tasks":[` +
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
	t.Parallel()
	pools := config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 2, MaxPoolSize: 4}
	res, out := registerAgainstAPI(t, domain.WarmPolicy{Enabled: true}, pools, mixedDeploySpec)
	for _, want := range []string{
		"  execution  2 tasks warm-eligible; load on a dedicated pod: node_selector set\n",
		"  warm pool  2 idle warm workers (up to 4) warming now, kept while this version is the DAG's current version",
		"; previous pool draining: its idle workers are removed now and busy ones finish their attempt first\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("deploy output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "first run") {
		t.Errorf("deploy output still says the pool waits for the first run:\n%s", out)
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
	t.Parallel()
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
	t.Parallel()
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
		{"every task dedicated for one task reason while idle workers are kept", executionSummary{WarmPools: true,
			Tasks: []taskExecution{task("a", "node_selector set"), task("b", "node_selector set")}, Pool: on},
			"  execution  every task runs on a dedicated pod: node_selector set\n  warm pool  2 idle warm workers\n"},
		{"one task reason, no pool, previous pool draining", executionSummary{WarmPools: true,
			Tasks: []taskExecution{task("a", "resources set (a warm pod is 1 unit)"), task("b", "resources set (a warm pod is 1 unit)")},
			Pool:  executionPool{State: "none", Detail: "no warm pool for this version: no task of it is warm-eligible; previous pool draining", PreviousPool: "draining"}},
			"  execution  every task runs on a dedicated pod: resources set (a warm pod is 1 unit)\n  warm pool  no warm pool for this version: no task of it is warm-eligible; previous pool draining\n"},
		{"no pool, mixed reasons", executionSummary{WarmPools: true, Tasks: []taskExecution{task("a", "no idle warm workers kept for this DAG"), task("b", "labels set")},
			Pool: executionPool{State: "none", Detail: "no idle warm worker is kept"}},
			"  execution  0 tasks warm-eligible; on a dedicated pod: a (no idle warm workers kept for this DAG), b (labels set)\n  warm pool  no idle warm worker is kept\n"},
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

// The register's own warnings reach the deploy's stderr (#1703), except
// those the preflight already printed from its dry run, so a warning is
// printed once.
func TestDeployPrintsTheRegisterWarningsOnce(t *testing.T) {
	t.Parallel()
	unit, err := domain.ParseResourceUnit(domain.ResourceUnitConfig{CPU: "250m", Memory: "512Mi", Enforce: domain.UnitEnforceWarn})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewServer(api.Dependencies{
		Logger: discardTestLogger(), DevNoAuth: true, Versions: registerRepo{}, ResourceUnit: unit,
	}))
	defer srv.Close()
	spec := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v2","image":"img:v2","tasks":[` +
		`{"task_id":"train","type":"python","entrypoint":"dag:a","resources":{"limits":{"cpu":"2"}}}]}`
	for _, shown := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "dag.json")
		if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, warn strings.Builder
		req := registerRequest{serverURL: srv.URL, output: path, digestRef: "img@sha256:cafe", version: "v2", origin: labelDefault, warn: &warn}
		if shown {
			// The preflight printed the register's warning already.
			var s domain.DAGSpec
			if err := json.Unmarshal([]byte(spec), &s); err != nil {
				t.Fatal(err)
			}
			warned, _ := unit.CheckSpec(&s)
			req.shown = []string{warned[0].Error()}
		}
		if _, err := registerDeployedDAG(context.Background(), &out, req); err != nil {
			t.Fatalf("registerDeployedDAG: %v", err)
		}
		got := strings.Count(warn.String(), "warning: the server: ")
		want := 1
		if shown {
			want = 0
		}
		if got != want || (want == 1 && !strings.Contains(warn.String(), "train")) {
			t.Errorf("preflight shown %v: stderr = %q, want %d server warning(s) naming the task", shown, warn.String(), want)
		}
	}
}
