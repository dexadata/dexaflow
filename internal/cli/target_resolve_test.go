package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// isolateCLIEnv clears every variable that selects a server, a target or a
// token, and points HOME at an empty directory, so a developer's own
// environment cannot decide what a test resolves.
func isolateCLIEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"TOKEN", "TARGET", "SERVER_URL", "REGISTRY", "TRUSTED_WORKSPACES"} {
		t.Setenv("DEXAFLOW_"+k, "")
		t.Setenv("LEOFLOW_"+k, "")
	}
	t.Setenv("HOME", t.TempDir())
	// Never the repository: the git-root fallback would otherwise make a
	// command see every DAG of this checkout, and compile into it.
	t.Chdir(t.TempDir())
}

// targetWorkspace writes a workspace with two targets, dev (the default) and
// prod, and returns its root.
func targetWorkspace(t *testing.T, devURL, prodURL string) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), "targets:\n"+
		"  dev:\n    server: "+devURL+"\n    registry: ghcr.io/acme/dev\n"+
		"  prod:\n    server: "+prodURL+"\n"+
		"default_target: dev\n")
	if err := os.MkdirAll(filepath.Join(root, "dags", "orders"), 0o750); err != nil {
		t.Fatal(err)
	}
	return root
}

// resolveVia parses args against the real command tree, as a user would type
// them, and resolves the server and token the way every server command does.
func resolveVia(t *testing.T, args ...string) (server, token string, err error) {
	t.Helper()
	root := NewRootCommand()
	cmd, rest, ferr := root.Find(args)
	if ferr != nil {
		t.Fatalf("finding %v: %v", args, ferr)
	}
	if perr := cmd.ParseFlags(rest); perr != nil {
		t.Fatalf("parsing %v: %v", rest, perr)
	}
	serverFlag, _ := cmd.Flags().GetString("server")
	tokenFlag, _ := cmd.Flags().GetString("token")
	return resolveServerToken(cmd, serverFlag, tokenFlag)
}

func TestTargetFromTheWorkspaceDefaultInASubfolder(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	t.Chdir(filepath.Join(root, "dags", "orders"))
	server, _, err := resolveVia(t, "dags", "list", "--config", cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if server != "https://dev.example" {
		t.Errorf("server = %q, want the workspace's default target", server)
	}
}

func TestTargetPrecedence(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{name: "--target flag", args: []string{"--target", "prod"}, want: "https://prod.example"},
		{name: "DEXAFLOW_TARGET", env: map[string]string{"DEXAFLOW_TARGET": "prod"}, want: "https://prod.example"},
		{name: "LEOFLOW_TARGET", env: map[string]string{"LEOFLOW_TARGET": "prod"}, want: "https://prod.example"},
		{name: "flag beats env", env: map[string]string{"DEXAFLOW_TARGET": "prod"}, args: []string{"--target", "dev"}, want: "https://dev.example"},
		{name: "--server beats --target", args: []string{"--target", "prod", "--server", "https://other.example"}, want: "https://other.example"},
		{name: "--server-url beats --target", args: []string{"--target", "prod", "--server-url", "https://other.example"}, want: "https://other.example"},
		{name: "server env beats the workspace default", env: map[string]string{"DEXAFLOW_SERVER_URL": "https://env.example"}, want: "https://env.example"},
		{name: "target env beats server env", env: map[string]string{"DEXAFLOW_SERVER_URL": "https://env.example", "DEXAFLOW_TARGET": "prod"}, want: "https://prod.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLIEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			root := targetWorkspace(t, "https://dev.example", "https://prod.example")
			t.Chdir(root)
			cfg := filepath.Join(t.TempDir(), "c.yaml")
			writeTree(t, cfg, "")
			args := append([]string{"dags", "list", "--config", cfg}, tc.args...)
			server, _, err := resolveVia(t, args...)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if server != tc.want {
				t.Errorf("server = %q, want %q", server, tc.want)
			}
		})
	}
}

func TestUnknownTargetNamesTheKnownOnes(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(targetWorkspace(t, "https://dev.example", "https://prod.example"))
	_, _, err := resolveVia(t, "dags", "list", "--config", filepath.Join(t.TempDir(), "c.yaml"), "--target", "staging")
	if err == nil || !strings.Contains(err.Error(), `"staging"`) || !strings.Contains(err.Error(), "dev, prod") {
		t.Errorf("err = %v, want the unknown name and the known targets", err)
	}
}

func TestTokensAreKeptPerServer(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(targetWorkspace(t, "https://dev.example", "https://prod.example"))
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "server_url: http://127.0.0.1:8088\ntoken: lite-tok\n"+
		"sessions:\n  - {server: https://dev.example, token: dev-tok}\n  - {server: https://prod.example/, token: prod-tok}\n")
	for target, want := range map[string]string{"dev": "dev-tok", "prod": "prod-tok"} {
		_, tok, err := resolveVia(t, "dags", "list", "--config", cfg, "--target", target)
		if err != nil {
			t.Fatalf("resolve %s: %v", target, err)
		}
		if tok != want {
			t.Errorf("token for %s = %q, want %q", target, tok, want)
		}
	}
}

func TestTheLegacyTokenNeverReachesAnotherTargetServer(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(targetWorkspace(t, "https://dev.example", "https://prod.example"))
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "server_url: https://prod.example\ntoken: legacy-tok\n")
	_, tok, err := resolveVia(t, "dags", "list", "--config", cfg, "--target", "dev")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tok != "" {
		t.Errorf("token = %q: a token saved for prod must not be sent to dev", tok)
	}
	_, tok, err = resolveVia(t, "dags", "list", "--config", cfg, "--target", "prod")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tok != "legacy-tok" {
		t.Errorf("token = %q: the single-server session still serves the server it was issued for", tok)
	}
}

