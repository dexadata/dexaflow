package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

// The register response says, per task, where its attempts run and why, and
// what the version's warm pool will do (#1707), from the same decision
// dispatch places with (domain.WarmPolicy).

func warmVersionServer(repo DagVersionRepository, policy domain.WarmPolicy, exec config.ExecutionSection) *gin.Engine {
	return warmVersionServerAbsent(repo, policy, exec, "")
}

func warmVersionServerAbsent(repo DagVersionRepository, policy domain.WarmPolicy, exec config.ExecutionSection, absent string) *gin.Engine {
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Execution: ExecutionDeps{
			WarmPolicy:        policy,
			WarmPools:         exec.WarmPoolDefaults(),
			WarmWorkersAbsent: absent,
		},
		Stores: StoreDeps{
			Versions: repo,
		},
	})
}

const mixedSpecJSON = `{"schema_version":"1.0","dag_id":"etl","dag_version":"v2","image":"img:v2","tasks":[` +
	`{"task_id":"extract","type":"python","entrypoint":"dag:extract"},` +
	`{"task_id":"load","type":"python","entrypoint":"dag:load","execution":{"node_selector":{"pool":"db"}}},` +
	`{"task_id":"report","type":"python","entrypoint":"dag:report","execution":{"service_account":"reporting"}}]}`

// mixedDeclaredSpecJSON is mixedSpecJSON with a declared min_idle_workers,
// the only DAG a register prewarms (ADR 0082 D3).
var mixedDeclaredSpecJSON = strings.Replace(mixedSpecJSON, `"image":"img:v2",`, `"image":"img:v2","min_idle_workers":2,`, 1)

type reportAnswer struct {
	Execution *struct {
		WarmPools bool `json:"warm_pools"`
		Tasks     []struct {
			TaskID string `json:"task_id"`
			Mode   string `json:"mode"`
			Reason string `json:"reason"`
		} `json:"tasks"`
		Pool struct {
			State          string `json:"state"`
			Detail         string `json:"detail"`
			MinIdleWorkers int    `json:"min_idle_workers"`
			MaxPoolSize    int    `json:"max_pool_size"`
			PreviousPool   string `json:"previous_pool"`
		} `json:"pool"`
	} `json:"execution"`
}

func decodeReport(t *testing.T, body []byte) reportAnswer {
	t.Helper()
	var a reportAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatal(err)
	}
	if a.Execution == nil {
		t.Fatalf("no execution report in %s", body)
	}
	return a
}

var warmOn = config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 2, MaxPoolSize: 5}

// TestRegisterReportsExecutionModePerTask: with warm pools on, the register
// and its dry run name each task's mode, the reason a task takes a dedicated
// pod, and the version's pool: idle workers kept, the cap, and the previous
// version's pool draining for a new version of an existing DAG.
func TestRegisterReportsExecutionModePerTask(t *testing.T) {
	policy := domain.WarmPolicy{Enabled: true, ServiceAccount: "dexaflow-task"}
	for _, path := range []string{"/api/v2/dags/etl/versions", "/api/v2/dags/etl/versions/dry_run"} {
		repo := &fakeVersionRepo{created: true}
		rec := authGet(warmVersionServer(repo, policy, warmOn), http.MethodPost, path, mixedDeclaredSpecJSON)
		if rec.Code >= http.StatusMultipleChoices {
			t.Fatalf("%s = %d (%s)", path, rec.Code, rec.Body.String())
		}
		a := decodeReport(t, rec.Body.Bytes())
		if !a.Execution.WarmPools {
			t.Errorf("%s: warm_pools = false, want true", path)
		}
		want := []struct{ id, mode, reason string }{
			{"extract", "warm", ""},
			{"load", "dedicated", "node_selector set"},
			{"report", "dedicated", "service_account set"},
		}
		if len(a.Execution.Tasks) != len(want) {
			t.Fatalf("%s: tasks = %+v, want %d", path, a.Execution.Tasks, len(want))
		}
		for i, w := range want {
			got := a.Execution.Tasks[i]
			if got.TaskID != w.id || got.Mode != w.mode || got.Reason != w.reason {
				t.Errorf("%s: task %d = %+v, want %+v", path, i, got, w)
			}
		}
		p := a.Execution.Pool
		if p.State != "on" || p.MinIdleWorkers != 2 || p.MaxPoolSize != 5 || p.PreviousPool != "draining" {
			t.Errorf("%s: pool = %+v, want on, 2 idle, up to 5, previous draining", path, p)
		}
		for _, want := range []string{"2 idle warm workers (up to 5) warming now", "previous pool draining"} {
			if !strings.Contains(p.Detail, want) {
				t.Errorf("%s: pool detail %q lacks %q", path, p.Detail, want)
			}
		}
		if strings.Contains(p.Detail, "first run") {
			t.Errorf("%s: pool detail %q still waits for the first run", path, p.Detail)
		}
	}
}

