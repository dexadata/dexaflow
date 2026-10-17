package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/dexadata/dexaflow/internal/build"
)

// promoteDigest is the digest the fake source's versions run.
var promoteDigest = "sha256:" + strings.Repeat("ab", 32)

// promoteSource is a fake source control plane: etl with versions 1..3,
// version 3 current, and the spec registered with each.
type promoteSource struct {
	mu        sync.Mutex
	image     string // the image every version runs
	auth      []string
	specReads []string // the version_number of each spec read
	// current serves the current spec whatever version is asked for, as a
	// broken or hostile source could.
	current bool
	// missing answers the spec route 404, as a server before it does.
	missing bool
	// forbidSpec answers the spec route 403, as for a deploy-only token.
	forbidSpec bool
}

func (s *promoteSource) spec(n int) map[string]any {
	label := []string{"", "v1.0.0-aaa", "v1.1.0-bbb", "v1.2.0-ccc"}[n]
	return map[string]any{
		"schema_version": "1.0", "dag_id": "etl", "dag_version": label, "image": s.image,
		"schedule": "@daily", "max_active_runs": float64(n), "description": "etl " + label,
		"tasks": []any{map[string]any{"task_id": "t" + label[:4], "type": "python", "entrypoint": "dag:t"}},
	}
}

func (s *promoteSource) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/dags/etl/dagVersions":
		var versions []map[string]any
		for n := 3; n >= 1; n-- {
			sp := s.spec(n)
			versions = append(versions, map[string]any{"id": sp["dag_version"], "version_number": n, "dag_id": "etl",
				"bundle_version": sp["dag_version"], "image": s.image, "is_current": n == 3,
				"build_info": map[string]any{"commit": fmt.Sprintf("abcdef%d", n), "pipeline_url": "https://ci.example/run/" + fmt.Sprint(n)}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"dag_versions": versions, "total_entries": len(versions)})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/dags/etl/dagVersions/") && strings.HasSuffix(r.URL.Path, "/spec"):
		n := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v2/dags/etl/dagVersions/"), "/spec")
		if s.forbidSpec {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"detail":"missing scope dexaflow:read"}`))
			return
		}
		s.specReads = append(s.specReads, n)
		num := map[string]int{"1": 1, "2": 2, "3": 3}[n]
		if s.current {
			num = 3
		}
		if s.missing {
			num = 0
		}
		if num == 0 {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(s.spec(num))
	default:
		http.NotFound(w, r)
	}
}

// promoteDest is a fake destination control plane that records each
// register and dry run.
type promoteDest struct {
	mu       sync.Mutex
	registry string // deploy.defaults.registry it advertises; "" for none
	dryCode  int    // the dry run's status; 0 is 200
	// dryBare answers the dry run with this status and no dry-run header,
	// as an older server (404) or a proxy in front of it does; 0 is off.
	dryBare int
	// dryUnchanged makes the dry run answer created false: the destination
	// already holds this content.
	dryUnchanged bool
	registers    []promotePost
	dryRuns      []promotePost
	auth         []string
}

// promotePost is one POST the destination received.
type promotePost struct {
	query queryMap
	spec  map[string]any
}

type queryMap = map[string]string

func (d *promoteDest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.auth = append(d.auth, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/deployDefaults":
		if d.registry == "" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"registry": d.registry})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v2/dags/etl/versions"):
		body, _ := io.ReadAll(r.Body)
		p := promotePost{query: queryMap{}}
		_ = json.Unmarshal(body, &p.spec)
		for k := range r.URL.Query() {
			p.query[k] = r.URL.Query().Get(k)
		}
		if strings.HasSuffix(r.URL.Path, "/dry_run") {
			d.dryRuns = append(d.dryRuns, p)
			if d.dryBare != 0 {
				w.WriteHeader(d.dryBare)
				return
			}
			w.Header().Set("Dexaflow-Dry-Run", "true")
			if d.dryCode != 0 {
				w.WriteHeader(d.dryCode)
				_, _ = w.Write([]byte(`{"detail":"image not in the allowlist"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"dag_id": "etl", "created": !d.dryUnchanged, "dag_created": !d.dryUnchanged, "is_paused": true, "dry_run": true})
			return
		}
		d.registers = append(d.registers, p)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"dag_id": "etl", "version": p.spec["dag_version"], "spec_hash": "h",
			"created": true, "dag_created": true, "is_paused": true})
	default:
		http.NotFound(w, r)
	}
}