func TestNoTargetKeepsTheSingleServerConfig(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(t.TempDir())
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "server_url: https://pro.example\ntoken: legacy-tok\n")
	server, tok, err := resolveVia(t, "dags", "list", "--config", cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if server != "https://pro.example" || tok != "legacy-tok" {
		t.Errorf("got %q %q, want the config's server_url and token", server, tok)
	}
}

func TestUserTargetOutsideAWorkspace(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(t.TempDir())
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "server_url: https://pro.example\ncurrent_target: lab\n"+
		"targets:\n  - {name: lab, server: https://lab.example}\n"+
		"sessions:\n  - {server: https://lab.example, token: lab-tok}\n")
	server, tok, err := resolveVia(t, "dags", "list", "--config", cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if server != "https://lab.example" || tok != "lab-tok" {
		t.Errorf("got %q %q, want the current user target and its session", server, tok)
	}
}

func TestWorkspaceSelectionBeatsTheSharedDefault(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	t.Chdir(root)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.UpdateUserStore(cfg, func(s *config.UserStore) error {
		s.SetWorkspaceTarget(root, "prod")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server, _, err := resolveVia(t, "dags", "list", "--config", cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if server != "https://prod.example" {
		t.Errorf("server = %q, want the target selected with `target use`", server)
	}
}

// TestDexaflowTokenIsAnExternalToken is the deploy.go fix: only LEOFLOW_TOKEN
// was checked, so a token supplied as DEXAFLOW_TOKEN was treated as the saved
// session: auto-refreshed into the config file, and blamed by the 401 hint.
func TestDexaflowTokenIsAnExternalToken(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(t.TempDir())
	t.Setenv("DEXAFLOW_TOKEN", "env-tok")
	resolvedTarget = targetRecord{}
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "server_url: http://127.0.0.1:8088\n")
	_, tok, err := resolveVia(t, "dags", "list", "--config", cfg, "--server", "https://prod.example")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tok != "env-tok" {
		t.Errorf("token = %q, want the DEXAFLOW_TOKEN value", tok)
	}
	if resolvedTarget.fromConfig {
		t.Error("a DEXAFLOW_TOKEN must not be recorded as the saved session")
	}
	if strings.Contains(apiStatusError(401, []byte("{}")).Error(), "hint:") {
		t.Error("no saved-session hint is owed for a token the user supplied")
	}
}

// TestServerCommandsSendEachTargetItsOwnToken drives real commands end to end:
// the bearer each control plane receives is the one saved for it.
func TestServerCommandsSendEachTargetItsOwnToken(t *testing.T) {
	isolateCLIEnv(t)
	var mu sync.Mutex
	got := map[string]string{}
	handler := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			got[name] = r.Header.Get("Authorization")
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dags":[],"total_entries":0}`))
		}
	}
	dev := httptest.NewServer(handler("dev"))
	defer dev.Close()
	prod := httptest.NewServer(handler("prod"))
	defer prod.Close()
	t.Chdir(targetWorkspace(t, dev.URL, prod.URL))
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "sessions:\n  - {server: "+dev.URL+", token: dev-tok}\n  - {server: "+prod.URL+", token: prod-tok}\n")
	if _, _, err := run(t, "dags", "list", "--config", cfg); err != nil {
		t.Fatalf("dags list (default target): %v", err)
	}
	if _, _, err := run(t, "dags", "list", "--config", cfg, "--target", "prod"); err != nil {
		t.Fatalf("dags list --target prod: %v", err)
	}
	if got["dev"] != "Bearer dev-tok" || got["prod"] != "Bearer prod-tok" {
		t.Errorf("bearers = %v, want each server's own token", got)
	}
}

// TestTheLegacyTokenNeverReachesAnOverriddenServer: the single-server token
// goes only to the server it was saved with, whatever chose the server.
func TestTheLegacyTokenNeverReachesAnOverriddenServer(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		args []string
	}{
		"--server":            {args: []string{"--server", "https://elsewhere.example"}},
		"--server-url":        {args: []string{"--server-url", "https://elsewhere.example"}},
		"DEXAFLOW_SERVER_URL": {env: map[string]string{"DEXAFLOW_SERVER_URL": "https://elsewhere.example"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolateCLIEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cfg := filepath.Join(t.TempDir(), "config.yaml")
			writeTree(t, cfg, "server_url: https://pro.example\ntoken: legacy-tok\n")
			_, tok, err := resolveVia(t, append([]string{"dags", "list", "--config", cfg}, tc.args...)...)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if tok != "" {
				t.Errorf("token = %q: the token saved for pro.example must not go to elsewhere.example", tok)
			}
			msg := apiStatusError(401, []byte("{}")).Error()
			if !strings.Contains(msg, "pro.example") || !strings.Contains(msg, "elsewhere.example") {
				t.Errorf("a 401 must still explain the mismatch, got %q", msg)
			}
		})
	}
}

func TestTheLegacyTokenWithoutAServerURLIsStillSent(t *testing.T) {
	isolateCLIEnv(t)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "token: legacy-tok\n")
	_, tok, err := resolveVia(t, "dags", "list", "--config", cfg, "--server", "https://pro.example")
	if err != nil || tok != "legacy-tok" {
		t.Errorf("token = %q, %v; a token saved with no server_url has nothing to mismatch", tok, err)
	}
}