// TestRegisterReportsPausedDagPoolWaitsForUnpause: a paused DAG is not
// prewarmed (ADR 0082), so its pool is reported as starting at the unpause,
// not as warming.
func TestRegisterReportsPausedDagPoolWaitsForUnpause(t *testing.T) {
	policy := domain.WarmPolicy{Enabled: true}
	rec := authGet(warmVersionServer(&fakeVersionRepo{created: true, dagCreated: true, paused: true}, policy, warmOn),
		http.MethodPost, "/api/v2/dags/etl/versions", mixedDeclaredSpecJSON)
	p := decodeReport(t, rec.Body.Bytes()).Execution.Pool
	if p.State != "on" || !strings.Contains(p.Detail, "start when the DAG is unpaused") || strings.Contains(p.Detail, "warming now") {
		t.Errorf("paused DAG: pool = %+v, want on, starting when the DAG is unpaused", p)
	}
}

// TestRegisterReportsWarmPoolsOff: with warm pools off, the default, every
// task runs on a dedicated pod for that one reason, and the pool says so.
func TestRegisterReportsWarmPoolsOff(t *testing.T) {
	rec := authGet(warmVersionServer(&fakeVersionRepo{created: true}, domain.WarmPolicy{}, config.ExecutionSection{}),
		http.MethodPost, "/api/v2/dags/etl/versions", mixedSpecJSON)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d (%s)", rec.Code, rec.Body.String())
	}
	a := decodeReport(t, rec.Body.Bytes())
	if a.Execution.WarmPools {
		t.Error("warm_pools = true, want false")
	}
	for _, task := range a.Execution.Tasks {
		if task.Mode != "dedicated" || task.Reason != "warm pools off" {
			t.Errorf("task %+v, want dedicated: warm pools off", task)
		}
	}
	if p := a.Execution.Pool; p.State != "off" || p.PreviousPool != "" || p.MinIdleWorkers != 0 {
		t.Errorf("pool = %+v, want off and nothing else", p)
	}
}

// TestRegisterReportsNoPool: a staging version gets no warm pool at all
// (ADR 0058 D5); with no idle worker kept (min_idle_workers 0) the reconciler
// creates none, and on a server that runs no warm workers (the subprocess
// executor, or pod dispatch disabled) none exists either: a task otherwise
// warm-eligible is reported dedicated with that reason, while a task with a
// reason of its own keeps it. A version none of whose tasks is warm-eligible
// gets no warm worker either (#2065). A new DAG has no previous pool.
func TestRegisterReportsNoPool(t *testing.T) {
	policy := domain.WarmPolicy{Enabled: true}
	staging := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","staging":{"enabled":true},` +
		`"tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`
	twoTasks := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"a","type":"python","entrypoint":"dag:a"},` +
		`{"task_id":"b","type":"python","entrypoint":"dag:b","execution":{"labels":{"team":"x"}}}]}`
	noWarmTask := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"a","type":"python","entrypoint":"dag:a","execution":{"labels":{"team":"x"}}},` +
		`{"task_id":"b","type":"bash","entrypoint":"echo b","execution":{"node_selector":{"pool":"db"}}}]}`
	cases := []struct {
		name, spec string
		exec       config.ExecutionSection
		absent     string
		reasons    []string
	}{
		{"staging", staging, warmOn, "", []string{"staging enabled"}},
		{"no idle workers", twoTasks, config.ExecutionSection{WarmPoolsEnabled: true, MaxPoolSize: 5}, "",
			[]string{"no idle warm workers kept for this DAG", "labels set"}},
		{"no warm workers on the server", twoTasks, warmOn, "the subprocess executor runs no warm workers",
			[]string{"the subprocess executor runs no warm workers", "labels set"}},
		{"no warm-eligible task", noWarmTask, warmOn, "", []string{"labels set", "node_selector set"}},
	}
	for _, c := range cases {
		rec := authGet(warmVersionServerAbsent(&fakeVersionRepo{created: true, dagCreated: true}, policy, c.exec, c.absent),
			http.MethodPost, "/api/v2/dags/etl/versions", c.spec)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: register = %d (%s)", c.name, rec.Code, rec.Body.String())
		}
		a := decodeReport(t, rec.Body.Bytes())
		for i, want := range c.reasons {
			if got := a.Execution.Tasks[i]; got.Mode != "dedicated" || got.Reason != want {
				t.Errorf("%s: task %d = %+v, want dedicated: %s", c.name, i, got, want)
			}
		}
		if p := a.Execution.Pool; p.State != "none" || p.PreviousPool != "" {
			t.Errorf("%s: pool = %+v, want none and no previous pool", c.name, p)
		}
	}
}

