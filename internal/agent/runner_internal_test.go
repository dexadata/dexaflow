package agent

import (
	"errors"
	"slices"
	"strings"
	"testing"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

func TestClampExit(t *testing.T) {
	cases := map[int]int32{
		0: 0, 1: 1, 137: 137, 255: 255,
		-1: 255, -255: 255, // negative is out of range -> clamp
		256: 255, 4096: 255, // above a byte -> clamp
	}
	for in, want := range cases {
		if got := clampExit(in); got != want {
			t.Errorf("clampExit(%d) = %d, want %d", in, got, want)
		}
	}
}

// TestClampExitOfSignalDeathIsProduction255 writes down the exit code the
// execution_timeout path actually reports, because nothing else does: the fakes
// invent their own. exec.CommandContext's cancel kills the child, ProcessState
// reports -1 for a signal death, execRunner passes that -1 through, and the clamp
// turns it into 255. So an operator greps for 255 on a timed-out attempt — not
// the 137 that a SIGKILL delivered from outside the process would produce.
func TestClampExitOfSignalDeathIsProduction255(t *testing.T) {
	if got := clampExit(-1); got != 255 {
		t.Errorf("clampExit(-1) = %d, want 255 — the timeout path's real exit code", got)
	}
}

func TestMergeEnvSortsSpecDeterministically(t *testing.T) {
	got := mergeEnv(
		[]string{"BASE=1"},
		map[string]string{"ZED": "26", "ALPHA": "1", "MID": "13"},
		[]string{"XCOM=x"},
	)
	want := []string{"BASE=1", "ALPHA=1", "MID=13", "ZED=26", "XCOM=x"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("mergeEnv = %v, want %v (base, sorted spec, then xcom)", got, want)
	}
	// Empty spec/xcom just returns the base.
	if g := mergeEnv([]string{"A=1"}, nil, nil); len(g) != 1 || g[0] != "A=1" {
		t.Errorf("mergeEnv with empty spec/xcom = %v", g)
	}
}

// capSink records full log lines (message + line number).
type capSink struct {
	msgs  []string
	lines []int64
}

func (s *capSink) Send(l *agentv1.LogLine) error {
	s.msgs = append(s.msgs, l.GetMessage())
	s.lines = append(s.lines, l.GetLineNumber())
	return nil
}
func (s *capSink) Close() error { return nil }

func TestLogWriterSplitsLines(t *testing.T) {
	sink := &capSink{}
	w := &logWriter{sink: sink, stream: "stdout", level: agentv1.LogLevel_LOG_LEVEL_INFO}

	// Two complete lines in one write, plus a partial line with no newline.
	n, err := w.Write([]byte("first\nsecond\npart"))
	if err != nil || n != len("first\nsecond\npart") {
		t.Fatalf("Write returned n=%d err=%v", n, err)
	}
	if strings.Join(sink.msgs, "|") != "first|second" {
		t.Errorf("complete lines should emit immediately, got %v", sink.msgs)
	}
	// A second write completes the partial line.
	if _, err := w.Write([]byte("ial\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sink.msgs, "|") != "first|second|partial" {
		t.Errorf("partial line should complete across writes, got %v", sink.msgs)
	}
	// Line numbers are monotonic from 1.
	for i, ln := range sink.lines {
		if ln != int64(i+1) {
			t.Errorf("line %d numbered %d, want %d", i, ln, i+1)
		}
	}
}

func TestLogWriterFlushEmitsTrailingPartial(t *testing.T) {
	sink := &capSink{}
	w := &logWriter{sink: sink}
	if _, err := w.Write([]byte("no newline here")); err != nil {
		t.Fatal(err)
	}
	if len(sink.msgs) != 0 {
		t.Errorf("a partial line must not emit until flush/newline, got %v", sink.msgs)
	}
	w.flush()
	if len(sink.msgs) != 1 || sink.msgs[0] != "no newline here" {
		t.Errorf("flush should emit the buffered remainder, got %v", sink.msgs)
	}
	// Flushing again with an empty buffer is a no-op.
	w.flush()
	if len(sink.msgs) != 1 {
		t.Errorf("flushing an empty buffer should do nothing, got %v", sink.msgs)
	}
}

// errSink fails every Send, simulating a broken log stream.
type errSink struct{ sends int }

func (s *errSink) Send(*agentv1.LogLine) error { s.sends++; return errors.New("stream broken") }
func (s *errSink) Close() error                { return nil }

// TestLogWriterSurvivesSinkErrors breaks the log sink: a streaming failure must
// not break the writer (the task keeps running; logs are best-effort).
func TestLogWriterSurvivesSinkErrors(t *testing.T) {
	sink := &errSink{}
	w := &logWriter{sink: sink}
	if _, err := w.Write([]byte("a\nb\n")); err != nil {
		t.Fatalf("Write must not surface sink errors, got %v", err)
	}
	w.flush()
	if sink.sends != 2 {
		t.Errorf("both lines should have been attempted, got %d sends", sink.sends)
	}
}

// The agent runs inside the task's own pod, so its environment IS the task's
// environment unless something removes the difference. LEOFLOW_AGENT_TOKEN is a
// bearer credential that authorizes GetVariables and GetConnections for the whole
// tenant, and identify() checks only its signature — so any task that can read it
// can fetch every decrypted connection URI, for the token's full lifetime, whether
// or not the task is still running.
//
// This is upstream of scoping secret delivery: a task handed only its own
// connections can read the token and ask for the rest directly (#476, #59).
func TestMergeEnvStripsTheAgentToken(t *testing.T) {
	got := mergeEnv(
		[]string{"PATH=/usr/bin", "LEOFLOW_AGENT_TOKEN=eyJhbGciOi.secret.sig", "HOME=/tmp"},
		nil, nil)
	for _, kv := range got {
		if strings.HasPrefix(kv, "LEOFLOW_AGENT_TOKEN=") {
			t.Fatalf("the agent token reached the task environment: %q", kv)
		}
	}
	// Everything the task legitimately needs must survive the filter.
	if !slices.Contains(got, "PATH=/usr/bin") || !slices.Contains(got, "HOME=/tmp") {
		t.Fatalf("the filter removed unrelated variables: %v", got)
	}
}

// The control-plane address is dialed by the agent, never by the task. Leaving it
// hands user code the endpoint to point a stolen or forged credential at.
func TestMergeEnvStripsTheControlPlaneAddress(t *testing.T) {
	got := mergeEnv([]string{"LEOFLOW_CONTROL_PLANE_ADDR=leoflow:9091"}, nil, nil)
	if len(got) != 0 {
		t.Fatalf("the control-plane address reached the task environment: %v", got)
	}
}

// The variables the agent *injects* for the runtime are added by mergeEnv's later
// arguments, not inherited from os.Environ, so they must pass through untouched.
// A filter that caught them would break XCom, TaskFlow args and the staging path.
func TestMergeEnvKeepsInjectedRuntimeVariables(t *testing.T) {
	got := mergeEnv(
		[]string{"LEOFLOW_STAGING_DIR=/staging", "LEOFLOW_TASK_INSTANCE_ID=ti-1"},
		map[string]string{"AIRFLOW_VAR_GREETING": "hi"},
		[]string{"LEOFLOW_XCOM_VALUE=42"},
	)
	for _, want := range []string{
		"LEOFLOW_STAGING_DIR=/staging", // user code reads this
		"LEOFLOW_TASK_INSTANCE_ID=ti-1",
		"AIRFLOW_VAR_GREETING=hi",
		"LEOFLOW_XCOM_VALUE=42",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q from %v", want, got)
		}
	}
}

// In Lite the agent is spawned by the server as a subprocess, inheriting the
// SERVER's environment (internal/executor/subprocess.go:161 appends to
// os.Environ()). That environment holds the AES key encrypting connections at
// rest and the HMAC secret signing every user and agent token. The signing
// secret is the worse of the two: with it, task code mints an admin token.
//
// A denylist naming today's secrets would not have caught these, and would not
// catch tomorrow's. Within the LEOFLOW_ prefix the filter is an allowlist, so a
// variable added later is stripped by default rather than leaked by default.
func TestMergeEnvStripsServerSecretsInheritedInLite(t *testing.T) {
	got := mergeEnv([]string{
		"LEOFLOW_SECRET_KEY=0123456789abcdef0123456789abcdef", // gitleaks:allow — fixture; the point is the KEY NAME, and any realistic value trips generic-api-key
		"LEOFLOW_AUTH_JWT_SECRET=hmac-signing-secret",
		"LEOFLOW_DATABASE_URL=postgres://leoflow:hunter2@db/leoflow",
		"LEOFLOW_REDIS_URL=redis://cache:6379/0",
		"LEOFLOW_BOOTSTRAP_PASSWORD=admin",
	}, nil, nil)
	if len(got) != 0 {
		t.Fatalf("server secrets reached the task environment: %v", got)
	}
}

// A LEOFLOW_ variable nobody has thought of yet must not reach task code either.
// This is the property a denylist cannot have.
func TestMergeEnvStripsUnknownLeoflowVariablesByDefault(t *testing.T) {
	got := mergeEnv([]string{"LEOFLOW_SOME_FUTURE_CREDENTIAL=shhh"}, nil, nil)
	if len(got) != 0 {
		t.Fatalf("an unrecognized LEOFLOW_ variable was passed through: %v", got)
	}
}
