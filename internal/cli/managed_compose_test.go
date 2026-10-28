package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dexaflow "github.com/dexadata/dexaflow"
)

// composeLog collects what ensureManagedComposeIn prints.
type composeLog struct{ lines []string }

func (l *composeLog) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *composeLog) String() string { return strings.Join(l.lines, "\n") }

// writeComposeState seeds dir with a compose and, when sum is not empty, the
// checksum marker a managed write records next to it.
func writeComposeState(t *testing.T, dir string, compose []byte, sum string) string {
	t.Helper()
	path := filepath.Join(dir, managedComposeName)
	if err := os.WriteFile(path, compose, 0o600); err != nil {
		t.Fatal(err)
	}
	if sum != "" {
		if err := os.WriteFile(filepath.Join(dir, managedComposeMarker), []byte(sum+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func readFileT(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return b
}

// A fresh home gets the embedded compose and the checksum that marks it as
// managed, silently.
func TestEnsureManagedComposeFreshWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var log composeLog
	path, err := ensureManagedComposeIn(dir, log.logf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFileT(t, path), dexaflow.DevCompose()) {
		t.Error("fresh write is not the embedded compose")
	}
	if got := strings.TrimSpace(string(readFileT(t, filepath.Join(dir, managedComposeMarker)))); got != composeChecksum(dexaflow.DevCompose()) {
		t.Errorf("marker = %q, want the embed checksum", got)
	}
	if len(log.lines) != 0 {
		t.Errorf("fresh write printed %q, want nothing", log.String())
	}
}

// The binary-upgrade case (#981): the file is exactly what an earlier binary
// wrote (it matches the recorded checksum), so it is replaced by this binary's
// compose and the refresh is announced.
func TestEnsureManagedComposeRefreshesAnUnchangedOldCompose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old := []byte("services:\n  postgres:\n    image: example.invalid/older-lite-db:1\n")
	path := writeComposeState(t, dir, old, composeChecksum(old))
	var log composeLog
	if _, err := ensureManagedComposeIn(dir, log.logf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFileT(t, path), dexaflow.DevCompose()) {
		t.Error("an unchanged managed compose from an older binary was not refreshed")
	}
	if got := strings.TrimSpace(string(readFileT(t, filepath.Join(dir, managedComposeMarker)))); got != composeChecksum(dexaflow.DevCompose()) {
		t.Errorf("marker after refresh = %q, want the embed checksum", got)
	}
	if !strings.Contains(log.String(), "↻ refreshed the managed compose (binary upgrade)") {
		t.Errorf("refresh not announced: %q", log.String())
	}

	// The next start finds it current and says nothing.
	log = composeLog{}
	if _, err := ensureManagedComposeIn(dir, log.logf); err != nil {
		t.Fatal(err)
	}
	if len(log.lines) != 0 {
		t.Errorf("a current compose printed %q", log.String())
	}
}

// An operator's edit is never overwritten: the file is kept, the new compose
// is written beside it, and the warning names both and says how to switch. It
// is printed once per compose this binary ships, not on every start.
func TestEnsureManagedComposeKeepsAnEditedComposeAndWarnsOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old := []byte("services:\n  postgres:\n    image: example.invalid/older-lite-db:1\n")
	edited := append(append([]byte{}, old...), []byte("    shm_size: 1g\n")...)
	path := writeComposeState(t, dir, edited, composeChecksum(old))
	var log composeLog
	got, err := ensureManagedComposeIn(dir, log.logf)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	if !bytes.Equal(readFileT(t, path), edited) {
		t.Fatal("an edited compose was overwritten")
	}
	newPath := path + ".new"
	if !bytes.Equal(readFileT(t, newPath), dexaflow.DevCompose()) {
		t.Error("the new compose was not written beside the edited one")
	}
	msg := log.String()
	for _, want := range []string{"⚠", path, newPath, "delete"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning %q does not mention %q", msg, want)
		}
	}

	log = composeLog{}
	if _, err := ensureManagedComposeIn(dir, log.logf); err != nil {
		t.Fatal(err)
	}
	if len(log.lines) != 0 {
		t.Errorf("the warning repeated on the next start: %q", log.String())
	}
	if !bytes.Equal(readFileT(t, path), edited) {
		t.Error("an edited compose was overwritten on the second start")
	}

	// Deleting the edited file, as the warning says, gets the new compose on
	// the next start and clears the side copy.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureManagedComposeIn(dir, log.logf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFileT(t, path), dexaflow.DevCompose()) {
		t.Error("deleting the edited compose did not bring the new one")
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf("stale %s left after a fresh write (err=%v)", newPath, err)
	}
}

// A compose written by a binary from before the checksum existed has no
// marker, so nothing tells an untouched copy from an edited one. The safe
// choice: a file equal to this binary's compose is adopted silently (the
// marker is recorded), and any other file is kept and treated as edited.
func TestEnsureManagedComposeWithoutMarker(t *testing.T) {
	t.Parallel()
	t.Run("equal to the embed is adopted", func(t *testing.T) {
		dir := t.TempDir()
		writeComposeState(t, dir, dexaflow.DevCompose(), "")
		var log composeLog
		if _, err := ensureManagedComposeIn(dir, log.logf); err != nil {
			t.Fatal(err)
		}
		if len(log.lines) != 0 {
			t.Errorf("adopting a current compose printed %q", log.String())
		}
		if got := strings.TrimSpace(string(readFileT(t, filepath.Join(dir, managedComposeMarker)))); got != composeChecksum(dexaflow.DevCompose()) {
			t.Errorf("marker = %q, want the embed checksum", got)
		}
	})
	t.Run("different is kept with a warning", func(t *testing.T) {
		dir := t.TempDir()
		old := []byte("services:\n  postgres:\n    image: example.invalid/older-lite-db:1\n")
		path := writeComposeState(t, dir, old, "")
		var log composeLog
		if _, err := ensureManagedComposeIn(dir, log.logf); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readFileT(t, path), old) {
			t.Error("a compose with no marker was overwritten")
		}
		if !strings.Contains(log.String(), path+".new") {
			t.Errorf("no warning naming the new compose: %q", log.String())
		}
	})
}

// The refresh runs on the documented path: resolveComposeFile with no flag and
// no source checkout, under the legacy ~/.leoflow home as well.
func TestResolveComposeFileRefreshesUnderLegacyHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	legacy := filepath.Join(home, ".leoflow")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte("services: {}\n")
	writeComposeState(t, legacy, old, composeChecksum(old))
	var out bytes.Buffer
	path, err := resolveComposeFile(&out, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFileT(t, path), dexaflow.DevCompose()) {
		t.Errorf("compose at %s not refreshed", path)
	}
	if !strings.Contains(out.String(), "↻ refreshed the managed compose (binary upgrade)") {
		t.Errorf("refresh not printed: %q", out.String())
	}
}
