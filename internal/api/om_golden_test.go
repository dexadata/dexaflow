package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// omServer is one server carrying every dependency OpenMetadata's REST connector
// touches, so the captured bodies are mutually consistent: the DAG in the list is
// the DAG whose tasks and runs are captured. Two separate fixture servers would
// let dags.json and tasks.json describe different DAGs, which OM never sees (it
// fetches tasks per dag_id from the list) and which would make a pairing
// assertion vacuous.
func omServer() *gin.Engine {
	sched := "0 5 * * *"
	spec := diamondSpec()
	return NewServer(Dependencies{
		Logger: discardLogger(),
		Authenticator: &fakeAuthn{
			user:  &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}},
			token: "golden-token",
		},
		RateLimiter:  auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:  []string{"*"},
		TokenTTLSecs: 3600,
		Specs:        &fakeSpecReader{spec: spec},
		Dags:         &fakeDagRepo{dags: []domain.DAG{{DagID: spec.DagID, Owner: "data", Schedule: &sched, Tags: []string{"x"}}}},
		DagRuns:      &fakeRunRepo{runs: []domain.DagRun{{DagID: spec.DagID, RunID: "r1", State: domain.DagRunStateQueued}}},
		Tasks:        &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", RunID: "r1", State: domain.TaskStateQueued}}},
	})
}

// TestWriteOpenMetadataGoldenBodies captures every response OpenMetadata's REST
// connector reads, so test/om-contract/validate.py can check them against OM's
// own pydantic models and its own client behavior.
//
// Two layers are needed, not one. A Go test cannot prove that pydantic accepts a
// body: the #1149-era defect was a field that is valid JSON, valid against our
// OpenAPI spec, and rejected by OM's type. Equally, the Python layer cannot run
// the server. So Go writes the bodies and Python judges them.
//
// The six captures are the connector's whole HTTP surface, in the order it calls
// them: POST /auth/token (the bearer it then uses everywhere), GET
// /api/v2/version (which also decides v1 vs v2 under apiVersion: auto), the DAG
// list, one DAG's tasks, its runs, and one run's task instances.
//
// Writing is gated on LEOFLOW_OM_GOLDEN so a normal `go test` run stays
// read-only; the CI step sets it.
func TestWriteOpenMetadataGoldenBodies(t *testing.T) {
	dir := os.Getenv("LEOFLOW_OM_GOLDEN")
	if dir == "" {
		t.Skip("gate skipped: set LEOFLOW_OM_GOLDEN to the directory to write")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	srv := omServer()
	write := func(file string, body []byte) {
		t.Helper()
		if !json.Valid(body) {
			t.Fatalf("%s is not valid JSON: %s", file, body)
		}
		if err := os.WriteFile(filepath.Join(dir, file), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// OM posts credentials to the BARE ROOT, not under /api: auth.py builds
	// f"{host}/auth/token" from hostPort directly. A Bearer is used for every
	// later call, so a change to this route or to the access_token field name
	// takes the whole integration down before the first DAG is read.
	tok := authGet(srv, http.MethodPost, "/auth/token", `{"username":"u","password":"p"}`)
	if tok.Code != http.StatusOK {
		t.Fatalf("POST /auth/token = %d (%s)", tok.Code, tok.Body.String())
	}
	write("token.json", tok.Body.Bytes())

	for _, c := range []struct{ file, path string }{
		// OM's ClientConfig sets api_version="api" and requests "/{version}/version",
		// so this is the URL it builds from a bare-root hostPort. Under
		// apiVersion: auto it is also the probe that picks v2 over v1.
		{"version.json", "/api/v2/version"},
		{"dags.json", "/api/v2/dags"},
		{"tasks.json", "/api/v2/dags/etl/tasks"},
		{"dag_runs.json", "/api/v2/dags/etl/dagRuns"},
		{"task_instances.json", "/api/v2/dags/etl/dagRuns/r1/taskInstances"},
	} {
		rec := authGet(srv, http.MethodGet, c.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d (%s)", c.path, rec.Code, rec.Body.String())
		}
		write(c.file, rec.Body.Bytes())
	}
}
