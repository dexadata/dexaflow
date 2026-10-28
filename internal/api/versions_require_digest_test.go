package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// deploy.require_digest (#1703) at the register, through the real routes.

const (
	pinnedDigestImage = "ghcr.io/acme/etl@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	taggedSpecJSON    = `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"ghcr.io/acme/etl:v1","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`
	pinnedSpecJSON    = `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"` + pinnedDigestImage + `","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`
)

// fakeUnpinned records each unpinned image counted, as stage/outcome.
type fakeUnpinned struct{ seen []string }

func (f *fakeUnpinned) RecordUnpinnedImage(stage, outcome string) {
	f.seen = append(f.seen, stage+"/"+outcome)
}

// digestServer is a register server under mode, logging to logs.
func digestServer(repo DagVersionRepository, mode domain.DigestRequirement, counts *fakeUnpinned, logs *bytes.Buffer) *gin.Engine {
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: slog.New(slog.NewJSONHandler(logs, nil)),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Execution: ExecutionDeps{
			RequireDigest:  mode,
			UnpinnedImages: counts,
		},
		Stores: StoreDeps{
			Versions: repo,
		},
	})
}

// Off (and unset, the zero value of an older wiring) registers a tag as
// before: no warning in the answer, nothing logged or counted.
func TestRequireDigestOffRegistersATag(t *testing.T) {
	for _, mode := range []domain.DigestRequirement{"", domain.DigestRequireOff} {
		repo, counts, logs := &fakeVersionRepo{created: true}, &fakeUnpinned{}, &bytes.Buffer{}
		rec := authGet(digestServer(repo, mode, counts, logs), http.MethodPost, "/api/v2/dags/etl/versions", taggedSpecJSON)
		if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "warnings") {
			t.Errorf("mode %q: register = %d %s, want 201 with no warnings", mode, rec.Code, rec.Body.String())
		}
		if len(counts.seen) != 0 || strings.Contains(logs.String(), "require_digest") {
			t.Errorf("mode %q: counted %v, logged %q; want nothing", mode, counts.seen, logs.String())
		}
	}
}

