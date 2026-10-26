package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A managed pin wins over a Docker that answers (ADR 0094): the restored or
// chosen managed datastore is the one with the data, and auto must not start
// an empty Docker Postgres next to it (#1997).
func TestAutoDatastoreFollowsAManagedPin(t *testing.T) {
	d := testDeps()
	probed := false
	d.dockerPing = func() error { probed = true; return nil }
	d.composeProbe = func() error { return nil }
	dir := t.TempDir()
	if err := writeDatastorePin(dir, datastoreManaged); err != nil {
		t.Fatal(err)
	}
	cmd, out := outCmd()
	if got := autoDatastore(testCtx(d), cmd, datastoreAuto, dir); got != datastoreManaged {
		t.Fatalf("autoDatastore with a managed pin = %q, want %q", got, datastoreManaged)
	}
	if probed {
		t.Error("a managed pin must not probe Docker")
	}
	if !strings.Contains(out.String(), "--postgres docker") {
		t.Errorf("the note must say how to switch back, got: %s", out.String())
	}
}

// A Docker pin, or none, keeps the ADR 0030 rule: Docker when it answers.
func TestAutoDatastoreWithoutAManagedPinKeepsTheDockerRule(t *testing.T) {
	for name, pin := range map[string]string{"none": "", "docker": datastoreDocker, "garbage": "postgres:16"} {
		t.Run(name, func(t *testing.T) {
			dockerWithoutCompose(t) // a docker on PATH, so Docker counts as present
			d := testDeps()
			d.dockerPing = func() error { return nil }
			d.composeProbe = func() error { return nil }
			dir := t.TempDir()
			if pin != "" {
				if err := os.WriteFile(filepath.Join(dir, datastorePinFile), []byte(pin+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd, _ := outCmd()
			if got := autoDatastore(testCtx(d), cmd, datastoreAuto, dir); got != datastoreDocker {
				t.Errorf("autoDatastore with pin %q = %q, want %q", pin, got, datastoreDocker)
			}
		})
	}
}

// An explicit --postgres is returned unchanged, whatever the pin says.
func TestExplicitPostgresIgnoresThePin(t *testing.T) {
	dir := t.TempDir()
	if err := writeDatastorePin(dir, datastoreManaged); err != nil {
		t.Fatal(err)
	}
	cmd, _ := outCmd()
	if got := autoDatastore(testCtx(nil), cmd, datastoreDocker, dir); got != datastoreDocker {
		t.Errorf("autoDatastore(docker) with a managed pin = %q, want %q", got, datastoreDocker)
	}
}

// Only an explicit --postgres pins: an auto run that fell back to the managed
// Postgres because Docker was down must not move the install for good.
func TestPinExplicitDatastoreOnlyPinsAnExplicitChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUDO_USER", "")
	cmd, _ := outCmd()
	pinExplicitDatastore(cmd, false, datastoreManaged)
	if got := readDatastorePin(liteDevDir()); got != "" {
		t.Fatalf("an auto run pinned %q", got)
	}
	pinExplicitDatastore(cmd, true, datastoreManaged)
	if got := readDatastorePin(liteDevDir()); got != datastoreManaged {
		t.Fatalf("pin after --postgres managed = %q, want %q", got, datastoreManaged)
	}
	pinExplicitDatastore(cmd, true, datastoreDocker)
	if got := readDatastorePin(liteDevDir()); got != datastoreDocker {
		t.Fatalf("pin after --postgres docker = %q, want %q", got, datastoreDocker)
	}
}