// promoteEnv is a workspace with targets dev (the source) and prod (the
// destination), each with a session saved for its server.
type promoteEnv struct {
	src     *promoteSource
	dst     *promoteDest
	srcURL  string
	dstURL  string
	cfgPath string
}

// newPromoteEnv starts both control planes and writes the workspace and the
// config file. devRegistry and prodRegistry are the targets' registries
// ("" for none).
func newPromoteEnv(t *testing.T, image, devRegistry, prodRegistry string) *promoteEnv {
	t.Helper()
	isolateCLIEnv(t)
	e := &promoteEnv{src: &promoteSource{image: image}, dst: &promoteDest{}}
	srcSrv, dstSrv := httptest.NewServer(e.src), httptest.NewServer(e.dst)
	t.Cleanup(srcSrv.Close)
	t.Cleanup(dstSrv.Close)
	e.srcURL, e.dstURL = srcSrv.URL, dstSrv.URL
	root := t.TempDir()
	ws := "targets:\n  dev:\n    server: " + e.srcURL + "\n"
	if devRegistry != "" {
		ws += "    registry: " + devRegistry + "\n"
	}
	ws += "  prod:\n    server: " + e.dstURL + "\n"
	if prodRegistry != "" {
		ws += "    registry: " + prodRegistry + "\n"
	}
	writeTree(t, filepath.Join(root, workspaceFileName), ws+"default_target: dev\n")
	t.Chdir(root)
	e.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, e.cfgPath, "sessions:\n  - server: "+e.srcURL+"\n    token: dev-tok\n  - server: "+e.dstURL+"\n    token: prod-tok\n")
	return e
}

// promote runs `dexaflow promote etl` with args and the env's config file.
func (e *promoteEnv) promote(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return run(t, append([]string{"promote", "etl", "--config", e.cfgPath}, args...)...)
}

func TestPromoteRegistersTheCurrentSourceVersionOnTheDestination(t *testing.T) {
	image := "registry.example/team/etl@" + promoteDigest
	e := newPromoteEnv(t, image, "", "")
	t.Setenv("DEXAFLOW_TOKEN", "env-tok")
	out, stderr, err := e.promote(t, "--from", "dev", "--to", "prod")
	if err != nil {
		t.Fatalf("promote: %v\n%s\n%s", err, out, stderr)
	}
	if len(e.dst.registers) != 1 {
		t.Fatalf("registers = %d, want 1", len(e.dst.registers))
	}
	got := e.dst.registers[0]
	want := e.src.spec(3)
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got.spec)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("registered spec = %s, want the source's current version as registered: %s", gotJSON, wantJSON)
	}
	if got.query["promoted_from"] != "dev" || got.query["promoted_from_server"] != e.srcURL {
		t.Errorf("register query = %v, want promoted_from dev and promoted_from_server %s", got.query, e.srcURL)
	}
	if _, ok := got.query["is_paused"]; ok {
		t.Errorf("register query = %v: a promote must not set is_paused", got.query)
	}
	if got.query["build_commit"] != "abcdef3" || got.query["build_pipeline_url"] != "https://ci.example/run/3" {
		t.Errorf("register query = %v, want the source version's build info", got.query)
	}
	if len(e.dst.dryRuns) != 1 || e.dst.dryRuns[0].query["promoted_from"] != "dev" {
		t.Errorf("dry runs = %+v, want one before the register", e.dst.dryRuns)
	}
	for _, a := range e.src.auth {
		if a != "Bearer dev-tok" {
			t.Errorf("source got Authorization %q, want its own session (dev-tok)", a)
		}
	}
	for _, a := range e.dst.auth {
		if a != "Bearer prod-tok" {
			t.Errorf("destination got Authorization %q, want its own session (prod-tok)", a)
		}
	}
	for _, want := range []string{"dev", "prod", e.srcURL, e.dstURL, "v1.2.0-ccc", image} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
}

