package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
)

// writeExecutable writes an executable test stub at path. Tests in this
// package run in parallel and fork processes, and a child forked while the
// stub is still open for writing inherits that descriptor until it calls exec.
// Running the stub in that window fails with "text file busy" (ETXTBSY,
// golang/go#22315). Holding syscall.ForkLock for reading while the file is
// open keeps every fork of this process out of the window, which is how the
// syscall package documents descriptor creation that must not race a fork.
func writeExecutable(t testing.TB, path, body string) {
	t.Helper()
	if err := writeExecutableFile(path, body); err != nil {
		t.Fatal(err)
	}
}

// writeExecutableFile is writeExecutable without the testing.TB, for helpers
// that report the error themselves.
func writeExecutableFile(path, body string) error {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	return os.WriteFile(path, []byte(body), 0o700) //nolint:gosec // a test stub must be executable
}

// A stub written while other goroutines fork and exec must always run: no
// "text file busy". Without the fork lock this fails within a few hundred
// rounds on a loaded machine (golang/go#22315).
func TestWriteExecutableNeverRacesAFork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	stop := make(chan struct{})
	var forkers sync.WaitGroup
	for range 4 {
		forkers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit 0").Run()
			}
		})
	}
	defer func() { close(stop); forkers.Wait() }()

	for i := range 300 {
		p := filepath.Join(dir, "stub"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		writeExecutable(t, p, "#!/bin/sh\nexit 0\n")
		if err := exec.CommandContext(t.Context(), p).Run(); err != nil { //nolint:gosec // the stub this test just wrote
			if errors.Is(err, syscall.ETXTBSY) {
				t.Fatalf("round %d: running a fresh stub failed with text file busy: %v", i, err)
			}
			t.Fatalf("round %d: running a fresh stub: %v", i, err)
		}
	}
}
