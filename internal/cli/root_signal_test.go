//go:build unix

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// interruptedBuildProject sets up `dexaflow compile --build` on a project with
// a .dockerignore of its own and a builder that blocks until it is stopped. It
// returns the root command ready to run, the project directory, and the marker
// file the builder writes once it is running.
func interruptedBuildProject(t *testing.T) (root *cobra.Command, dir, started string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dockerignoreName), []byte("node_modules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parser := filepath.Join(t.TempDir(), "fake-parser.sh")
	pscript := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"cat > \"$out\" <<'JSON'\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"proj\",\"dag_version\":\"dev\",\"image\":\"test:v1\",\"tasks\":[{\"task_id\":\"hello\",\"type\":\"python\",\"entrypoint\":\"dag:hello\"}]}\n" +
		"JSON\n"
	if err := os.WriteFile(parser, []byte(pscript), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	// The builder stands in for a docker build that takes minutes: it says it
	// started, then waits. exec keeps it one process, so the signal the CLI
	// sends it is the one that ends it.
	started = filepath.Join(t.TempDir(), "started")
	builder := filepath.Join(t.TempDir(), "slow-docker.sh")
	if err := os.WriteFile(builder, []byte("#!/bin/sh\n: > "+started+"\nexec sleep 60\n"), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	root = NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"compile", dir, "--output", filepath.Join(dir, "dag.json"), "--image", "test:v1",
		"--parser-cmd", parser, "--build", "--builder", builder})
	return root, dir, started
}

// waitForFile polls until path exists, failing the test after a while.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared: the build did not start", path)
}

// assertBuildCleanedUp checks that an interrupted build left the project as it
// found it: no generated Dockerfile, the author's .dockerignore restored.
func assertBuildCleanedUp(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, generatedDockerfileName)); !os.IsNotExist(err) {
		t.Errorf("the interrupted build left %s behind (stat err = %v)", generatedDockerfileName, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, dockerignoreName)) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatalf("reading .dockerignore: %v", err)
	}
	if string(got) != "node_modules\n" {
		t.Errorf("the interrupted build left the merged .dockerignore behind:\n%s", got)
	}
}

// waitForResult waits for the command to return, failing the test when the
// builder was not stopped.
func waitForResult(t *testing.T, done <-chan struct {
	code int
	err  error
}) (int, error) {
	t.Helper()
	select {
	case r := <-done:
		return r.code, r.err
	case <-time.After(30 * time.Second):
		t.Fatal("the command did not return after the stop: the builder was left running")
		return 0, nil
	}
}

// A canceled context stops `compile --build` mid-build and the deferred
// cleanups run (#1061): the generated Dockerfile is removed and the
// .dockerignore restored, and the builder is stopped rather than left running.
func TestCanceledBuildRunsTheDockerfileCleanup(t *testing.T) {
	root, dir, started := interruptedBuildProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, err := executeRoot(ctx, root)
		done <- struct {
			code int
			err  error
		}{code, err}
	}()
	waitForFile(t, started)
	// The test means something only if the files were there while it ran.
	if _, err := os.Stat(filepath.Join(dir, generatedDockerfileName)); err != nil {
		t.Fatalf("the build ran without a generated Dockerfile, so this test proves nothing: %v", err)
	}
	cancel()
	code, err := waitForResult(t, done)
	if err == nil {
		t.Fatal("a canceled build reported success")
	}
	if code != exitInterrupted {
		t.Errorf("exit code = %d, want %d for an interrupted command", code, exitInterrupted)
	}
	assertBuildCleanedUp(t, dir)
}

// The documented path is Ctrl-C (SIGINT) or SIGTERM reaching the CLI, not a
// context a test cancels: executeWithSignals is what Execute runs, and a
// SIGTERM sent to the process must end the build the same way.
func TestSigtermDuringBuildRunsTheDockerfileCleanup(t *testing.T) {
	root, dir, started := interruptedBuildProject(t)
	done := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, err := executeWithSignals(root)
		done <- struct {
			code int
			err  error
		}{code, err}
	}()
	// The handler is installed before the command runs, so once the builder
	// started the signal is caught rather than ending the test binary.
	waitForFile(t, started)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	code, err := waitForResult(t, done)
	if err == nil {
		t.Fatal("a build stopped by SIGTERM reported success")
	}
	if code != exitInterrupted {
		t.Errorf("exit code = %d, want %d for an interrupted command", code, exitInterrupted)
	}
	assertBuildCleanedUp(t, dir)
}