func TestPromoteVersionPicksALabelOrANumber(t *testing.T) {
	for version, want := range map[string]string{"v1.0.0-aaa": "1", "2": "2"} {
		e := newPromoteEnv(t, "registry.example/team/etl@"+promoteDigest, "", "")
		if _, _, err := e.promote(t, "--from", "dev", "--to", "prod", "--version", version); err != nil {
			t.Fatalf("promote --version %s: %v", version, err)
		}
		if len(e.src.specReads) != 1 || e.src.specReads[0] != want {
			t.Errorf("--version %s read spec version_number %v, want %s", version, e.src.specReads, want)
		}
		if len(e.dst.registers) != 1 || e.dst.registers[0].spec["max_active_runs"] != float64(map[string]int{"1": 1, "2": 2}[want]) {
			t.Errorf("--version %s registered %+v", version, e.dst.registers)
		}
	}
	e := newPromoteEnv(t, "registry.example/team/etl@"+promoteDigest, "", "")
	if _, _, err := e.promote(t, "--from", "dev", "--to", "prod", "--version", "v9"); err == nil || !strings.Contains(err.Error(), "dags versions etl") {
		t.Errorf("unknown --version: err = %v, want one naming `dexaflow dags versions`", err)
	}
}

func TestPromoteRefusesAnImageNotPinnedByDigest(t *testing.T) {
	e := newPromoteEnv(t, "registry.example/team/etl:v1.2.0", "", "")
	_, _, err := e.promote(t, "--from", "dev", "--to", "prod")
	if err == nil || !strings.Contains(err.Error(), "not pinned by digest") {
		t.Fatalf("err = %v, want a refusal of an image not pinned by digest", err)
	}
	if len(e.dst.registers)+len(e.dst.dryRuns) != 0 {
		t.Errorf("the destination was sent %d registers and %d dry runs", len(e.dst.registers), len(e.dst.dryRuns))
	}
}

func TestPromoteRefusesBadTargets(t *testing.T) {
	e := newPromoteEnv(t, "registry.example/team/etl@"+promoteDigest, "", "")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--from", "dev", "--to", "dev"}, "same"},
		{[]string{"--from", "dev", "--to", "qa"}, "known targets: dev, prod"},
		{[]string{"--to", "prod"}, "--from"},
		{[]string{"--from", "dev"}, "--to"},
		{[]string{"--from", "dev", "--to", "prod", "--target", "prod"}, "--target does not apply"},
	} {
		if _, _, err := e.promote(t, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("promote %v: err = %v, want one containing %q", tc.args, err, tc.want)
		}
	}
	if len(e.dst.registers) != 0 {
		t.Errorf("a refused promote registered %d versions", len(e.dst.registers))
	}
}

func TestPromoteStopsWhenTheDestinationDryRunRefuses(t *testing.T) {
	e := newPromoteEnv(t, "registry.example/team/etl@"+promoteDigest, "", "")
	e.dst.dryCode = http.StatusBadRequest
	_, _, err := e.promote(t, "--from", "dev", "--to", "prod")
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("err = %v, want the destination's refusal", err)
	}
	if len(e.dst.registers) != 0 {
		t.Errorf("a refused dry run still registered")
	}
}

