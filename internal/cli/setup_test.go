package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLibcSuffix(t *testing.T) {
	cases := map[string]string{"": "", "glibc": " (glibc)", "musl": " (musl)"}
	for in, want := range cases {
		if got := libcSuffix(in); got != want {
			t.Errorf("libcSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteSetupManifest(t *testing.T) {
	home := t.TempDir()
	want := setupManifest{
		Python: "/p/python3.11", Workspace: "/w", Tier: "k8s",
		OS: "linux", Arch: "amd64", ParserCmd: "/v/bin/python -m leoflow_parser",
		UpdatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := writeSetupManifest(home, want); err != nil {
		t.Fatalf("writeSetupManifest err = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, "setup.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var got setupManifest
	if uerr := json.Unmarshal(data, &got); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	if got.Python != want.Python || got.Tier != want.Tier || got.ParserCmd != want.ParserCmd {
		t.Errorf("manifest round-trip mismatch: got %+v", got)
	}
}

func TestRunSetupVenvless(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A fake python3.11 on PATH so EnsurePython uses it instead of downloading.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "python3.11"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := newSetupCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	ws := filepath.Join(home, "ws")
	cmd.SetArgs([]string{"--workspace", ws})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup err = %v\n%s", err, out.String())
	}
	// Sources extracted, workspace + manifest + config created — no parser venv.
	for _, p := range []string{
		filepath.Join(home, ".dexaflow", "pysrc", "parser", "pyproject.toml"),
		filepath.Join(home, ".dexaflow", "setup.json"),
		filepath.Join(home, ".dexaflow", "config.yaml"),
		ws,
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".dexaflow", "parser-venv")); !os.IsNotExist(err) {
		t.Error("a parser-venv was created; setup must be venv-less (ADR 0024)")
	}
	cfg, _ := os.ReadFile(filepath.Join(home, ".dexaflow", "config.yaml"))
	if !strings.Contains(string(cfg), "leoflow_parser") || !strings.Contains(string(cfg), "PYTHONPATH") {
		t.Errorf("config.yaml should set a PYTHONPATH-based parser_cmd:\n%s", cfg)
	}
}

func TestRunSetupDryRun(t *testing.T) {
	cmd := newSetupCommand()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup --dry-run err = %v", err)
	}
	s := out.String()
	for _, want := range []string{"dexaflow setup", "platform", "executor", "dry run"} {
		if !strings.Contains(s, want) {
			t.Errorf("dry-run output missing %q\n---\n%s", want, s)
		}
	}
}

func TestWriteLiteConfig(t *testing.T) {
	home := t.TempDir()
	lc := liteSettings{Workspace: "/ws", Executor: "subprocess", AdminEmail: "admin@leoflow.local", Port: 8088}
	if err := writeLiteConfig(home, "env PYTHONPATH=/p python -m leoflow_parser", lc, "$2a$12$abcHASH", liteFileSecrets{jwtSecret: "deadbeef-jwt-secret", secretKey: "deadbeef-secret-key"}); err != nil {
		t.Fatalf("writeLiteConfig err = %v", err)
	}
	data, rerr := os.ReadFile(filepath.Join(home, "config.yaml"))
	if rerr != nil {
		t.Fatalf("reading config: %v", rerr)
	}
	s := string(data)
	for _, want := range []string{
		"parser_cmd:", "leoflow_parser", "workspace: \"/ws\"", "lite_executor: \"subprocess\"",
		"lite_port: 8088", "admin_email: \"admin@leoflow.local\"", "admin_password_hash:",
		// Per-install JWT secret rotates on every fresh install (#121); the marker
		// must be persisted so the lite runner can pick it up.
		"jwt_secret: \"deadbeef-jwt-secret\"",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("config missing %q\n---\n%s", want, s)
		}
	}
	// Only the hash is stored — never a plaintext password field.
	if strings.Contains(s, "password:") {
		t.Errorf("config must not contain a plaintext password field:\n%s", s)
	}
	if fi, _ := os.Stat(filepath.Join(home, "config.yaml")); fi != nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("config.yaml mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestLoadManifestSettings(t *testing.T) {
	def := liteSettings{Workspace: "/def", Executor: "subprocess", AdminEmail: "admin@leoflow.local", Port: 8088}

	t.Run("missing manifest returns defaults", func(t *testing.T) {
		if got := loadManifestSettings(t.TempDir(), def); got != def {
			t.Errorf("got %+v, want defaults", got)
		}
	})

	t.Run("reads recorded settings", func(t *testing.T) {
		home := t.TempDir()
		m := setupManifest{Workspace: "/ws", Executor: "k8s", Port: 9090, AdminEmail: "me@x.io"}
		data, _ := json.MarshalIndent(m, "", "  ")
		if err := os.WriteFile(filepath.Join(home, "setup.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		got := loadManifestSettings(home, def)
		want := liteSettings{Workspace: "/ws", Executor: "k8s", AdminEmail: "me@x.io", Port: 9090}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestRunSetupReRunKeepsSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "python3.11"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Pre-existing config + manifest = a re-run: must not re-prompt or re-generate.
	lf := filepath.Join(home, ".dexaflow")
	if err := os.MkdirAll(lf, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lf, "config.yaml"),
		[]byte("admin_password_hash: \"$2a$12$x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(setupManifest{Workspace: filepath.Join(home, "ws"), Executor: "k8s", Port: 9090, AdminEmail: "me@x.io"}, "", "  ")
	if err := os.WriteFile(filepath.Join(lf, "setup.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newSetupCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("re-run setup err = %v\n%s", err, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "already configured") {
		t.Errorf("re-run should say 'already configured':\n%s", s)
	}
	if strings.Contains(s, "shown only once") {
		t.Error("re-run must NOT generate/print a new password")
	}
	if !strings.Contains(s, "me@x.io") || !strings.Contains(s, "k8s") {
		t.Errorf("re-run should keep recorded settings (admin/executor):\n%s", s)
	}
}

func TestGatherLiteConfig(t *testing.T) {
	def := liteSettings{Workspace: "/def/ws", Executor: "subprocess", AdminEmail: "admin@leoflow.local", Port: 8088}

	t.Run("non-interactive returns defaults verbatim", func(t *testing.T) {
		got := gatherLiteConfig(false, bufio.NewReader(strings.NewReader("")), io.Discard, def)
		if got != def {
			t.Errorf("got %+v, want defaults %+v", got, def)
		}
	})

	t.Run("interactive parses answers; blank keeps default; invalid executor re-asks", func(t *testing.T) {
		// workspace, executor(invalid then valid), port, admin email
		in := bufio.NewReader(strings.NewReader("/my/ws\nbogus\nk8s\n9000\nme@x.io\n"))
		got := gatherLiteConfig(true, in, io.Discard, def)
		want := liteSettings{Workspace: "/my/ws", Executor: "k8s", AdminEmail: "me@x.io", Port: 9000}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("interactive with all-blank keeps defaults", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("\n\n\n\n"))
		got := gatherLiteConfig(true, in, io.Discard, def)
		if got != def {
			t.Errorf("got %+v, want defaults %+v", got, def)
		}
	})

	// The numbered menu (the human feedback): typing 1/2 must work, not only the
	// names. 2 -> cluster (k8s), 1 -> local (subprocess).
	t.Run("numbered run mode picks the executor", func(t *testing.T) {
		if got := gatherLiteConfig(true, bufio.NewReader(strings.NewReader("/ws\n2\n8088\nx@y.io\n")), io.Discard, def); got.Executor != "k8s" {
			t.Errorf("'2' should select cluster (k8s), got %q", got.Executor)
		}
		if got := gatherLiteConfig(true, bufio.NewReader(strings.NewReader("/ws\n1\n8088\nx@y.io\n")), io.Discard, def); got.Executor != "subprocess" {
			t.Errorf("'1' should select local (subprocess), got %q", got.Executor)
		}
	})
}
