package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dexadata/dexaflow/internal/agent/secretsource"
	"github.com/dexadata/dexaflow/internal/taskoutcome"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// outageRegisterClient answers Register with a transient code forever: the
// control plane restarted between the dial and the agent's first unary call.
type outageRegisterClient struct {
	*fakeClient
	code codes.Code
}

func (c outageRegisterClient) Register(context.Context, *agentv1.RegisterRequest, ...grpc.CallOption) (*agentv1.RegisterResponse, error) {
	return nil, status.Error(c.code, "injected: control plane restarting")
}

// noFailedReport fails the test when the agent reported FAILED: a task whose
// code never ran because the control plane did not answer is not the task's
// failure, and a FAILED report would settle it as one before the reconciler
// reads the record.
func noFailedReport(t *testing.T, base *fakeClient) {
	t.Helper()
	for _, s := range base.states {
		if s == agentv1.TaskState_TASK_STATE_FAILED {
			t.Errorf("reported states %v: a start the control plane never answered must not be reported FAILED", base.states)
		}
	}
}

// TestStartWithoutControlPlaneIsUnexecutable is R-01's third item (#1594). An
// outage longer than the start budget used to end the attempt as the user's
// failure: a spec read that never succeeded left no record, so the reconciler
// read a failed pod and spent a retry; an upstream read that never succeeded
// was reported FAILED. The task's code never ran, so the substrate could not
// run it (ADR 0051, Unexecutable): the agent now writes a record that puts the
// attempt on the infra rail, with a fixed reason and no exit code, and reports
// nothing the control plane would settle as the task's failure.
func TestStartWithoutControlPlaneIsUnexecutable(t *testing.T) {
	spec := func() *agentv1.TaskSpec { return &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"} }
	cases := []struct {
		name  string
		setup func(r *Runner, base *fakeClient)
	}{
		{"register", func(r *Runner, base *fakeClient) {
			r.Client = outageRegisterClient{fakeClient: base, code: codes.Unavailable}
		}},
		{"spec read, Unavailable", func(r *Runner, base *fakeClient) {
			r.Client = newFlakyClient(base, codes.Unavailable, map[string]int{"GetTaskSpec": 1 << 30})
		}},
		{"spec read, Internal from an older control plane", func(r *Runner, base *fakeClient) {
			r.Client = newFlakyClient(base, codes.Internal, map[string]int{"GetTaskSpec": 1 << 30})
		}},
		{"upstream XCom read", func(r *Runner, base *fakeClient) {
			base.spec.XcomInputMapping = map[string]*agentv1.XComUpstreams{"x": {TaskIds: []string{"up"}}}
			base.xcom = map[string]*agentv1.FetchXComResponse{"up": {Value: []byte(`1`)}}
			r.Client = newFlakyClient(base, codes.Unavailable, map[string]int{"FetchXCom": 1 << 30})
		}},
		{"RUNNING report", func(_ *Runner, base *fakeClient) {
			base.reportFailCode, base.reportFailTimes = codes.Internal, 1<<30
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &fakeClient{spec: spec()}
			cmd := &fakeCmd{exitCode: 0}
			r := newRunner(base, cmd, &recordingSink{})
			r.TerminationLogPath = filepath.Join(t.TempDir(), "termination-log")
			tc.setup(r, base)

			if err := r.Run(context.Background()); err == nil {
				t.Fatal("Run succeeded against a control plane that never answered")
			}
			if cmd.argv != nil {
				t.Fatal("user code ran")
			}
			rec := readOutcome(t, r.TerminationLogPath)
			if rec.Outcome != taskoutcome.Failed || !rec.Infra {
				t.Errorf("record = %+v, want a failure carrying the infra marker", rec)
			}
			if rec.ExitCode != nil {
				t.Errorf("record exit code = %d, want none: no user process ran", *rec.ExitCode)
			}
			if !rec.AgentAuthored() || rec.Reason != reasonControlPlaneUnavailable {
				t.Errorf("record reason = %q, want the fixed classification %q", rec.Reason, reasonControlPlaneUnavailable)
			}
			if strings.Contains(rec.Reason, "injected") {
				t.Errorf("record reason %q carries the error's text", rec.Reason)
			}
			noFailedReport(t, base)
		})
	}
}

// TestStartFailureThatIsNotAnOutageStaysOffTheInfraRail: only the control
// plane not answering is the substrate's fault. A logical refusal of the spec
// read, and an external secret backend that is down (whose own gRPC error is
// Unavailable too), keep today's settle: no infra marker.
func TestStartFailureThatIsNotAnOutageStaysOffTheInfraRail(t *testing.T) {
	t.Run("spec read refused", func(t *testing.T) {
		base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"}}
		r := newRunner(base, &fakeCmd{}, &recordingSink{})
		r.Client = newFlakyClient(base, codes.PermissionDenied, map[string]int{"GetTaskSpec": 1 << 30})
		r.TerminationLogPath = filepath.Join(t.TempDir(), "termination-log")

		if err := r.Run(context.Background()); err == nil {
			t.Fatal("Run succeeded")
		}
		if _, err := os.Stat(r.TerminationLogPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a refused spec read wrote a record (stat err %v); want none, as before", err)
		}
	})
	t.Run("external secret backend unavailable", func(t *testing.T) {
		base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t", DeclaredVariables: []string{"x"}}}
		r := newRunner(base, &fakeCmd{}, &recordingSink{})
		r.Resolver = fakeResolver{err: status.Error(codes.Unavailable, "secret manager down")}
		r.SecretBackend = secretsource.Backend{Variables: true}
		r.TerminationLogPath = filepath.Join(t.TempDir(), "termination-log")

		if err := r.Run(context.Background()); err == nil {
			t.Fatal("Run succeeded")
		}
		rec := readOutcome(t, r.TerminationLogPath)
		if rec.Infra || rec.Reason != reasonSecretUnresolved {
			t.Errorf("record = %+v, want the secret classification and no infra marker", rec)
		}
		if got := lastState(t, base); got != agentv1.TaskState_TASK_STATE_FAILED {
			t.Errorf("final state = %v, want FAILED", got)
		}
	})
}

// TestWarmAttemptStartOutageIsStillReported: a warm worker serves many
// attempts from one pod, so no reconciler reads a per-attempt record from its
// termination message. Its attempt keeps reporting FAILED, the only settle it
// has, and writes no infra record over the worker's.
func TestWarmAttemptStartOutageIsStillReported(t *testing.T) {
	base := &fakeClient{spec: &agentv1.TaskSpec{
		Operator: "python", Entrypoint: "dag:t",
		XcomInputMapping: map[string]*agentv1.XComUpstreams{"x": {TaskIds: []string{"up"}}},
	}}
	path := filepath.Join(t.TempDir(), "termination-log")
	w := &WarmRunner{
		WorkClient:         newFlakyClient(base, codes.Unavailable, map[string]int{"FetchXCom": 1 << 30}),
		Cmd:                &fakeCmd{},
		ScratchDir:         t.TempDir(),
		TerminationLogPath: path,
	}
	r := withFakeClock(w.attemptRunner(&recordingSink{}))

	if err := r.runOneAttempt(context.Background()); err == nil {
		t.Fatal("runOneAttempt succeeded")
	}
	if got := lastState(t, base); got != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("final state = %v, want FAILED", got)
	}
	if data, err := os.ReadFile(path); err == nil {
		if rec, ok := taskoutcome.Decode(string(data)); ok && rec.Infra {
			t.Errorf("warm attempt wrote an infra record: %q", data)
		}
	}
}