func TestPromoteTokenFlags(t *testing.T) {
	e := newPromoteEnv(t, "registry.example/team/etl@"+promoteDigest, "", "")
	if _, _, err := e.promote(t, "--from", "dev", "--to", "prod", "--from-token", "a", "--to-token", "b"); err != nil {
		t.Fatal(err)
	}
	for _, a := range e.src.auth {
		if a != "Bearer a" {
			t.Errorf("source got Authorization %q, want --from-token's", a)
		}
	}
	for _, a := range e.dst.auth {
		if a != "Bearer b" {
			t.Errorf("destination got Authorization %q, want --to-token's", a)
		}
	}
	if len(e.src.auth) == 0 || len(e.dst.auth) == 0 {
		t.Error("a control plane was not called")
	}
}

// ociRegistry starts an in-memory registry and returns its host:port.
func ociRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// pushRandom pushes a random image under ref and returns its digest.
func pushRandom(t *testing.T, ref string) string {
	t.Helper()
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if werr := remote.Write(r, img); werr != nil {
		t.Fatal(werr)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

func TestPromoteCopiesTheImageToTheDestinationRegistry(t *testing.T) {
	devReg, prodReg := ociRegistry(t), ociRegistry(t)
	digest := pushRandom(t, devReg+"/dev/etl:v1.2.0-ccc")
	e := newPromoteEnv(t, devReg+"/dev/etl@"+digest, devReg+"/dev", prodReg+"/prod")
	out, _, err := e.promote(t, "--from", "dev", "--to", "prod")
	if err != nil {
		t.Fatalf("promote: %v\n%s", err, out)
	}
	// The source repository path (dev/etl) is kept under the destination
	// registry, so team-a/etl and team-b/etl never land on one repository.
	want := prodReg + "/prod/dev/etl@" + digest
	if len(e.dst.registers) != 1 || e.dst.registers[0].spec["image"] != want {
		t.Fatalf("registered %+v, want image %s", e.dst.registers, want)
	}
	pinned, err := build.Registry{}.Resolve(context.Background(), prodReg+"/prod/dev/etl:v1.2.0-ccc")
	if err != nil || pinned.Ref != want {
		t.Errorf("destination registry holds %q (%v), want %s", pinned.Ref, err, want)
	}
	reg := e.dst.registers[0].spec
	src := e.src.spec(3)
	delete(reg, "image")
	delete(src, "image")
	if a, b := mustJSON(t, reg), mustJSON(t, src); a != b {
		t.Errorf("only the image may change: registered %s, source %s", a, b)
	}
	if !strings.Contains(out, "copied") {
		t.Errorf("output does not say the image was copied:\n%s", out)
	}
}

func TestPromoteReadsTheDestinationRegistryFromItsCluster(t *testing.T) {
	devReg, prodReg := ociRegistry(t), ociRegistry(t)
	digest := pushRandom(t, devReg+"/dev/etl:v1.2.0-ccc")
	e := newPromoteEnv(t, devReg+"/dev/etl@"+digest, devReg+"/dev", "")
	e.dst.registry = prodReg + "/cluster"
	if _, _, err := e.promote(t, "--from", "dev", "--to", "prod"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if want := prodReg + "/cluster/dev/etl@" + digest; len(e.dst.registers) != 1 || e.dst.registers[0].spec["image"] != want {
		t.Errorf("registered %+v, want image %s", e.dst.registers, want)
	}
}

func TestPromoteReusesAnImageAlreadyInTheDestinationRegistry(t *testing.T) {
	image := "ghcr.io/acme/etl@" + promoteDigest
	e := newPromoteEnv(t, image, "ghcr.io/acme", "ghcr.io/acme")
	out, _, err := e.promote(t, "--from", "dev", "--to", "prod")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(e.dst.registers) != 1 || e.dst.registers[0].spec["image"] != image {
		t.Errorf("registered %+v, want the same image %s", e.dst.registers, image)
	}
	if strings.Contains(out, "copied") {
		t.Errorf("output says the image was copied:\n%s", out)
	}
}

func TestPromoteDryRunRegistersAndCopiesNothing(t *testing.T) {
	devReg, prodReg := ociRegistry(t), ociRegistry(t)
	digest := pushRandom(t, devReg+"/dev/etl:v1.2.0-ccc")
	e := newPromoteEnv(t, devReg+"/dev/etl@"+digest, devReg+"/dev", prodReg+"/prod")
	out, _, err := e.promote(t, "--from", "dev", "--to", "prod", "--dry-run")
	if err != nil {
		t.Fatalf("promote --dry-run: %v", err)
	}
	if len(e.dst.registers) != 0 {
		t.Errorf("--dry-run registered %d versions", len(e.dst.registers))
	}
	if len(e.dst.dryRuns) != 1 || e.dst.dryRuns[0].spec["image"] != prodReg+"/prod/dev/etl@"+digest {
		t.Errorf("dry runs = %+v, want one with the destination image", e.dst.dryRuns)
	}
	if _, rerr := (build.Registry{}).Resolve(context.Background(), prodReg+"/prod/dev/etl@"+digest); rerr == nil {
		t.Error("--dry-run copied the image")
	}
	if !strings.Contains(out, "would copy") || !strings.Contains(out, "would register") {
		t.Errorf("output does not say what would happen:\n%s", out)
	}
}

func TestPromoteOutputJSON(t *testing.T) {
	image := "registry.example/team/etl@" + promoteDigest
	e := newPromoteEnv(t, image, "", "")
	out, stderr, err := e.promote(t, "--from", "dev", "--to", "prod", "--output", "json")
	if err != nil {
		t.Fatalf("promote: %v\n%s", err, stderr)
	}
	var doc struct {
		DagID string `json:"dag_id"`
		From  struct {
			Target, Server, Version, Image string
			VersionNumber                  int `json:"version_number"`
		} `json:"from"`
		To struct {
			Target, Server, Image string
		} `json:"to"`
		Copied     bool   `json:"copied"`
		Status     string `json:"status"`
		DagCreated bool   `json:"dag_created"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", jerr, out)
	}
	if doc.DagID != "etl" || doc.From.Target != "dev" || doc.From.Server != e.srcURL || doc.From.Version != "v1.2.0-ccc" ||
		doc.From.VersionNumber != 3 || doc.From.Image != image || doc.To.Target != "prod" || doc.To.Server != e.dstURL ||
		doc.To.Image != image || doc.Copied || doc.Status != "registered" || !doc.DagCreated {
		t.Errorf("document = %+v", doc)
	}
	if !strings.Contains(stderr, "prod") {
		t.Errorf("the human lines did not go to stderr: %q", stderr)
	}
}

// mustJSON encodes v.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A source that serves another version's spec than the one asked for would
// promote other content: refused before the destination sees anything.
func TestPromoteRefusesASourceThatServesAnotherVersion(t *testing.T) {
	e := newPromoteEnv(t, "registry.example/team/etl@"+promoteDigest, "", "")
	e.src.current = true
	_, _, err := e.promote(t, "--from", "dev", "--to", "prod", "--version", "1")
	if err == nil || !strings.Contains(err.Error(), "not that version's") {
		t.Fatalf("err = %v, want a refusal of the wrong spec", err)
	}
	if len(e.dst.registers)+len(e.dst.dryRuns) != 0 {
		t.Errorf("the destination was sent the wrong spec")
	}
}

// A source that does not serve a version's spec (an older server) is named
// as such.
func TestPromoteNamesASourceWithoutTheSpecRoute(t *testing.T) {
	e := newPromoteEnv(t, "registry.example/team/etl@"+promoteDigest, "", "")
	_, _, err := e.promote(t, "--from", "dev", "--to", "prod", "--version", "7")
	if err == nil {
		t.Fatal("want an error for an unknown version")
	}
	e.src.missing = true
	_, _, err = e.promote(t, "--from", "dev", "--to", "prod")
	if err == nil || !strings.Contains(err.Error(), "predate") {
		t.Fatalf("err = %v, want one saying the source may predate the route", err)
	}
}

// Reading a version's spec needs read on the source (a deploy-only token
// cannot): a 403 there says which scope the --from token needs.
func TestPromoteNamesTheReadScopeTheSourceNeeds(t *testing.T) {
	e := newPromoteEnv(t, "registry.example/team/etl@"+promoteDigest, "", "")
	e.src.forbidSpec = true
	_, _, err := e.promote(t, "--from", "dev", "--to", "prod")
	if err == nil || !strings.Contains(err.Error(), "--from-token") || !strings.Contains(err.Error(), "dexaflow:read") {
		t.Fatalf("err = %v, want one naming the read scope and --from-token", err)
	}
	if len(e.dst.registers)+len(e.dst.dryRuns) != 0 {
		t.Error("the destination was called")
	}
}

// Only an older server's 404 or 405 lets the promote go on without a dry
// run; any other answer without the dry-run header (a proxy's 502, a 500)
// stops it before the copy and the register.
func TestPromoteDryRunFailsClosed(t *testing.T) {
	for code, wantRegister := range map[int]bool{
		http.StatusNotFound: true, http.StatusMethodNotAllowed: true,
		http.StatusBadGateway: false, http.StatusInternalServerError: false, http.StatusTeapot: false,
	} {
		e := newPromoteEnv(t, "registry.example/team/etl@"+promoteDigest, "", "")
		e.dst.dryBare = code
		_, _, err := e.promote(t, "--from", "dev", "--to", "prod")
		if wantRegister && (err != nil || len(e.dst.registers) != 1) {
			t.Errorf("dry run %d: err = %v, registers = %d; want the register to go on", code, err, len(e.dst.registers))
		}
		if !wantRegister && (err == nil || !strings.Contains(err.Error(), fmt.Sprint(code)) || len(e.dst.registers) != 0) {
			t.Errorf("dry run %d: err = %v, registers = %d; want a stop naming the status", code, err, len(e.dst.registers))
		}
	}
}

// Content the destination already holds (the dry run says created false) is
// not copied again; the register reports it unchanged.
func TestPromoteSkipsTheCopyOfContentTheDestinationHolds(t *testing.T) {
	devReg, prodReg := ociRegistry(t), ociRegistry(t)
	digest := pushRandom(t, devReg+"/dev/etl:v1.2.0-ccc")
	e := newPromoteEnv(t, devReg+"/dev/etl@"+digest, devReg+"/dev", prodReg+"/prod")
	e.dst.dryUnchanged = true
	if _, _, err := e.promote(t, "--from", "dev", "--to", "prod"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, rerr := (build.Registry{}).Resolve(context.Background(), prodReg+"/prod/dev/etl@"+digest); rerr == nil {
		t.Error("the image was copied although the destination holds the content")
	}
}

// A copy that fails names the command that signs in to each registry.
func TestPromoteCopyFailureNamesTheLogins(t *testing.T) {
	devReg := ociRegistry(t)
	digest := pushRandom(t, devReg+"/dev/etl:v1.2.0-ccc")
	e := newPromoteEnv(t, devReg+"/dev/etl@"+digest, devReg+"/dev", "127.0.0.1:1/prod")
	_, _, err := e.promote(t, "--from", "dev", "--to", "prod")
	if err == nil || !strings.Contains(err.Error(), "dexaflow registry login 127.0.0.1:1") || !strings.Contains(err.Error(), "dexaflow registry login "+devReg) {
		t.Fatalf("err = %v, want the login command of both registries", err)
	}
	if len(e.dst.registers) != 0 {
		t.Error("registered after a failed copy")
	}
}