// TestRegisterReportsPreviousPoolOnlyWhenOneCouldExist: a new version of an
// existing DAG says the previous pool drains only when this server could have
// kept warm workers for it.
func TestRegisterReportsPreviousPoolOnlyWhenOneCouldExist(t *testing.T) {
	policy := domain.WarmPolicy{Enabled: true}
	cases := []struct {
		name   string
		exec   config.ExecutionSection
		absent string
		want   string
	}{
		{"idle workers kept", warmOn, "", "draining"},
		{"min idle 0", config.ExecutionSection{WarmPoolsEnabled: true, MaxPoolSize: 5}, "", ""},
		{"no warm workers on the server", warmOn, "pod dispatch is disabled on this server", ""},
	}
	for _, c := range cases {
		rec := authGet(warmVersionServerAbsent(&fakeVersionRepo{created: true}, policy, c.exec, c.absent),
			http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
		a := decodeReport(t, rec.Body.Bytes())
		if got := a.Execution.Pool.PreviousPool; got != c.want {
			t.Errorf("%s: previous_pool = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRegisterReportsUndeclaredMinIdleWaitsForFirstRun: a DAG that declares
// no min_idle_workers is not prewarmed (ADR 0082 D3): under the operator
// floor its pool starts with its first run, and the report says so and why.
func TestRegisterReportsUndeclaredMinIdleWaitsForFirstRun(t *testing.T) {
	policy := domain.WarmPolicy{Enabled: true}
	rec := authGet(warmVersionServer(&fakeVersionRepo{created: true}, policy, warmOn),
		http.MethodPost, "/api/v2/dags/etl/versions", mixedSpecJSON)
	p := decodeReport(t, rec.Body.Bytes()).Execution.Pool
	for _, want := range []string{"2 idle warm workers (up to 5) start with this version's first run", "declares no min_idle_workers", "previous pool draining"} {
		if !strings.Contains(p.Detail, want) {
			t.Errorf("pool detail %q lacks %q", p.Detail, want)
		}
	}
	if p.State != "on" || strings.Contains(p.Detail, "warming now") {
		t.Errorf("undeclared DAG: pool = %+v, want on and not warming", p)
	}
}

// TestRegisterExecutionReportJSON pins the execution report the register
// response carries, byte for byte, now that the warm pool settings reach the
// API as domain.WarmPoolDefaults rather than the config section (#1948).
func TestRegisterExecutionReportJSON(t *testing.T) {
	policy := domain.WarmPolicy{Enabled: true, ServiceAccount: "dexaflow-task"}
	rec := authGet(warmVersionServer(&fakeVersionRepo{created: true}, policy, warmOn), http.MethodPost,
		"/api/v2/dags/etl/versions/dry_run", mixedDeclaredSpecJSON)
	if rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("dry_run = %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Execution json.RawMessage `json:"execution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	const want = `{"warm_pools":true,"tasks":[` +
		`{"task_id":"extract","mode":"warm"},` +
		`{"task_id":"load","mode":"dedicated","reason":"node_selector set"},` +
		`{"task_id":"report","mode":"dedicated","reason":"service_account set"}],` +
		`"pool":{"state":"on","detail":"2 idle warm workers (up to 5) warming now, kept while this version is the DAG's current version; ` +
		`previous pool draining: its idle workers are removed now and busy ones finish their attempt first",` +
		`"min_idle_workers":2,"max_pool_size":5,"previous_pool":"draining"}}`
	if got := string(body.Execution); got != want {
		t.Errorf("execution report =\n%s\nwant\n%s", got, want)
	}
}
