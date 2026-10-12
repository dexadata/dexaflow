package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// recordingBuilder writes a stand-in for `docker` that records the -f operand
// it was given, so a test can see which Dockerfile a build would have used.
func recordingBuilder(t *testing.T) (builder, record string) {
	t.Helper()
	builder = filepath.Join(t.TempDir(), "fake-builder")
	record = filepath.Join(t.TempDir(), "dockerfile.txt")
	script := "#!/bin/sh\nwhile [ $# -gt 0 ]; do if [ \"$1\" = -f ]; then printf %s \"$2\" > \"" + record + "\"; fi; shift; done\nexit 0\n"
	if err := os.WriteFile(builder, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	return builder, record
}

// buildWithConfiguredDockerfile drives the real --build path for a project in
// dir whose dexaflow.yaml sets build.dockerfile, and returns the -f the builder
// saw ("" when it never ran) and the error.
func buildWithConfiguredDockerfile(t *testing.T, dir, dockerfile string) (string, error) {
	t.Helper()
	builder, record := recordingBuilder(t)
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cfg := &domain.LeoflowConfig{DagID: "d", Build: &domain.BuildConfig{Dockerfile: dockerfile}}
	cfg.ApplyDefaults()
	err := buildAndPush(cmd, dir, compileOptions{build: true, builder: builder, dockerfile: "Dockerfile"}, cfg, "img:t")
	seen, rerr := os.ReadFile(record) //nolint:gosec // a path this test created
	if rerr != nil {
		return "", err
	}
	return string(seen), err
}

// outsideDockerfile is a Dockerfile in a directory next to the project, the
// "sibling directory nobody reviewed" of #1272.
func outsideDockerfile(t *testing.T) string {
	t.Helper()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return outside
}

func projectWithDag(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A symlink is lexically clean, so Validate cannot see where it leads: the
// build has to resolve it against the real project directory before using it.
// Both shapes are covered: a linked directory on the way, and the file itself.
func TestBuildRefusesAConfiguredDockerfileThatASymlinkTakesOutOfTheProject(t *testing.T) {
	t.Run("linked directory", func(t *testing.T) {
		dir := projectWithDag(t)
		if err := os.Symlink(outsideDockerfile(t), filepath.Join(dir, "docker")); err != nil {
			t.Fatal(err)
		}
		seen, err := buildWithConfiguredDockerfile(t, dir, "docker/Dockerfile")
		assertRefusedOutsideProject(t, seen, err, "docker/Dockerfile")
	})
	t.Run("linked file", func(t *testing.T) {
		dir := projectWithDag(t)
		if err := os.Symlink(filepath.Join(outsideDockerfile(t), "Dockerfile"), filepath.Join(dir, "Dockerfile.prod")); err != nil {
			t.Fatal(err)
		}
		seen, err := buildWithConfiguredDockerfile(t, dir, "Dockerfile.prod")
		assertRefusedOutsideProject(t, seen, err, "Dockerfile.prod")
	})
	// A caller that skipped Validate still cannot reach a file outside by
	// spelling: the resolved check catches the lexical shapes too.
	t.Run("dot-dot without Validate", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "proj")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(parent, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		seen, err := buildWithConfiguredDockerfile(t, dir, "../Dockerfile")
		assertRefusedOutsideProject(t, seen, err, "../Dockerfile")
	})
}

func assertRefusedOutsideProject(t *testing.T, seen string, err error, value string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the build used a Dockerfile outside the project: -f %s", seen)
	}
	if seen != "" {
		t.Errorf("the builder ran with -f %s before the refusal", seen)
	}
	for _, want := range []string{"build.dockerfile", value, "outside"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must mention %q, got: %v", want, err)
		}
	}
}

