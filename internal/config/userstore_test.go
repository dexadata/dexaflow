package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReadUserStoreMissingFileIsEmpty(t *testing.T) {
	s, err := ReadUserStore(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("ReadUserStore: %v", err)
	}
	if len(s.Sessions) != 0 || len(s.Targets) != 0 || s.CurrentTarget != "" {
		t.Errorf("a missing file must read as an empty store, got %+v", s)
	}
}

func TestReadUserStoreReadsTheLegacySingleServerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server_url: https://pro.example\ntoken: old-tok\nworkspace: /w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadUserStore(path)
	if err != nil {
		t.Fatalf("ReadUserStore: %v", err)
	}
	if s.ServerURL != "https://pro.example" || s.Token != "old-tok" {
		t.Errorf("legacy keys not read: %+v", s)
	}
}

func TestUpdateUserStoreKeepsEveryOtherKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	orig := "server_url: https://pro.example\ntoken: old-tok\nlite_port: 8088\njwt_secret: abc\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	err := UpdateUserStore(path, func(s *UserStore) error {
		s.SetSession("https://dev.example/", "dev-tok")
		s.SetTarget(UserTarget{Name: "dev", Server: "https://dev.example"})
		s.CurrentTarget = "dev"
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateUserStore: %v", err)
	}
	raw := map[string]any{}
	data, _ := os.ReadFile(path)
	if uerr := yaml.Unmarshal(data, &raw); uerr != nil {
		t.Fatal(uerr)
	}
	for k, want := range map[string]any{"server_url": "https://pro.example", "token": "old-tok", "lite_port": 8088, "jwt_secret": "abc", "current_target": "dev"} {
		if raw[k] != want {
			t.Errorf("%s = %v, want %v", k, raw[k], want)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600: the file holds tokens", info.Mode().Perm())
	}
	s, _ := ReadUserStore(path)
	if tok, ok := s.Session("https://dev.example"); !ok || tok != "dev-tok" {
		t.Errorf("session for dev = %q, %v; a trailing slash must not split the key", tok, ok)
	}
	if _, ok := s.Session("https://pro.example"); ok {
		t.Error("the legacy token is not a per-server session")
	}
}

func TestUpdateUserStoreCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	if err := UpdateUserStore(path, func(s *UserStore) error { s.SetSession("https://a", "t"); return nil }); err != nil {
		t.Fatalf("UpdateUserStore: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "https://a") {
		t.Errorf("file not written: %q, %v", data, err)
	}
}

func TestUserStoreSessionsReplaceAndRemove(t *testing.T) {
	var s UserStore
	s.SetSession("https://a", "one")
	s.SetSession("https://a/", "two")
	if len(s.Sessions) != 1 {
		t.Fatalf("one server, one session; got %+v", s.Sessions)
	}
	if tok, _ := s.Session("https://a"); tok != "two" {
		t.Errorf("token = %q, want the latest", tok)
	}
	if !s.RemoveSession("https://a") || len(s.Sessions) != 0 {
		t.Errorf("RemoveSession left %+v", s.Sessions)
	}
}

func TestUserStoreTargetsAndWorkspaceSelection(t *testing.T) {
	var s UserStore
	s.SetTarget(UserTarget{Name: "dev", Server: "https://one"})
	s.SetTarget(UserTarget{Name: "dev", Server: "https://two"})
	if got, ok := s.Target("dev"); !ok || got.Server != "https://two" || len(s.Targets) != 1 {
		t.Errorf("SetTarget must replace by name, got %+v", s.Targets)
	}
	s.SetWorkspaceTarget("/w", "prod")
	if got := s.WorkspaceTarget("/w"); got != "prod" {
		t.Errorf("WorkspaceTarget = %q, want prod", got)
	}
	s.SetWorkspaceTarget("/w", "")
	if got := s.WorkspaceTarget("/w"); got != "" || len(s.WorkspaceTargets) != 0 {
		t.Errorf("an empty name clears the selection, got %q (%+v)", got, s.WorkspaceTargets)
	}
	if !s.RemoveTarget("dev") || s.RemoveTarget("dev") {
		t.Error("RemoveTarget must report whether it removed")
	}
}
