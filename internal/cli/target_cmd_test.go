package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// emptyConfig returns the path of an empty config file in a fresh directory.
func emptyConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, p, "")
	return p
}

func TestTargetAddWritesTheWorkspaceFile(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	t.Chdir(filepath.Join(root, "dags"))
	cfg := emptyConfig(t)
	out, _, err := run(t, "target", "add", "staging", "https://stg.example", "--registry", "ghcr.io/acme/stg", "--config", cfg)
	if err != nil {
		t.Fatalf("target add: %v", err)
	}
	if !strings.Contains(out, "staging") || !strings.Contains(out, "auth login --target staging") {
		t.Errorf("output = %q, want the target and the sign-in step", out)
	}
	ws, err := findWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := ws.Spec.Targets["staging"]; got == nil || got.Server != "https://stg.example" || got.Registry != "ghcr.io/acme/stg" {
		t.Errorf("staging = %+v", got)
	}
	if ws.Spec.DefaultTarget != "dev" {
		t.Errorf("default_target = %q; adding a target must not move an existing default", ws.Spec.DefaultTarget)
	}
	if _, _, err := run(t, "target", "add", "staging", "https://other.example", "--config", cfg); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("re-adding without --force: err = %v, want a refusal naming --force", err)
	}
	if _, _, err := run(t, "target", "add", "staging", "https://other.example", "--force", "--config", cfg); err != nil {
		t.Errorf("re-adding with --force: %v", err)
	}
}

func TestTargetAddRejectsBadInput(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(t.TempDir())
	cfg := emptyConfig(t)
	for _, args := range [][]string{{"Dev Box", "https://d"}, {"dev", "d.example"}} {
		if _, _, err := run(t, append([]string{"target", "add"}, append(args, "--config", cfg)...)...); err == nil {
			t.Errorf("target add %v: want an error", args)
		}
	}
}

func TestFirstWorkspaceTargetBecomesTheDefault(t *testing.T) {
	isolateCLIEnv(t)
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), workspaceFileTemplate)
	t.Chdir(root)
	if _, _, err := run(t, "target", "add", "dev", "https://dev.example", "--config", emptyConfig(t)); err != nil {
		t.Fatalf("target add: %v", err)
	}
	ws, _ := findWorkspace(root)
	if ws.Spec.DefaultTarget != "dev" {
		t.Errorf("default_target = %q, want dev", ws.Spec.DefaultTarget)
	}
	data, _ := os.ReadFile(filepath.Join(root, workspaceFileName))
	if !strings.Contains(string(data), "holds no credentials") {
		t.Errorf("the template's comments were lost:\n%s", data)
	}
}

func TestTargetAddOutsideAWorkspaceIsAUserTarget(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(t.TempDir())
	cfg := emptyConfig(t)
	if _, _, err := run(t, "target", "add", "lab", "https://lab.example", "--config", cfg); err != nil {
		t.Fatalf("target add: %v", err)
	}
	s, _ := config.ReadUserStore(cfg)
	if tgt, ok := s.Target("lab"); !ok || tgt.Server != "https://lab.example" || s.CurrentTarget != "lab" {
		t.Errorf("store = %+v, want lab as the current user target", s)
	}
}

func TestTargetAddGlobalInsideAWorkspace(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	t.Chdir(root)
	cfg := emptyConfig(t)
	if _, _, err := run(t, "target", "add", "mine", "https://mine.example", "--global", "--config", cfg); err != nil {
		t.Fatalf("target add --global: %v", err)
	}
	if ws, _ := findWorkspace(root); ws.Spec.Targets["mine"] != nil {
		t.Error("--global must not write the shared workspace file")
	}
	if s, _ := config.ReadUserStore(cfg); len(s.Targets) != 1 {
		t.Errorf("user targets = %+v", s.Targets)
	}
}

func TestTargetUseInAWorkspaceIsPersonal(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	t.Chdir(root)
	cfg := emptyConfig(t)
	before, _ := os.ReadFile(filepath.Join(root, workspaceFileName))
	if _, _, err := run(t, "target", "use", "prod", "--config", cfg); err != nil {
		t.Fatalf("target use: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, workspaceFileName))
	if !bytes.Equal(before, after) {
		t.Error("target use must not edit the shared workspace file")
	}
	if server, _, err := resolveVia(t, "dags", "list", "--config", cfg); err != nil || server != "https://prod.example" {
		t.Errorf("after target use prod: server = %q, err = %v", server, err)
	}
	if _, _, err := run(t, "target", "use", "nope", "--config", cfg); err == nil {
		t.Error("using an unknown target must fail")
	}
	if _, _, err := run(t, "target", "use", "prod", "--workspace-default", "--config", cfg); err != nil {
		t.Fatalf("target use --workspace-default: %v", err)
	}
	if ws, _ := findWorkspace(root); ws.Spec.DefaultTarget != "prod" {
		t.Errorf("default_target = %q, want prod", ws.Spec.DefaultTarget)
	}
}

func TestTargetListShowsTheActiveTarget(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(targetWorkspace(t, "https://dev.example", "https://prod.example"))
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "sessions:\n  - {server: https://prod.example, token: t}\n")
	out, _, err := run(t, "target", "list", "--config", cfg)
	if err != nil {
		t.Fatalf("target list: %v", err)
	}
	for _, want := range []string{"* dev", "https://dev.example", "ghcr.io/acme/dev", "prod", "workspace", "default_target in"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	prodLine := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "https://prod.example") {
			prodLine = l
		}
	}
	if !strings.Contains(prodLine, "yes") {
		t.Errorf("prod has a saved session; its line = %q", prodLine)
	}
}