// The counterweight: a configured Dockerfile inside the project keeps being
// used as-is, including through a symlink that stays inside, and when the
// project directory itself is reached through a symlink (a CI workspace often
// is). Refusing any of these would break a project that builds today.
func TestBuildKeepsAConfiguredDockerfileInsideTheProject(t *testing.T) {
	t.Run("nested file", func(t *testing.T) {
		dir := projectWithDag(t)
		writeStubDockerfile(t, filepath.Join(dir, "docker", "Dockerfile.prod"))
		seen, err := buildWithConfiguredDockerfile(t, dir, "docker/Dockerfile.prod")
		assertBuiltWith(t, seen, err, filepath.Join(dir, "docker", "Dockerfile.prod"))
	})
	t.Run("symlink inside the project", func(t *testing.T) {
		dir := projectWithDag(t)
		writeStubDockerfile(t, filepath.Join(dir, "docker", "Dockerfile.prod"))
		if err := os.Symlink(filepath.Join("docker", "Dockerfile.prod"), filepath.Join(dir, "Dockerfile.link")); err != nil {
			t.Fatal(err)
		}
		seen, err := buildWithConfiguredDockerfile(t, dir, "Dockerfile.link")
		assertBuiltWith(t, seen, err, filepath.Join(dir, "Dockerfile.link"))
	})
	t.Run("project reached through a symlink", func(t *testing.T) {
		target := projectWithDag(t)
		writeStubDockerfile(t, filepath.Join(target, "Dockerfile.prod"))
		link := filepath.Join(t.TempDir(), "proj")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		seen, err := buildWithConfiguredDockerfile(t, link, "Dockerfile.prod")
		assertBuiltWith(t, seen, err, filepath.Join(link, "Dockerfile.prod"))
	})
	// A configured name that does not exist falls through to the generated
	// Dockerfile, which is the guarded path, exactly as before.
	t.Run("missing file", func(t *testing.T) {
		dir := projectWithDag(t)
		seen, err := buildWithConfiguredDockerfile(t, dir, "docker/Dockerfile.prod")
		assertBuiltWith(t, seen, err, filepath.Join(dir, generatedDockerfileName))
	})
}

func writeStubDockerfile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertBuiltWith(t *testing.T, seen string, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("the build was refused: %v", err)
	}
	if seen != want {
		t.Errorf("the builder got -f %q, want %q", seen, want)
	}
}

// --dockerfile is the operator's own choice on the command line, not a value a
// reviewed yaml carries, so it keeps meaning what it says.
func TestTheDockerfileFlagIsNotConfinedToTheProject(t *testing.T) {
	dir := projectWithDag(t)
	outside := filepath.Join(outsideDockerfile(t), "Dockerfile")
	builder, record := recordingBuilder(t)
	cmd := &cobra.Command{}
	cmd.Flags().String("dockerfile", "Dockerfile", "")
	if err := cmd.Flags().Set("dockerfile", "../x/Dockerfile"); err != nil {
		t.Fatal(err)
	}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	rel, rerr := filepath.Rel(dir, outside)
	if rerr != nil {
		t.Fatal(rerr)
	}
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	o := compileOptions{build: true, builder: builder, dockerfile: rel}
	if err := buildAndPush(cmd, dir, o, cfg, "img:t"); err != nil {
		t.Fatalf("an explicit --dockerfile was refused: %v", err)
	}
	seen, err := os.ReadFile(record) //nolint:gosec // a path this test created
	if err != nil || string(seen) != filepath.Join(dir, rel) {
		t.Errorf("the builder got -f %q (%v), want %q", seen, err, filepath.Join(dir, rel))
	}
}

// `validate` refuses the lexical shapes for both config file names, before any
// build is involved.
func TestValidateCommandRefusesABuildDockerfileOutsideTheProject(t *testing.T) {
	for _, configName := range []string{projectConfigFile, legacyProjectConfigFile} {
		t.Run(configName, func(t *testing.T) {
			dir := writePoisonedProject(t, configName, "build:\n  dockerfile: ../../other/Dockerfile\n")
			cmd := newValidateCommand()
			cmd.SetArgs([]string{dir})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "build.dockerfile") {
				t.Fatalf("validate must refuse and name build.dockerfile, got: %v", err)
			}
		})
	}
}
