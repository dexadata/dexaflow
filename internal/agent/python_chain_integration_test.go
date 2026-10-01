//go:build integration

// E2e log-chain integration tests: spawn real python3 via the production
// execRunner and prove the full path
//
//	Python -> OS pipe -> logWriter -> sink
//
// captures every byte the user wrote, even under failure modes like
// os._exit (skips atexit) and SIGTERM (mid-run cancel). These are the
// reality-anchored counterparts to the fake-Cmd unit tests in
// runner_test.go and the regression guard for Defense 1 (-u flag +
// PYTHONUNBUFFERED env). Skips when python3 is absent on PATH.

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// captureSink stores every LogLine the agent's logWriter sends.
type captureSink struct {
	mu    sync.Mutex
	lines []*agentv1.LogLine
}

func (s *captureSink) Send(line *agentv1.LogLine) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, &agentv1.LogLine{
		Time: line.Time, Level: line.Level,
		Message: line.Message, Stream: line.Stream, LineNumber: line.LineNumber,
	})
	return nil
}

func (s *captureSink) Close() error { return nil }

// messagesFor returns the messages captured under the given stream, in order.
func (s *captureSink) messagesFor(stream string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.lines {
		if l.GetStream() == stream {
			out = append(out, l.GetMessage())
		}
	}
	return out
}

// runPython invokes python3 via the production execRunner with the same flags
// command.go bakes in (`-u`) and the same env vars buildEnv adds
// (PYTHONUNBUFFERED, PYTHONIOENCODING). Code is passed via -c so each
// scenario is self-contained.
func runPython(t *testing.T, ctx context.Context, code string) *captureSink {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 not on PATH: %v", err)
	}
	sink := &captureSink{}
	stdout := &logWriter{sink: sink, stream: "stdout", level: agentv1.LogLevel_LOG_LEVEL_INFO}
	stderr := &logWriter{sink: sink, stream: "stderr", level: agentv1.LogLevel_LOG_LEVEL_ERROR}

	argv := []string{py, "-u", "-c", code}
	env := append(os.Environ(), "PYTHONUNBUFFERED=1", "PYTHONIOENCODING=UTF-8")

	_, _ = NewExecRunner().Run(ctx, argv, env, stdout, stderr)
	stdout.flush()
	stderr.flush()
	return sink
}

// TestE2EPrintReachesSinkIntegration is the simplest case: a single print().
// Regression guard ensuring we never break the baseline.
func TestE2EPrintReachesSinkIntegration(t *testing.T) {
	sink := runPython(t, context.Background(), `print("CANARIO_BASIC")`)
	stdout := sink.messagesFor("stdout")
	if !anyContains(stdout, "CANARIO_BASIC") {
		t.Fatalf("expected stdout to contain CANARIO_BASIC, got %v", stdout)
	}
}

// TestE2EPrintSurvivesOsExitIntegration is the Defense 1 contract: a task that
// prints and immediately calls os._exit(0) — which SKIPS atexit handlers and
// would lose buffered bytes — must still ship every line. With `-u` Python
// writes synchronously so the pipe already has the bytes by the time the
// process dies. WITHOUT `-u` (regression), the bytes might sit in Python's
// 8 KiB block buffer and never reach the agent.
func TestE2EPrintSurvivesOsExitIntegration(t *testing.T) {
	code := `
import os
print("CANARIO_BEFORE_EXIT")
os._exit(0)
`
	sink := runPython(t, context.Background(), code)
	if !anyContains(sink.messagesFor("stdout"), "CANARIO_BEFORE_EXIT") {
		t.Fatalf("Defense 1 broken: print() before os._exit was lost; got stdout=%v\n"+
			"This means Python is buffering stdout (no -u, no PYTHONUNBUFFERED) and "+
			"the bytes sat in Python's block buffer when atexit was skipped.",
			sink.messagesFor("stdout"))
	}
}

// TestE2EStderrReachesSinkIntegration validates that stderr is captured on its
// own stream (the UI's "task.stderr" badge), distinct from stdout.
func TestE2EStderrReachesSinkIntegration(t *testing.T) {
	code := `
import sys
sys.stderr.write("CANARIO_STDERR\n")
sys.stderr.flush()
`
	sink := runPython(t, context.Background(), code)
	if !anyContains(sink.messagesFor("stderr"), "CANARIO_STDERR") {
		t.Fatalf("stderr was not captured on its own stream; lines: %+v", sink.lines)
	}
}

// TestE2EBigStdoutNoTruncationIntegration exercises the kernel pipe buffer
// (64 KiB on Linux/macOS): if our agent doesn't drain the pipe concurrently
// with the child, the child blocks on write and either hangs or loses bytes
// when the dispatch ctx times out.
func TestE2EBigStdoutNoTruncationIntegration(t *testing.T) {
	const lines = 200
	code := fmt.Sprintf(`
for i in range(%d):
    print("X" * 500 + " :" + str(i))
`, lines)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sink := runPython(t, ctx, code)
	got := sink.messagesFor("stdout")
	if len(got) != lines {
		t.Fatalf("expected %d stdout lines (matching the loop), got %d — "+
			"pipe back-pressure may have lost lines", lines, len(got))
	}
}

// TestE2ESigtermDrainsPipeIntegration: while a long-running task is producing
// output, cancel its context (which sends SIGKILL via exec.CommandContext) and
// assert that the lines the child already flushed BEFORE the kill made it to
// the sink. Pins the OS-pipe-drain guarantee.
func TestE2ESigtermDrainsPipeIntegration(t *testing.T) {
	code := `
import time, sys
print("CANARIO_BEFORE_SLEEP")
sys.stdout.flush()
time.sleep(60)  # killed by ctx timeout below
`
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	sink := runPython(t, ctx, code)
	if !anyContains(sink.messagesFor("stdout"), "CANARIO_BEFORE_SLEEP") {
		t.Fatalf("pre-kill print did not reach the sink — exec.Cmd.Wait should drain "+
			"the OS pipe before reporting completion. Captured: %+v", sink.lines)
	}
}

// anyContains reports whether any string in haystack contains needle.
// Named to avoid colliding with `contains` in returnvalue_test.go.
func anyContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
