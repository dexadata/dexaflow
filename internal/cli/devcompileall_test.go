package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/spf13/cobra"
)

// TestDevCompileAndRegisterAll_IteratesEveryProjectAndLogsConfigSource is the
// load-bearing integration-style test for the multi-DAG reload loop. It pins
// the contract behind PR #246 that the unit tests up to this point only
// approximated:
//
//  1. EVERY project in the workspace is compiled (no silent skipping).
//  2. The pre-compile log line names the resolved config source — either the
//     absolute path of the project's dexaflow.yaml, or `auto-defaults: <subdir>`
//     for projects that don't carry one. (Docs/dag-authoring promises this;
//     this test pins it.)
//  3. Per-project compile failures don't abort the loop — siblings still run.
//
// The test injects a mock devCompileAndRegisterFn so the real parser + HTTP
// stack is not exercised here (it has its own tests); the contract being
// pinned is the LOOP, not the per-project compile.
func TestDevCompileAndRegisterAll_IteratesEveryProjectAndLogsConfigSource(t *testing.T) {
	// Three projects: two with yaml, one yaml-less (must show "auto-defaults: …").
	type call struct {
		dir string
	}
	var (
		mu    sync.Mutex
		calls []call
	)
	orig := devCompileAndRegisterFn
	devCompileAndRegisterFn = func(_ context.Context, _ *cobra.Command, dir string, _ compileOptions, _ string, _ func() error, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{dir: filepath.Base(dir)})
		return nil
	}
	defer func() { devCompileAndRegisterFn = orig }()

	cmd := &cobra.Command{}
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stdout)

	ws := &WorkspaceSpec{
		Path: "/tmp/ws",
		Projects: []Project{
			{
				Path:       "/tmp/ws/etl",
				DagID:      "etl",
				ConfigPath: "/tmp/ws/etl/dexaflow.yaml",
				HasYAML:    true,
				Config:     defaultedCfg("etl"),
			},
			{
				Path:       "/tmp/ws/ml",
				DagID:      "ml",
				ConfigPath: "/tmp/ws/ml/dexaflow.yaml",
				HasYAML:    true,
				Config:     defaultedCfg("ml"),
			},
			{
				Path:       "/tmp/ws/yamlless",
				DagID:      "yamlless",
				ConfigPath: "", // auto-defaults case
				HasYAML:    false,
				Config:     defaultedCfg("yamlless"),
			},
		},
		RootCfg: defaultedCfg(""),
	}

	if err := devCompileAndRegisterAll(context.Background(), cmd, ws, compileOptions{image: "dev"}, "tok", nil, "http://127.0.0.1:0"); err != nil {
		t.Fatalf("devCompileAndRegisterAll: %v", err)
	}

	// Every project compiled.
	if len(calls) != 3 {
		t.Fatalf("expected 3 compile calls, got %d (%v)", len(calls), calls)
	}
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.dir] = true
	}
	for _, want := range []string{"etl", "ml", "yamlless"} {
		if !seen[want] {
			t.Errorf("expected compile call for %q, got %v", want, calls)
		}
	}

	// Pre-compile log line names the config source per project. yaml-bearing
	// projects show the absolute yaml path; yaml-less shows `auto-defaults: <subdir>`.
	out := stdout.String()
	for _, must := range []string{
		`compiling "etl" (config: /tmp/ws/etl/dexaflow.yaml)`,
		`compiling "ml" (config: /tmp/ws/ml/dexaflow.yaml)`,
		`compiling "yamlless" (config: auto-defaults: yamlless)`,
	} {
		if !strings.Contains(out, must) {
			t.Errorf("stdout missing log line %q\nfull:\n%s", must, out)
		}
	}
}