func TestTargetRemoveClearsTheDefault(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	t.Chdir(root)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "sessions:\n  - {server: https://dev.example, token: t}\n")
	if _, _, err := run(t, "target", "remove", "dev", "--logout", "--config", cfg); err != nil {
		t.Fatalf("target remove: %v", err)
	}
	ws, err := findWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Spec.Targets["dev"] != nil || ws.Spec.DefaultTarget != "" {
		t.Errorf("spec = %+v, want dev and default_target gone", ws.Spec)
	}
	if s, _ := config.ReadUserStore(cfg); len(s.Sessions) != 0 {
		t.Errorf("--logout must forget the session, got %+v", s.Sessions)
	}
	if _, _, err := run(t, "target", "remove", "dev", "--config", cfg); err == nil {
		t.Error("removing a missing target must fail")
	}
}

func TestLoginToATargetSavesItsOwnSession(t *testing.T) {
	isolateCLIEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"dev-tok"}`))
	}))
	defer srv.Close()
	t.Chdir(targetWorkspace(t, srv.URL, "https://prod.example"))
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "server_url: https://prod.example\ntoken: prod-legacy\n")
	out, _, err := run(t, "auth", "login", "--target", "dev", "--username", "a@b.c", "--password", "pw", "--config", cfg)
	if err != nil {
		t.Fatalf("auth login: %v", err)
	}
	if !strings.Contains(out, "target dev") {
		t.Errorf("output = %q, want the target named", out)
	}
	if !strings.HasPrefix(out, "Logging in to "+srv.URL+" (target dev, from --target)") {
		t.Errorf("output = %q: the server and what chose it come first, before any password is read", out)
	}
	s, _ := config.ReadUserStore(cfg)
	if tok, ok := s.Session(srv.URL); !ok || tok != "dev-tok" {
		t.Errorf("session for dev = %q, %v", tok, ok)
	}
	if s.ServerURL != "https://prod.example" || s.Token != "prod-legacy" {
		t.Errorf("a target login must leave the single-server session alone, got %q %q", s.ServerURL, s.Token)
	}
}

func TestTargetUseSurvivesASymlinkedPath(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := emptyConfig(t)
	t.Chdir(link)
	if _, _, err := run(t, "target", "use", "prod", "--config", cfg); err != nil {
		t.Fatalf("target use: %v", err)
	}
	t.Chdir(root)
	if server, _, err := resolveVia(t, "dags", "list", "--config", cfg); err != nil || server != "https://prod.example" {
		t.Errorf("through the real path: server = %q, err = %v; the choice made through the link must hold", server, err)
	}
}

// TestTargetAddKeepsAnExistingServerURL: a user who works against server_url
// today is not moved to a new target by adding one.
func TestTargetAddKeepsAnExistingServerURL(t *testing.T) {
	isolateCLIEnv(t)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "server_url: https://pro.example\n")
	out, _, err := run(t, "target", "add", "lab", "https://lab.example", "--config", cfg)
	if err != nil {
		t.Fatalf("target add: %v", err)
	}
	if s, _ := config.ReadUserStore(cfg); s.CurrentTarget != "" {
		t.Errorf("current_target = %q, want none while server_url is set", s.CurrentTarget)
	}
	if !strings.Contains(out, "dexaflow target use lab") {
		t.Errorf("output must say how to select it:\n%s", out)
	}

	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), workspaceFileTemplate)
	t.Chdir(root)
	out, _, err = run(t, "target", "add", "dev", "https://dev.example", "--config", cfg)
	if err != nil {
		t.Fatalf("target add in a workspace: %v", err)
	}
	// The shared file must not depend on one author's personal config: a
	// workspace always gets its default target.
	if ws, _ := findWorkspace(root); ws.Spec.DefaultTarget != "dev" {
		t.Errorf("default_target = %q, want dev whatever the author's config holds", ws.Spec.DefaultTarget)
	}
	if !strings.Contains(out, "instead of server_url (https://pro.example)") {
		t.Errorf("the author must be told the workspace now overrides their server_url:\n%s", out)
	}
}

func TestTargetRemoveLogoutClearsTheMatchingLegacySession(t *testing.T) {
	isolateCLIEnv(t)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "server_url: https://lab.example/\ntoken: old\ntargets:\n  - {name: lab, server: https://lab.example}\n")
	if _, _, err := run(t, "target", "remove", "lab", "--logout", "--config", cfg); err != nil {
		t.Fatalf("target remove: %v", err)
	}
	if s, _ := config.ReadUserStore(cfg); s.Token != "" || s.ServerURL != "" {
		t.Errorf("legacy pair = %q %q, want it forgotten with the target's server", s.ServerURL, s.Token)
	}
}