// Warn registers the tag, says so in the answer, logs and counts it, and
// tells the repository the mode so the audit entry records it.
func TestRequireDigestWarnRegistersATagWithAWarning(t *testing.T) {
	repo, counts, logs := &fakeVersionRepo{created: true}, &fakeUnpinned{}, &bytes.Buffer{}
	rec := authGet(digestServer(repo, domain.DigestRequireWarn, counts, logs), http.MethodPost, "/api/v2/dags/etl/versions", taggedSpecJSON)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d %s, want 201", rec.Code, rec.Body.String())
	}
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "deploy.require_digest") || !strings.Contains(resp.Warnings[0], "ghcr.io/acme/etl:v1") {
		t.Errorf("warnings = %q, want one naming deploy.require_digest and the image", resp.Warnings)
	}
	if !slices.Equal(counts.seen, []string{"register/warned"}) {
		t.Errorf("counted %v, want [register/warned]", counts.seen)
	}
	for _, want := range []string{`"setting":"deploy.require_digest"`, `"image":"ghcr.io/acme/etl:v1"`, `"dag":"etl"`, `"stage":"register"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log %q lacks %s", logs.String(), want)
		}
	}
	if !slices.Equal(repo.requireDigest, []domain.DigestRequirement{domain.DigestRequireWarn}) {
		t.Errorf("repository saw require_digest %v, want [warn]", repo.requireDigest)
	}
}

// Enforce refuses the tag with a 400 naming the setting and how to pin,
// counts it, and never reaches the repository.
func TestRequireDigestEnforceRefusesATag(t *testing.T) {
	repo, counts, logs := &fakeVersionRepo{created: true}, &fakeUnpinned{}, &bytes.Buffer{}
	rec := authGet(digestServer(repo, domain.DigestRequireEnforce, counts, logs), http.MethodPost, "/api/v2/dags/etl/versions", taggedSpecJSON)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("register = %d %s, want 400", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"deploy.require_digest=enforce", "ghcr.io/acme/etl:v1", "@sha256:", "dexaflow deploy"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("refusal %s lacks %q", rec.Body.String(), want)
		}
	}
	if len(repo.seen) != 0 {
		t.Errorf("the repository was asked to register %v", repo.seen)
	}
	if !slices.Equal(counts.seen, []string{"register/refused"}) {
		t.Errorf("counted %v, want [register/refused]", counts.seen)
	}
}

// A pinned image registers under every mode with nothing to say.
func TestRequireDigestPinnedImagePassesEveryMode(t *testing.T) {
	for _, mode := range []domain.DigestRequirement{domain.DigestRequireOff, domain.DigestRequireWarn, domain.DigestRequireEnforce} {
		repo, counts, logs := &fakeVersionRepo{created: true}, &fakeUnpinned{}, &bytes.Buffer{}
		rec := authGet(digestServer(repo, mode, counts, logs), http.MethodPost, "/api/v2/dags/etl/versions", pinnedSpecJSON)
		if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "warnings") || len(counts.seen) != 0 {
			t.Errorf("mode %q: register = %d %s, counted %v; want 201, no warning, nothing counted", mode, rec.Code, rec.Body.String(), counts.seen)
		}
	}
}

// The dry run gives the register's warning or refusal, and records nothing:
// no count, no log line.
func TestRequireDigestDryRunAnswersWithoutRecording(t *testing.T) {
	repo, counts, logs := &fakeVersionRepo{created: true}, &fakeUnpinned{}, &bytes.Buffer{}
	rec := authGet(digestServer(repo, domain.DigestRequireWarn, counts, logs), http.MethodPost, "/api/v2/dags/etl/versions/dry_run", taggedSpecJSON)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "deploy.require_digest=warn") {
		t.Errorf("dry run under warn = %d %s, want 200 with the warning", rec.Code, rec.Body.String())
	}
	rec = authGet(digestServer(repo, domain.DigestRequireEnforce, counts, logs), http.MethodPost, "/api/v2/dags/etl/versions/dry_run", taggedSpecJSON)
	if rec.Code != http.StatusBadRequest || rec.Header().Get(dryRunHeader) != "true" || !strings.Contains(rec.Body.String(), "deploy.require_digest=enforce") {
		t.Errorf("dry run under enforce = %d %v %s, want the 400 refusal with the dry run header", rec.Code, rec.Header(), rec.Body.String())
	}
	// The request log names the refusal's detail; the gate's own line, which
	// carries the setting as a field, must not appear.
	if len(counts.seen) != 0 || strings.Contains(logs.String(), `"setting":"deploy.require_digest"`) {
		t.Errorf("dry runs counted %v and logged %q; want nothing", counts.seen, logs.String())
	}
}

// Making a version current again applies the setting as a register would,
// like every other register check, so enforcing is not bypassed by a
// rollback to a version registered by tag before. The refusal names the way
// out of a rollback (warn, or a deploy of the sources), not the register's
// pinning steps; under warn the caller gets the warning, and the repository
// the mode for the audit entry.
func TestRequireDigestAppliesToSetCurrent(t *testing.T) {
	tagged := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "ghcr.io/acme/etl:v1",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: "python", Entrypoint: "dag:a"}},
	}
	repo := &fakeVersionRepo{change: rollbackChange(), specs: map[string]domain.DAGSpec{"v1": tagged}}
	counts, logs := &fakeUnpinned{}, &bytes.Buffer{}
	rec := authGet(digestServer(repo, domain.DigestRequireEnforce, counts, logs), http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v1"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "deploy.require_digest=warn") ||
		!strings.Contains(rec.Body.String(), "dexaflow deploy") || strings.Contains(rec.Body.String(), "compile --build --push") {
		t.Errorf("rollback to a tag under enforce = %d %s, want 400 naming warn and dexaflow deploy as the way out", rec.Code, rec.Body.String())
	}
	rec = authGet(digestServer(repo, domain.DigestRequireWarn, counts, logs), http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback to a tag under warn = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0], "deploy.require_digest=warn") || !strings.Contains(resp.Warnings[0], "ghcr.io/acme/etl:v1") {
		t.Errorf("rollback warnings = %q, want one naming the setting and the image", resp.Warnings)
	}
	if !slices.Equal(counts.seen, []string{"set_current/refused", "set_current/warned"}) {
		t.Errorf("counted %v, want [set_current/refused set_current/warned]", counts.seen)
	}
	if !slices.Equal(repo.setRequireDigest, []domain.DigestRequirement{domain.DigestRequireEnforce, domain.DigestRequireWarn}) {
		t.Errorf("repository saw require_digest %v, want [enforce warn]", repo.setRequireDigest)
	}
}

// A rollback under off answers as it always did: no warnings field.
func TestRequireDigestOffSetCurrentHasNoWarnings(t *testing.T) {
	tagged := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "ghcr.io/acme/etl:v1",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: "python", Entrypoint: "dag:a"}},
	}
	repo := &fakeVersionRepo{change: rollbackChange(), specs: map[string]domain.DAGSpec{"v1": tagged}}
	rec := authGet(digestServer(repo, domain.DigestRequireOff, &fakeUnpinned{}, &bytes.Buffer{}), http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v1"}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "warnings") {
		t.Errorf("rollback under off = %d %s, want 200 without warnings", rec.Code, rec.Body.String())
	}
}