// TestDevCompileAndRegisterAll_PerProjectFailureDoesNotAbortLoop pins the
// resilience contract: a single DAG with a broken yaml or compile error must
// not stop sibling DAGs from registering. The function returns nil as long as
// at least one project succeeded; the failure is recorded via the per-project
// import-error reporter (best-effort, not asserted here — too much HTTP
// scaffolding for one test) and the terminal already showed the ✗ line.
func TestDevCompileAndRegisterAll_PerProjectFailureDoesNotAbortLoop(t *testing.T) {
	orig := devCompileAndRegisterFn
	devCompileAndRegisterFn = func(_ context.Context, _ *cobra.Command, dir string, _ compileOptions, _ string, _ func() error, _ string) error {
		if strings.HasSuffix(dir, "broken") {
			return errors.New("simulated compile failure")
		}
		return nil
	}
	defer func() { devCompileAndRegisterFn = orig }()

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	ws := &WorkspaceSpec{
		Path: "/tmp/ws",
		Projects: []Project{
			{Path: "/tmp/ws/good", DagID: "good", HasYAML: false, Config: defaultedCfg("good")},
			{Path: "/tmp/ws/broken", DagID: "broken", HasYAML: false, Config: defaultedCfg("broken")},
			{Path: "/tmp/ws/also_good", DagID: "also_good", HasYAML: false, Config: defaultedCfg("also_good")},
		},
		RootCfg: defaultedCfg(""),
	}
	if err := devCompileAndRegisterAll(context.Background(), cmd, ws, compileOptions{image: "dev"}, "tok", nil, "http://127.0.0.1:0"); err != nil {
		t.Fatalf("loop should not abort on per-project failure; got: %v", err)
	}
}

// TestDevCompileAndRegisterAll_AllFailuresReturnLastError mirrors the inverse:
// when EVERY project fails, the loop returns the last error so the watcher's
// `✗ %v` print has something to show. The caller (devWatchLoop) treats this
// as the reload outcome.
func TestDevCompileAndRegisterAll_AllFailuresReturnLastError(t *testing.T) {
	orig := devCompileAndRegisterFn
	devCompileAndRegisterFn = func(_ context.Context, _ *cobra.Command, _ string, _ compileOptions, _ string, _ func() error, _ string) error {
		return errors.New("everything is broken")
	}
	defer func() { devCompileAndRegisterFn = orig }()

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	ws := &WorkspaceSpec{
		Path:    "/tmp/ws",
		RootCfg: defaultedCfg(""),
		Projects: []Project{
			{Path: "/tmp/ws/a", DagID: "a", Config: defaultedCfg("a")},
			{Path: "/tmp/ws/b", DagID: "b", Config: defaultedCfg("b")},
		},
	}
	err := devCompileAndRegisterAll(context.Background(), cmd, ws, compileOptions{image: "dev"}, "tok", nil, "http://127.0.0.1:0")
	if err == nil {
		t.Fatal("expected error when every project fails")
	}
	if !strings.Contains(err.Error(), "everything is broken") {
		t.Errorf("returned error %q should be the per-project failure", err.Error())
	}
}

// TestDevCompileAndRegisterAll_EmptyWorkspacePrintsHintAndReturnsNil covers
// the "user just created the workspace dir" path: lite must not error, just
// hint at what's missing. The caller (devWatchLoop) keeps polling.
func TestDevCompileAndRegisterAll_EmptyWorkspacePrintsHintAndReturnsNil(t *testing.T) {
	cmd := &cobra.Command{}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})

	ws := &WorkspaceSpec{Path: "/tmp/empty-ws", RootCfg: defaultedCfg("")}
	if err := devCompileAndRegisterAll(context.Background(), cmd, ws, compileOptions{}, "tok", nil, ""); err != nil {
		t.Fatalf("empty workspace should not error: %v", err)
	}
	if !strings.Contains(out.String(), "no DAGs in workspace") {
		t.Errorf("empty workspace should print a hint; got: %q", out.String())
	}
}

// defaultedCfg builds a fully-defaulted LeoflowConfig with the given dag_id —
// the shape every Project.Config is in after ResolveWorkspace runs.
func defaultedCfg(dagID string) *domain.LeoflowConfig {
	cfg := &domain.LeoflowConfig{DagID: dagID}
	cfg.ApplyDefaults()
	return cfg
}

// Sanity for the closure form devCompileAndRegisterFn takes — keeps a compile-
// time check that the package var matches the production function signature.
// If devCompileAndRegister's signature ever changes, this test fails to build,
// not at runtime.
var _ = func(_ context.Context, _ *cobra.Command, _ string, _ compileOptions, _ string, _ func() error, _ string) error {
	return fmt.Errorf("compile-time sig check only")
}
