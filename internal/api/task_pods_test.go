package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// fakeServiceAccounts is a ServiceAccountReader over a fixed set of
// ServiceAccounts, by name the count of their imagePullSecrets.
type fakeServiceAccounts struct {
	accounts map[string]int
	err      error
	asked    []string
}

func (f *fakeServiceAccounts) ImagePullSecrets(_ context.Context, name string) (count int, found bool, err error) {
	f.asked = append(f.asked, name)
	if f.err != nil {
		return 0, false, f.err
	}
	n, ok := f.accounts[name]
	return n, ok, nil
}

// taskPodsServer is a register server whose task pods are pods.
func taskPodsServer(pods TaskPodSettings) *gin.Engine {
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
			TaskPods: pods,
		},
		Stores: StoreDeps{
			Versions: &fakeVersionRepo{created: true},
		},
	})
}

// threeAccountsSpec has a task on the default ServiceAccount (a), one on
// etl-sa (b and d) and one on gone (c).
const threeAccountsSpec = `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
	`{"task_id":"a","type":"python","entrypoint":"dag:a"},` +
	`{"task_id":"b","type":"python","entrypoint":"dag:b","execution":{"service_account":"etl-sa"}},` +
	`{"task_id":"c","type":"python","entrypoint":"dag:c","execution":{"service_account":"gone"}},` +
	`{"task_id":"d","type":"python","entrypoint":"dag:d","execution":{"service_account":"etl-sa"}}]}`

// dryRunTaskPods posts spec to the dry run and returns its task_pods.
func dryRunTaskPods(t *testing.T, srv *gin.Engine, spec string) (pods map[string]any, body string) {
	t.Helper()
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions/dry_run", spec)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	pods, ok := resp["task_pods"].(map[string]any)
	if !ok {
		t.Fatalf("task_pods = %v, want an object (%s)", resp["task_pods"], rec.Body.String())
	}
	return pods, rec.Body.String()
}

// rawAccounts is the service_accounts of the task_pods of body, as sent.
func rawAccounts(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		TaskPods struct {
			ServiceAccounts json.RawMessage `json:"service_accounts"`
		} `json:"task_pods"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	return string(resp.TaskPods.ServiceAccounts)
}

// TestDryRunReportsTaskServiceAccounts: the dry run looks up the
// ServiceAccount each task's pod would run as (its execution.service_account,
// else the server's default) once per account, and reports, sorted by name,
// which tasks use it, whether it exists and how many imagePullSecrets it
// carries, and whether task pods refuse an image that runs as root (#1889).
func TestDryRunReportsTaskServiceAccounts(t *testing.T) {
	reader := &fakeServiceAccounts{accounts: map[string]int{"etl-sa": 1, "dexaflow-task": 0}}
	pods, body := dryRunTaskPods(t, taskPodsServer(TaskPodSettings{Enabled: true, RunAsNonRoot: true,
		DefaultServiceAccount: "dexaflow-task", ServiceAccounts: reader}), threeAccountsSpec)
	if pods["run_as_non_root"] != true {
		t.Errorf("run_as_non_root = %v, want true", pods["run_as_non_root"])
	}
	got := rawAccounts(t, body)
	want := `[{"name":"dexaflow-task","tasks":["a"],"state":"found","image_pull_secrets":0},` +
		`{"name":"etl-sa","tasks":["b","d"],"state":"found","image_pull_secrets":1},` +
		`{"name":"gone","tasks":["c"],"state":"missing"}]`
	if got != want {
		t.Errorf("service_accounts = %s\nwant %s\n(%s)", got, want, body)
	}
	if len(reader.asked) != 3 {
		t.Errorf("lookups = %v, want one per account", reader.asked)
	}
}

// TestDryRunTaskPodsDefaultServiceAccount: with no default task
// ServiceAccount, a task that names none runs as the namespace's default.
func TestDryRunTaskPodsDefaultServiceAccount(t *testing.T) {
	reader := &fakeServiceAccounts{accounts: map[string]int{"default": 0}}
	pods, body := dryRunTaskPods(t, taskPodsServer(TaskPodSettings{Enabled: true, ServiceAccounts: reader}), validSpecJSON)
	if got, want := rawAccounts(t, body), `[{"name":"default","tasks":["a"],"state":"found","image_pull_secrets":0}]`; got != want {
		t.Errorf("service_accounts = %s, want %s", got, want)
	}
	if pods["run_as_non_root"] != false {
		t.Errorf("run_as_non_root = %v, want false", pods["run_as_non_root"])
	}
}

