package api

import (
	"encoding/json"
	"net/http"
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
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Versions:      repo,
		WarmPolicy:    policy,
		WarmPools:     exec,
	})
}

const mixedSpecJSON = `{"schema_version":"1.0","dag_id":"etl","dag_version":"v2","image":"img:v2","tasks":[` +
	`{"task_id":"extract","type":"python","entrypoint":"dag:extract"},` +
	`{"task_id":"load","type":"python","entrypoint":"dag:load","execution":{"node_selector":{"pool":"db"}}},` +
	`{"task_id":"report","type":"python","entrypoint":"dag:report","execution":{"service_account":"reporting"}}]}`

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
		rec := authGet(warmVersionServer(repo, policy, warmOn), http.MethodPost, path, mixedSpecJSON)
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
		if p.Detail == "" {
			t.Errorf("%s: pool detail is empty", path)
		}
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
// (ADR 0058 D5), and with no idle worker kept (min_idle_workers 0) a pool
// never starts; a new DAG has no previous pool.
func TestRegisterReportsNoPool(t *testing.T) {
	policy := domain.WarmPolicy{Enabled: true}
	staging := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","staging":{"enabled":true},` +
		`"tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`
	cases := []struct {
		name, spec string
		exec       config.ExecutionSection
		reason     string
	}{
		{"staging", staging, warmOn, "staging enabled"},
		{"no idle workers", validSpecJSON, config.ExecutionSection{WarmPoolsEnabled: true, MaxPoolSize: 5}, ""},
	}
	for _, c := range cases {
		rec := authGet(warmVersionServer(&fakeVersionRepo{created: true, dagCreated: true}, policy, c.exec),
			http.MethodPost, "/api/v2/dags/etl/versions", c.spec)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: register = %d (%s)", c.name, rec.Code, rec.Body.String())
		}
		a := decodeReport(t, rec.Body.Bytes())
		if got := a.Execution.Tasks[0].Reason; got != c.reason {
			t.Errorf("%s: task reason = %q, want %q", c.name, got, c.reason)
		}
		if p := a.Execution.Pool; p.State != "none" || p.PreviousPool != "" {
			t.Errorf("%s: pool = %+v, want none and no previous pool", c.name, p)
		}
	}
}
