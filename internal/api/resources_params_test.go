package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// specWithParams builds a DAGSpec that declares the given params.
func specWithParams(params map[string]domain.ParamSpec) domain.DAGSpec {
	return domain.DAGSpec{
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img:v1",
		Params: params,
		Tasks:  []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"}},
	}
}

// triggerServer builds a server whose Specs returns spec and whose DagRuns
// records the created run so a test can read back the persisted conf.
func triggerServer(spec domain.DAGSpec) (srv *gin.Engine, runs *fakeRunRepo) {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	runs = &fakeRunRepo{}
	e := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: admin},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		DagRuns:       runs,
		Specs:         &fakeSpecReader{spec: spec},
	})
	return e, runs
}

func confOf(t *testing.T, rec interface{ Bytes() []byte }) map[string]any {
	t.Helper()
	var got struct {
		Conf map[string]any `json:"conf"`
	}
	if err := json.Unmarshal(rec.Bytes(), &got); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	return got.Conf
}

// TestTriggerMaterializesDeclaredDefaults pins that when the run supplies no
// conf, the declared param defaults are merged into the persisted conf.
func TestTriggerMaterializesDeclaredDefaults(t *testing.T) {
	srv, _ := triggerServer(specWithParams(map[string]domain.ParamSpec{
		"limit": {Default: json.RawMessage(`5`), Schema: json.RawMessage(`{}`)},
	}))
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{}`)
	if rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("trigger = %d, want 2xx; body=%q", rec.Code, rec.Body.String())
	}
	conf := confOf(t, rec.Body)
	if conf["limit"] != float64(5) {
		t.Errorf("conf = %v, want the declared default limit=5 materialized", conf)
	}
}

// TestTriggerConfOverridesDefault pins that a supplied conf value wins over the
// declared default, per key.
func TestTriggerConfOverridesDefault(t *testing.T) {
	srv, _ := triggerServer(specWithParams(map[string]domain.ParamSpec{
		"limit": {Default: json.RawMessage(`5`), Schema: json.RawMessage(`{}`)},
	}))
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"conf":{"limit":42}}`)
	if rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("trigger = %d, want 2xx; body=%q", rec.Code, rec.Body.String())
	}
	conf := confOf(t, rec.Body)
	if conf["limit"] != float64(42) {
		t.Errorf("conf = %v, want the supplied limit=42 to override the default", conf)
	}
}

// TestTriggerKeepsExtraConfKeys pins Airflow's dag_run_conf_overrides_params
// default: conf keys with no declared param are allowed and carried through.
func TestTriggerKeepsExtraConfKeys(t *testing.T) {
	srv, _ := triggerServer(specWithParams(map[string]domain.ParamSpec{
		"limit": {Default: json.RawMessage(`5`), Schema: json.RawMessage(`{}`)},
	}))
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"conf":{"extra":"kept"}}`)
	if rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("trigger = %d, want 2xx; body=%q", rec.Code, rec.Body.String())
	}
	conf := confOf(t, rec.Body)
	if conf["extra"] != "kept" {
		t.Errorf("conf = %v, want the extra (undeclared) key kept", conf)
	}
	if conf["limit"] != float64(5) {
		t.Errorf("conf = %v, want the default still materialized alongside extras", conf)
	}
}

// TestTriggerRejectsWrongType pins that a conf value violating a param's schema
// (a string where an integer is required) is a 400.
func TestTriggerRejectsWrongType(t *testing.T) {
	srv, _ := triggerServer(specWithParams(map[string]domain.ParamSpec{
		"n": {Default: json.RawMessage(`3`), Schema: json.RawMessage(`{"type":"integer","minimum":1}`)},
	}))
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"conf":{"n":"nope"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong-type conf = %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

// TestTriggerRejectsBelowMinimum pins that a conf value that breaks a numeric
// constraint (below minimum) is a 400.
func TestTriggerRejectsBelowMinimum(t *testing.T) {
	srv, _ := triggerServer(specWithParams(map[string]domain.ParamSpec{
		"n": {Default: json.RawMessage(`3`), Schema: json.RawMessage(`{"type":"integer","minimum":1}`)},
	}))
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"conf":{"n":0}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("below-minimum conf = %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

// TestTriggerAcceptsValidTypedValue pins that a conf value satisfying the schema
// passes and is persisted.
func TestTriggerAcceptsValidTypedValue(t *testing.T) {
	srv, _ := triggerServer(specWithParams(map[string]domain.ParamSpec{
		"n": {Default: json.RawMessage(`3`), Schema: json.RawMessage(`{"type":"integer","minimum":1}`)},
	}))
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"conf":{"n":7}}`)
	if rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("valid typed conf = %d, want 2xx; body=%q", rec.Code, rec.Body.String())
	}
	if conf := confOf(t, rec.Body); conf["n"] != float64(7) {
		t.Errorf("conf = %v, want n=7 persisted", conf)
	}
}

// TestTriggerTypedDefaultMaterialized pins that a run supplying no conf for a
// typed param materializes the declared default without a spurious validation
// failure.
func TestTriggerTypedDefaultMaterialized(t *testing.T) {
	srv, _ := triggerServer(specWithParams(map[string]domain.ParamSpec{
		"n": {Default: json.RawMessage(`3`), Schema: json.RawMessage(`{"type":"integer","minimum":1}`)},
	}))
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{}`)
	if rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("trigger = %d, want 2xx; body=%q", rec.Code, rec.Body.String())
	}
	if conf := confOf(t, rec.Body); conf["n"] != float64(3) {
		t.Errorf("conf = %v, want typed default n=3 materialized", conf)
	}
}

// TestTriggerFailsLoudOnSpecReadError pins that a real spec-read failure fails
// the trigger loud (500) rather than silently skipping param validation — a DAG
// that declares typed params must not be triggered unvalidated on a transient
// error. (A missing version is handled separately by the create path.)
func TestTriggerFailsLoudOnSpecReadError(t *testing.T) {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	e := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: admin},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		DagRuns:       &fakeRunRepo{},
		Specs:         &fakeSpecReader{err: errors.New("postgres briefly unreachable")},
	})
	rec := authGet(e, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"conf":{"x":1}}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("spec-read error trigger = %d, want 500 (fail loud, not a silent validation skip); body=%q", rec.Code, rec.Body.String())
	}
}

// TestTriggerRejectsMissingRequiredParam pins that a param declared with no
// default is required: a trigger that omits it is a 400.
func TestTriggerRejectsMissingRequiredParam(t *testing.T) {
	srv, _ := triggerServer(specWithParams(map[string]domain.ParamSpec{
		"n": {Schema: json.RawMessage(`{"type":"integer"}`)}, // no Default -> required
	}))
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing required param = %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

// TestTriggerAcceptsProvidedRequiredParam pins that supplying the required param
// in conf passes and is persisted.
func TestTriggerAcceptsProvidedRequiredParam(t *testing.T) {
	srv, _ := triggerServer(specWithParams(map[string]domain.ParamSpec{
		"n": {Schema: json.RawMessage(`{"type":"integer"}`)},
	}))
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"conf":{"n":7}}`)
	if rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("provided required param = %d, want 2xx; body=%q", rec.Code, rec.Body.String())
	}
	if conf := confOf(t, rec.Body); conf["n"] != float64(7) {
		t.Errorf("conf = %v, want n=7 persisted", conf)
	}
}