// TestDryRunTaskPodsUnchecked: a server that does not read ServiceAccounts
// says why, and a lookup that fails is unchecked with a fixed detail that
// carries nothing of the error.
func TestDryRunTaskPodsUnchecked(t *testing.T) {
	_, body := dryRunTaskPods(t, taskPodsServer(TaskPodSettings{Enabled: true, Unread: "no reader here"}), validSpecJSON)
	if got, want := rawAccounts(t, body), `[{"name":"default","tasks":["a"],"state":"unchecked","detail":"no reader here"}]`; got != want {
		t.Errorf("no reader: service_accounts = %s, want %s", got, want)
	}

	reader := &fakeServiceAccounts{err: errors.New("dial tcp 10.0.0.1:443: secret-internal-host")}
	_, body = dryRunTaskPods(t, taskPodsServer(TaskPodSettings{Enabled: true, ServiceAccounts: reader}), validSpecJSON)
	if strings.Contains(body, "secret-internal-host") {
		t.Errorf("the answer leaks the lookup error: %s", body)
	}
	if got, want := rawAccounts(t, body), `[{"name":"default","tasks":["a"],"state":"unchecked","detail":"`+serviceAccountLookupFailed+`"}]`; got != want {
		t.Errorf("failed lookup: service_accounts = %s, want %s", got, want)
	}
}

// TestDryRunTaskPodsOff: a server that runs no task pods reports none, so
// a client can tell it from an older server, which reports nothing.
func TestDryRunTaskPodsOff(t *testing.T) {
	reader := &fakeServiceAccounts{}
	pods, _ := dryRunTaskPods(t, taskPodsServer(TaskPodSettings{RunAsNonRoot: true, ServiceAccounts: reader}), validSpecJSON)
	got, _ := json.Marshal(pods)
	if want := `{"run_as_non_root":false,"service_accounts":[]}`; string(got) != want {
		t.Errorf("task_pods = %s, want %s", got, want)
	}
	if len(reader.asked) != 0 {
		t.Errorf("lookups = %v, want none", reader.asked)
	}
}

