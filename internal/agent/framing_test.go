package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// linesSink captures whole LogLine values so a framing test can assert message,
// level, and stream (capSink only records the message + line number). When err
// is non-nil Send returns it (so the framing helpers' "log-and-continue" error
// path is exercised).
type linesSink struct {
	ls  []*agentv1.LogLine
	err error
}

func (s *linesSink) Send(l *agentv1.LogLine) error {
	s.ls = append(s.ls, l)
	return s.err
}
func (s *linesSink) Close() error { return nil }

// TestFramingSurvivesSinkErrors exercises the log-and-continue branch on the
// three framing helpers. A sink returning an error must NOT propagate — the
// framing is purely cosmetic, and propagating would mean a flaky log shipper
// could make a task that succeeded look as if it failed. This locks that
// behavior; the runtime can keep going on a degraded sink (the comment on
// each helper spells it out).
func TestFramingSurvivesSinkErrors(t *testing.T) {
	want := errors.New("sink briefly unreachable")
	s := &linesSink{err: want}
	// All three helpers swallow the error.
	emitTaskStarted(s)
	emitTaskEnded(s, 0, nil, time.Second)
	emitTaskBoot(s, []string{"python3", "-u", "task.py"}, []string{"AIRFLOW_VAR_GREETING=hi"})
	// Each helper appended its line via the recorder BEFORE Send returned the
	// error; the count proves no helper short-circuited on its own.
	if got := len(s.ls); got < 3 {
		t.Errorf("framing helpers did not all attempt a Send (got %d, want >= 3)", got)
	}
}

// TestEmitTaskStarted writes a synthetic "▸ task started" line at INFO with
// stream "agent" — so the UI's Logs panel is never empty even for a task that
// calls no print() (#119).
func TestEmitTaskStarted(t *testing.T) {
	s := &linesSink{}
	emitTaskStarted(s)
	if len(s.ls) != 1 {
		t.Fatalf("emitTaskStarted must send exactly 1 line, got %d", len(s.ls))
	}
	l := s.ls[0]
	if !strings.Contains(l.GetMessage(), "task started") {
		t.Errorf("message must mark the start, got %q", l.GetMessage())
	}
	if l.GetLevel() != agentv1.LogLevel_LOG_LEVEL_INFO {
		t.Errorf("start framing should be INFO, got %v", l.GetLevel())
	}
	if l.GetStream() != "agent" {
		t.Errorf("framing must use stream=%q so the UI logger column shows task.agent, got %q", "agent", l.GetStream())
	}
}

// TestEmitTaskEndedSuccess writes an INFO "✓ task succeeded in <dur>" line on a
// clean exit, so the panel always shows the terminal state.
func TestEmitTaskEndedSuccess(t *testing.T) {
	s := &linesSink{}
	emitTaskEnded(s, 0, nil, 1234*time.Millisecond)
	if len(s.ls) != 1 {
		t.Fatalf("emitTaskEnded must send exactly 1 line, got %d", len(s.ls))
	}
	l := s.ls[0]
	if !strings.Contains(l.GetMessage(), "succeeded") || !strings.Contains(l.GetMessage(), "1.234s") {
		t.Errorf("success message must say succeeded with the duration, got %q", l.GetMessage())
	}
	if l.GetLevel() != agentv1.LogLevel_LOG_LEVEL_INFO {
		t.Errorf("success framing should be INFO, got %v", l.GetLevel())
	}
}

// TestEmitTaskEndedFailureWithCause writes an ERROR line with the exit code and
// the wrapping cause, so a failing task always shows the reason — even when the
// user task printed nothing before the failure.
func TestEmitTaskEndedFailureWithCause(t *testing.T) {
	s := &linesSink{}
	emitTaskEnded(s, 2, errors.New("boom"), 500*time.Millisecond)
	if len(s.ls) != 1 {
		t.Fatalf("emitTaskEnded must send exactly 1 line, got %d", len(s.ls))
	}
	l := s.ls[0]
	for _, want := range []string{"failed", "exit 2", "boom", "500ms"} {
		if !strings.Contains(l.GetMessage(), want) {
			t.Errorf("failure message must mention %q, got %q", want, l.GetMessage())
		}
	}
	if l.GetLevel() != agentv1.LogLevel_LOG_LEVEL_ERROR {
		t.Errorf("failure framing should be ERROR, got %v", l.GetLevel())
	}
}

// TestEmitTaskEndedFailureNoCause covers a non-zero exit with no Go-level error
// (the subprocess exited badly but Cmd.Run returned nil): still ERROR, still
// mentions the exit code and duration.
func TestEmitTaskEndedFailureNoCause(t *testing.T) {
	s := &linesSink{}
	emitTaskEnded(s, 7, nil, 100*time.Millisecond)
	if len(s.ls) != 1 {
		t.Fatalf("expected 1 line, got %d", len(s.ls))
	}
	l := s.ls[0]
	if !strings.Contains(l.GetMessage(), "failed") || !strings.Contains(l.GetMessage(), "exit 7") {
		t.Errorf("non-zero exit without cause must still be flagged failed with the exit code, got %q", l.GetMessage())
	}
	if l.GetLevel() != agentv1.LogLevel_LOG_LEVEL_ERROR {
		t.Errorf("non-zero exit framing should be ERROR, got %v", l.GetLevel())
	}
}
