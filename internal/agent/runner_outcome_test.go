package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// readOutcome reads and decodes the durable outcome record the agent wrote to its
// termination-log path, failing the test if it is absent or undecodable.
func readOutcome(t *testing.T, path string) taskoutcome.Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("termination log not written: %v", err)
	}
	rec, ok := taskoutcome.Decode(string(data))
	if !ok {
		t.Fatalf("termination log is not a decodable outcome record: %q", data)
	}
	return rec
}

// TestRunnerWritesSuccessOutcome: a task that succeeds leaves a `success` record
// in the termination log, so a reconciler can recover the success even if the
// report is lost (ADR 0052). The success record is written only after the
// pre-report pushes, since report(SUCCESS) is the last step of the terminal path.
func TestRunnerWritesSuccessOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:ok"}}
	r := newRunner(client, &fakeCmd{exitCode: 0}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec := readOutcome(t, path); rec.Outcome != taskoutcome.Success {
		t.Errorf("outcome = %q, want success", rec.Outcome)
	}
}

// TestRunnerWritesFailedOutcome: a task that exits non-zero leaves a `failed`
// record carrying the exit code.
func TestRunnerWritesFailedOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:boom"}}
	r := newRunner(client, &fakeCmd{exitCode: 1}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a non-zero exit must fail")
	}
	rec := readOutcome(t, path)
	if rec.Outcome != taskoutcome.Failed {
		t.Errorf("outcome = %q, want failed", rec.Outcome)
	}
	if rec.ExitCode == nil || *rec.ExitCode != 1 {
		t.Errorf("exit_code = %v, want 1", rec.ExitCode)
	}
}

// TestRunnerWritesFailedOutcomeOnTimeout pins that the execution-timeout sink
// (failWithReason, not fail) also leaves a durable record. The record is keyed off
// the reported state inside report(), so every failure sink is covered — the exact
// gap a "write before each fail()" approach would miss (ADR 0052 review).
func TestRunnerWritesFailedOutcomeOnTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator:                "bash",
		Entrypoint:              "sleep 1000",
		ExecutionTimeoutSeconds: 1,
	}}
	r := newRunner(client, &fakeCmd{blockUntilCancel: true}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a task exceeding its execution timeout must fail")
	}
	if rec := readOutcome(t, path); rec.Outcome != taskoutcome.Failed {
		t.Errorf("timeout outcome = %q, want failed", rec.Outcome)
	}
}

// TestRunnerWritesRescheduleOutcome: a reschedule-mode sensor leaves a
// `reschedule` record carrying the next-poke time, so a lost report can still be
// settled as up_for_reschedule with the real time rather than an invented one.
func TestRunnerWritesRescheduleOutcome(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "reschedule.txt")
	if err := os.WriteFile(rp, []byte("2099-01-02T03:04:05+00:00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:sensor"}}
	r := newRunner(client, &fakeCmd{exitCode: rescheduleExitCode}, &recordingSink{})
	r.ReschedulePath = rp
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: reschedule must not surface as an error: %v", err)
	}
	rec := readOutcome(t, path)
	if rec.Outcome != taskoutcome.Reschedule {
		t.Fatalf("outcome = %q, want reschedule", rec.Outcome)
	}
	when, ok := rec.At()
	if !ok {
		t.Fatal("reschedule record must carry a parseable next-poke time")
	}
	if got := when.UTC().Format("2006-01-02T15:04:05Z"); got != "2099-01-02T03:04:05Z" {
		t.Errorf("reschedule_at = %s, want 2099-01-02T03:04:05Z", got)
	}
}

// TestRunnerWritesOutcomeBeforeReport is the durability property: the record is on
// disk even when the report delivery itself fails. This is the whole point — a pod
// killed mid-report still leaves the truth behind.
func TestRunnerWritesOutcomeBeforeReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{
		spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:ok"},
		// Let the RUNNING report land, then lose the terminal SUCCESS report —
		// exactly the pod-killed-mid-report case the record exists to survive.
		failReportState: agentv1.TaskState_TASK_STATE_SUCCESS,
	}
	r := newRunner(client, &fakeCmd{exitCode: 0}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a failed report must surface as an error")
	}
	// Even though the report never landed, the outcome is durable.
	if rec := readOutcome(t, path); rec.Outcome != taskoutcome.Success {
		t.Errorf("outcome = %q, want success recorded before the (failed) report", rec.Outcome)
	}
}