// TestDryRunTaskPodsCapsLookups: a spec with more accounts than
// maxServiceAccountLookups looks up that many; the rest are unchecked.
func TestDryRunTaskPodsCapsLookups(t *testing.T) {
	tasks := make([]string, 0, maxServiceAccountLookups+2)
	accounts := map[string]int{}
	for i := range maxServiceAccountLookups + 2 {
		name := fmt.Sprintf("sa-%02d", i)
		accounts[name] = 1
		tasks = append(tasks, fmt.Sprintf(`{"task_id":"t%02d","type":"python","entrypoint":"dag:t","execution":{"service_account":%q}}`, i, name))
	}
	spec := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` + strings.Join(tasks, ",") + `]}`
	reader := &fakeServiceAccounts{accounts: accounts}
	pods, _ := dryRunTaskPods(t, taskPodsServer(TaskPodSettings{Enabled: true, ServiceAccounts: reader}), spec)
	if len(reader.asked) != maxServiceAccountLookups {
		t.Errorf("lookups = %d, want %d", len(reader.asked), maxServiceAccountLookups)
	}
	list, _ := pods["service_accounts"].([]any)
	unchecked := 0
	for _, e := range list {
		if m, _ := e.(map[string]any); m["state"] == "unchecked" {
			unchecked++
		}
	}
	if len(list) != maxServiceAccountLookups+2 || unchecked != 2 {
		t.Errorf("service_accounts = %d entries, %d unchecked; want %d and 2", len(list), unchecked, maxServiceAccountLookups+2)
	}
}

// TestRegisterCarriesNoTaskPods: the register answer is unchanged; only
// the dry run reports task pods.
func TestRegisterCarriesNoTaskPods(t *testing.T) {
	reader := &fakeServiceAccounts{accounts: map[string]int{"default": 0}}
	rec := authGet(taskPodsServer(TaskPodSettings{Enabled: true, ServiceAccounts: reader}), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d, want 201", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "task_pods") || len(reader.asked) != 0 {
		t.Errorf("register reports task pods (%s) or looked up %v", rec.Body.String(), reader.asked)
	}
}

// TestDryRunTaskPodsInvalidName: an account name that is not a DNS-1123
// subdomain can name no ServiceAccount, so it is reported missing with a
// fixed detail that says why, and never looked up, whether or not the
// server reads ServiceAccounts.
func TestDryRunTaskPodsInvalidName(t *testing.T) {
	spec := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"a","type":"python","entrypoint":"dag:a"},` +
		`{"task_id":"b","type":"python","entrypoint":"dag:b","execution":{"service_account":"Bad_Name"}}]}`
	want := `[{"name":"Bad_Name","tasks":["b"],"state":"missing","detail":"` + serviceAccountInvalidName + `"},` +
		`{"name":"default","tasks":["a"],"state":"found","image_pull_secrets":0}]`
	reader := &fakeServiceAccounts{accounts: map[string]int{"default": 0, "Bad_Name": 1}}
	_, body := dryRunTaskPods(t, taskPodsServer(TaskPodSettings{Enabled: true, ServiceAccounts: reader}), spec)
	if got := rawAccounts(t, body); got != want {
		t.Errorf("service_accounts = %s\nwant %s", got, want)
	}
	if len(reader.asked) != 1 || reader.asked[0] != "default" {
		t.Errorf("lookups = %v, want only default", reader.asked)
	}

	_, body = dryRunTaskPods(t, taskPodsServer(TaskPodSettings{Enabled: true, Unread: "no reader here"}), spec)
	want = `[{"name":"Bad_Name","tasks":["b"],"state":"missing","detail":"` + serviceAccountInvalidName + `"},` +
		`{"name":"default","tasks":["a"],"state":"unchecked","detail":"no reader here"}]`
	if got := rawAccounts(t, body); got != want {
		t.Errorf("no reader: service_accounts = %s\nwant %s", got, want)
	}
}

// TestValidServiceAccountName: a ServiceAccount name is a DNS-1123
// subdomain of at most 253 characters.
func TestValidServiceAccountName(t *testing.T) {
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	for name, want := range map[string]bool{
		"a": true, "etl-sa": true, "team.etl-sa": true, "0sa9": true, long: true,
		long + "e": false, "": false, "Bad_Name": false, "bad_name": false, "Upper": false, "-sa": false, "sa-": false,
		"a..b": false, ".a": false, "a.": false, ".": false, "..": false, "a/b": false, "a%2Fb": false, "sa\n": false,
	} {
		if got := validServiceAccountName(name); got != want {
			t.Errorf("validServiceAccountName(%q) = %v, want %v", name, got, want)
		}
	}
	if len(long) != 253 {
		t.Fatalf("long is %d characters, want 253", len(long))
	}
}

// recordingHandler is a slog handler that keeps the string value of every
// attribute it is given.
type recordingHandler struct{ values *[]string }

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		*h.values = append(*h.values, a.Value.String())
		return true
	})
	return nil
}
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

// TestTaskPodsLookupErrorLogIsSafe: the log line of a failed lookup
// carries no line break of the account name or of the error, so neither
// can forge a log line.
func TestTaskPodsLookupErrorLogIsSafe(t *testing.T) {
	var values []string
	r := taskPodReporter{
		settings: TaskPodSettings{Enabled: true, ServiceAccounts: &fakeServiceAccounts{err: errors.New("boom\r\nlevel=ERROR forged")}},
		logger:   slog.New(recordingHandler{values: &values}),
	}
	check := &serviceAccountCheck{Name: "etl-sa"}
	r.lookUp(context.Background(), check, true)
	if len(values) == 0 {
		t.Fatal("the failed lookup logged nothing")
	}
	for _, v := range values {
		if strings.ContainsAny(v, "\r\n") {
			t.Errorf("a logged value carries a line break: %q", v)
		}
	}
}
