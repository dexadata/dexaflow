package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// dockerWithoutCompose puts on PATH a `docker` whose daemon answers but which
// has no Compose v2 plugin: `docker version` succeeds and every `docker
// compose ...` fails the way the docker CLI does without the plugin. It returns
// the log of the argv of every call.
func dockerWithoutCompose(t *testing.T) (log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + log + "\"\n" +
		"case \"$1\" in\n" +
		"  version) exit 0 ;;\n" +
		"  compose) echo \"unknown shorthand flag: 'f' in -f\" >&2; echo \"Usage:  docker [OPTIONS] COMMAND [ARG...]\" >&2; exit 125 ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// dockerCalls returns the logged argv lines.
func dockerCalls(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log) //nolint:gosec // a path this test created
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func outCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	return cmd, &out
}

// A daemon that answers is not enough for the Docker Postgres: it is started
// with `docker compose`, and without the Compose v2 plugin `--postgres auto`
// picked Docker and then died on a raw docker usage error (#759). The plugin is
// now probed next to the daemon, and its absence falls back to the managed
// Postgres the way a daemon that does not answer does, saying why.
func TestAutoPostgresFallsBackToManagedWithoutTheComposePlugin(t *testing.T) {
	log := dockerWithoutCompose(t)
	cmd, out := outCmd()
	if got := autoDatastore(testCtx(nil), cmd, datastoreAuto, t.TempDir()); got != datastoreManaged {
		t.Fatalf("autoDatastore = %q, want %q without the Compose v2 plugin", got, datastoreManaged)
	}
	if !strings.Contains(dockerCalls(t, log), "compose version") {
		t.Errorf("the plugin was never probed; docker calls:\n%s", dockerCalls(t, log))
	}
	note := out.String()
	for _, want := range []string{"Compose v2 plugin", "managed Postgres"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note must contain %q, got: %s", want, note)
		}
	}
}

// The counterweight: with the plugin, auto keeps the Docker Postgres.
func TestAutoPostgresKeepsDockerWithTheComposePlugin(t *testing.T) {
	dockerWithoutCompose(t) // a docker on PATH, so Docker counts as present
	d := testDeps()
	d.dockerPing = func() error { return nil }
	d.composeProbe = func() error { return nil }
	cmd, _ := outCmd()
	if got := autoDatastore(testCtx(d), cmd, datastoreAuto, t.TempDir()); got != datastoreDocker {
		t.Errorf("autoDatastore = %q, want %q with the Compose v2 plugin", got, datastoreDocker)
	}
}

// An explicit --postgres docker without the plugin fails with what to do, not
// the docker CLI's usage text, and never runs `docker compose up`.
func TestDockerPostgresWithoutTheComposePluginSaysWhatToDo(t *testing.T) {
	log := dockerWithoutCompose(t)
	cmd, _ := outCmd()
	err := devComposeUp(context.Background(), cmd, devOptions{composeFile: filepath.Join(t.TempDir(), "compose.yaml")}, "postgres")
	if err == nil {
		t.Fatal("devComposeUp succeeded without the Compose v2 plugin")
	}
	for _, want := range []string{"Compose v2 plugin", "install", "--postgres managed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must contain %q, got: %v", want, err)
		}
	}
	for _, raw := range []string{"unknown shorthand flag", "Usage:"} {
		if strings.Contains(err.Error(), raw) {
			t.Errorf("the error leaks the docker usage text %q: %v", raw, err)
		}
	}
	if calls := dockerCalls(t, log); strings.Contains(calls, " up ") {
		t.Errorf("docker compose up ran without the plugin; calls:\n%s", calls)
	}
}