// TestRunnerSuccessPathPushFailureWritesFailedRecord locks the write ordering the
// design leans on: a task whose user code exited 0 but whose pre-report XCom push
// fails routes to fail(), which must leave a FAILED record — never a stale SUCCESS.
// A recovered success must only ever be written after the pushes were accepted.
func TestRunnerSuccessPathPushFailureWritesFailedRecord(t *testing.T) {
	dir := t.TempDir()
	returnPath := filepath.Join(dir, "return.json")
	if err := os.WriteFile(returnPath, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "termination-log")
	client := &fakeClient{
		spec:    &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:ok"},
		pushErr: errors.New("xcom backend down"), // a non-Unimplemented push failure
	}
	r := newRunner(client, &fakeCmd{exitCode: 0}, &recordingSink{})
	r.ReturnPath = returnPath
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a failed pre-report push must fail the task")
	}
	if rec := readOutcome(t, path); rec.Outcome != taskoutcome.Failed {
		t.Errorf("a push failure on the success path must write a FAILED record, not a stale success, got %q", rec.Outcome)
	}
}

// TestRunnerBeforeReportHookFiresAfterRecordBeforeReport locks the fault-injection
// seam the ADR 0052 E2E relies on: BeforeReport is invoked with the terminal state
// after the durable record is on disk but before the report is delivered — so the
// E2E can simulate a pod killed mid-report with the record already written.
func TestRunnerBeforeReportHookFiresAfterRecordBeforeReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:ok"}}
	r := newRunner(client, &fakeCmd{exitCode: 0}, &recordingSink{})
	r.TerminationLogPath = path

	var gotState agentv1.TaskState
	var recordOnDisk, reportSeenYet bool
	r.BeforeReport = func(state agentv1.TaskState) {
		gotState = state
		if _, err := os.Stat(path); err == nil {
			recordOnDisk = true
		}
		// Only the RUNNING report has been sent by now; the terminal SUCCESS report
		// is what this hook precedes.
		for _, rep := range client.reports {
			if rep.GetState() == agentv1.TaskState_TASK_STATE_SUCCESS {
				reportSeenYet = true
			}
		}
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotState != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("hook state = %v, want SUCCESS", gotState)
	}
	if !recordOnDisk {
		t.Error("the outcome record must be on disk before BeforeReport fires")
	}
	if reportSeenYet {
		t.Error("the SUCCESS report must NOT have been delivered before BeforeReport fires")
	}
}

// TestRunnerRunningStateWritesNoRecord: a non-terminal RUNNING report must never
// write an outcome record — only the terminal states carry a durable outcome.
func TestRunnerRunningStateWritesNoRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	r := newRunner(&fakeClient{}, &fakeCmd{}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.report(context.Background(), agentv1.TaskState_TASK_STATE_RUNNING, 0, ""); err != nil {
		t.Fatalf("report RUNNING: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a RUNNING report must not write an outcome record")
	}
}

// TestRunnerNoTerminationLogWhenPathUnset: Lite (subprocess, no pod, in-process
// report) sets no termination-log path and must be entirely unaffected — no file,
// no error.
func TestRunnerNoTerminationLogWhenPathUnset(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:ok"}}
	r := newRunner(client, &fakeCmd{exitCode: 0}, &recordingSink{})
	// TerminationLogPath left empty.
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run with no termination-log path must be unaffected: %v", err)
	}
}

// TestRunnerTimeoutOutcomeRecordCarriesReason is the airtight half of the
// execution_timeout guarantee (#930). #925 made the agent's clock fire before the
// kubelet's, so the agent produces the diagnosis — but the diagnosis only reached
// the operator through the REPORT. When the control plane is unreachable across
// the timeout, or the kubelet's SIGTERM lands mid-retry, the report never arrives
// and the reconciler renders the generic "task failed (exit N)" from the durable
// record. The record must carry the classification too, and keep the exit code:
// both matter to the operator.
func TestRunnerTimeoutOutcomeRecordCarriesReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator:                "bash",
		Entrypoint:              "sleep 1000",
		ExecutionTimeoutSeconds: 1,
	}}
	r := newRunner(client, &fakeCmd{blockUntilCancel: true}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a task exceeding its execution timeout must fail")
	}
	rec := readOutcome(t, path)
	if rec.Outcome != taskoutcome.Failed {
		t.Fatalf("timeout outcome = %q, want failed", rec.Outcome)
	}
	if !strings.Contains(rec.Reason, "execution_timeout") {
		t.Errorf("record reason = %q, want it to name execution_timeout", rec.Reason)
	}
	if rec.ExitCode == nil || *rec.ExitCode != 137 {
		t.Errorf("exit_code = %v, want the 137 this fake command runner reported, clamped, "+
			"kept alongside the reason. 137 is the fake's own invention, NOT the production "+
			"value: see TestClampExitOfSignalDeathIsProduction255", rec.ExitCode)
	}
}

// TestRunnerNonZeroExitOutcomeRecordCarriesNoReason is the other side of the
// narrow scope (#930): an ordinary non-zero exit leaves NO classification. Its
// message is either a restatement of the exit code or raw error text whose detail
// lives in the logs, and the record's own "task failed (exit N)" rendering is the
// better operator string — so the reason field stays reserved for a diagnosis the
// control plane cannot otherwise derive.
func TestRunnerNonZeroExitOutcomeRecordCarriesNoReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:boom"}}
	r := newRunner(client, &fakeCmd{exitCode: 3, err: errors.New("boom")}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a non-zero exit must fail")
	}
	if rec := readOutcome(t, path); rec.Reason != "" {
		t.Errorf("reason = %q, want empty for an ordinary non-zero exit", rec.Reason)
	}
}

// TestRunnerXComFetchErrorOutcomeRecordCarriesNoReason is the negative half of
// the classification contract (#930). An environment-build failure the agent
// cannot classify must leave the record's reason EMPTY rather than dumping the
// raw error into it: the XCom fetch wraps a gRPC error that can carry the
// control-plane endpoint and TLS handshake text, and the record is durable and
// readable by anyone with pod read access in the task namespace.
func TestRunnerXComFetchErrorOutcomeRecordCarriesNoReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator:   "python",
			Entrypoint: "dag:consume",
			XcomInputMapping: map[string]*agentv1.XComUpstreams{
				"payload": {TaskIds: []string{"producer"}},
			},
		},
		fetchXComErr: status.Error(codes.Unavailable,
			`connection error: desc = "transport: authentication handshake failed: `+
				`tls: failed to verify certificate for leoflow-grpc.leoflow.svc:9090"`),
	}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.TerminationLogPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("an XCom fetch failure must fail the task")
	}
	rec := readOutcome(t, path)
	if rec.Reason != "" {
		t.Errorf("record reason = %q, want empty: an unclassified environment-build "+
			"failure must not put raw error text on the durable record", rec.Reason)
	}
	if rec.Outcome != taskoutcome.Failed {
		t.Errorf("outcome = %q, want failed", rec.Outcome)
	}
}

// TestRunnerReturnValuePushFailureRecordDoesNotRenderBareExitCode: the user
// process exited 0 and only the delivery of its outputs failed, so a reason-less
// record renders (executor.recordFailureReason) as "task failed (exit 0)" — a
// string that reads as a success and names no cause. This failure is one the
// agent diagnoses itself, so it carries its own classification (#930).
// TestRunnerOutputPushFailureRecordsTheClassification covers all THREE
// post-task push sites, not just the return value. They share one constant and
// one shape — the user's code has already exited 0, so an unclassified record
// renders as a failure naming exit code 0, which reads as a success and names
// no cause — and a future edit that classifies one and forgets another would
// otherwise go unnoticed. Extra links in particular are neither a return value
// nor an XCom, which is why the constant names all three.
func TestRunnerOutputPushFailureRecordsTheClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		body  string
	}{
		{"return value", "return", `{"x":1}`},
		{"extra links", "links", `[{"name":"run","url":"https://example.invalid"}]`},
		{"custom xcoms", "pushes", `{"k":"v"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, tc.field+".json")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "termination-log")
			client := &fakeClient{
				spec:    &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:ok"},
				pushErr: errors.New("xcom backend down"),
			}
			r := newRunner(client, &fakeCmd{exitCode: 0}, &recordingSink{})
			switch tc.field {
			case "return":
				r.ReturnPath = p
			case "links":
				r.LinksPath = p
			case "pushes":
				r.PushesPath = p
			}
			r.TerminationLogPath = path

			if err := r.Run(context.Background()); err == nil {
				t.Fatalf("%s: a failed pre-report push must fail the task", tc.name)
			}
			rec := readOutcome(t, path)
			if rec.Reason != reasonOutputUndelivered {
				t.Errorf("%s: record reason = %q, want the classification constant %q — otherwise the "+
					"reconciler renders the bare %q", tc.name, rec.Reason, reasonOutputUndelivered, "task failed (exit 0)")
			}
			if strings.Contains(rec.Reason, "xcom backend down") {
				t.Errorf("%s: record reason leaks the raw push error: %q", tc.name, rec.Reason)
			}
		})
	}
}

// TestRunnerRecordNamesTheExecution: the record carries the attempt epoch the
// task spec names (ADR 0052 amendment), so the reconciler can tell it from a
// record of another execution of the same try. Epoch 0 is left out.
func TestRunnerRecordNamesTheExecution(t *testing.T) {
	for epoch, want := range map[int64]*int64{4: ptrInt64(4), 0: nil} {
		path := filepath.Join(t.TempDir(), "termination-log")
		client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:ok", AttemptEpoch: epoch}}
		r := newRunner(client, &fakeCmd{exitCode: 0}, &recordingSink{})
		r.TerminationLogPath = path
		if err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		rec := readOutcome(t, path)
		if (want == nil) != (rec.AttemptEpoch == nil) || (want != nil && *rec.AttemptEpoch != *want) {
			t.Errorf("spec epoch %d: record epoch = %v, want %v", epoch, rec.AttemptEpoch, want)
		}
	}
}

func ptrInt64(n int64) *int64 { return &n }
