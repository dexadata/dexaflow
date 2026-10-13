package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// flakyClient fails the first failures[method] calls of a method with code,
// then defers to the embedded fakeClient. It plays a control plane whose
// database or Redis blipped (R-01, #1594).
type flakyClient struct {
	*fakeClient
	code     codes.Code
	failures map[string]int
	calls    map[string]int
}

func newFlakyClient(base *fakeClient, code codes.Code, failures map[string]int) *flakyClient {
	return &flakyClient{fakeClient: base, code: code, failures: failures, calls: map[string]int{}}
}

func (f *flakyClient) fail(method string) error {
	f.calls[method]++
	if f.calls[method] <= f.failures[method] {
		return status.Error(f.code, "injected: control plane store unavailable")
	}
	return nil
}

func (f *flakyClient) GetTaskSpec(ctx context.Context, in *agentv1.GetTaskSpecRequest, opts ...grpc.CallOption) (*agentv1.TaskSpec, error) {
	if err := f.fail("GetTaskSpec"); err != nil {
		return nil, err
	}
	return f.fakeClient.GetTaskSpec(ctx, in, opts...)
}

func (f *flakyClient) PushXCom(ctx context.Context, in *agentv1.PushXComRequest, opts ...grpc.CallOption) (*agentv1.PushXComResponse, error) {
	if err := f.fail("PushXCom"); err != nil {
		return nil, err
	}
	return f.fakeClient.PushXCom(ctx, in, opts...)
}

func (f *flakyClient) FetchXCom(ctx context.Context, in *agentv1.FetchXComRequest, opts ...grpc.CallOption) (*agentv1.FetchXComResponse, error) {
	if err := f.fail("FetchXCom"); err != nil {
		return nil, err
	}
	return f.fakeClient.FetchXCom(ctx, in, opts...)
}

func (f *flakyClient) GetVariables(ctx context.Context, in *agentv1.GetVariablesRequest, opts ...grpc.CallOption) (*agentv1.GetVariablesResponse, error) {
	if err := f.fail("GetVariables"); err != nil {
		return nil, err
	}
	return f.fakeClient.GetVariables(ctx, in, opts...)
}

func (f *flakyClient) GetConnections(ctx context.Context, in *agentv1.GetConnectionsRequest, opts ...grpc.CallOption) (*agentv1.GetConnectionsResponse, error) {
	if err := f.fail("GetConnections"); err != nil {
		return nil, err
	}
	return f.fakeClient.GetConnections(ctx, in, opts...)
}

// delayRecorder is an instant afterFunc that remembers every delay asked of
// it and advances clk by it.
type delayRecorder struct {
	clk    *fakeClock
	delays []time.Duration
}

func newDelayRecorder() *delayRecorder { return &delayRecorder{clk: newFakeClock()} }

func (d *delayRecorder) after(delay time.Duration) <-chan time.Time {
	d.delays = append(d.delays, delay)
	return d.clk.after(delay)
}

func (d *delayRecorder) total() time.Duration {
	var sum time.Duration
	for _, x := range d.delays {
		sum += x
	}
	return sum
}

func lastState(t *testing.T, base *fakeClient) agentv1.TaskState {
	t.Helper()
	if len(base.states) == 0 {
		t.Fatal("no state was reported")
	}
	return base.states[len(base.states)-1]
}

func writeReturnValue(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "return_value.json")
	if err := os.WriteFile(path, []byte(`{"rows": 42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A task whose user code exited 0 used to be reported FAILED when one push of
// its return value hit a transient error: the push was never retried, the
// successful task spent a user try, and the retry replayed its side effects.
func TestRunnerRetriesTransientReturnValuePush(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.Internal} {
		t.Run(code.String(), func(t *testing.T) {
			base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"}}
			c := newFlakyClient(base, code, map[string]int{"PushXCom": 1})
			r := newRunner(base, &fakeCmd{exitCode: 0}, &recordingSink{})
			r.Client = c
			r.ReturnPath = writeReturnValue(t)

			if err := r.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if c.calls["PushXCom"] != 2 {
				t.Errorf("PushXCom calls = %d, want 2 (one transient failure, one retry)", c.calls["PushXCom"])
			}
			if got := lastState(t, base); got != agentv1.TaskState_TASK_STATE_SUCCESS {
				t.Errorf("final state = %v, want SUCCESS", got)
			}
		})
	}
}

// One transient error on GetTaskSpec at pod start used to end the agent with
// an error and no report; the pod exited 1 and the reconciler settled a user
// failure for a task whose code never ran.
func TestRunnerRetriesTransientGetTaskSpec(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.Internal} {
		t.Run(code.String(), func(t *testing.T) {
			base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"}}
			c := newFlakyClient(base, code, map[string]int{"GetTaskSpec": 2})
			cmd := &fakeCmd{exitCode: 0}
			r := newRunner(base, cmd, &recordingSink{})
			r.Client = c

			if err := r.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if c.calls["GetTaskSpec"] != 3 {
				t.Errorf("GetTaskSpec calls = %d, want 3", c.calls["GetTaskSpec"])
			}
			if cmd.argv == nil {
				t.Error("user code never ran")
			}
			if got := lastState(t, base); got != agentv1.TaskState_TASK_STATE_SUCCESS {
				t.Errorf("final state = %v, want SUCCESS", got)
			}
		})
	}
}

// The control plane reported every store error as Internal, which the RUNNING
// report did not retry, so one database error before user code started ended
// the attempt and the reconciler settled a user failure.
func TestRunningReportRetriesInternal(t *testing.T) {
	base := &fakeClient{
		spec:           &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"},
		reportFailCode: codes.Internal, reportFailTimes: 1,
	}
	cmd := &fakeCmd{exitCode: 0}
	r := newRunner(base, cmd, &recordingSink{})

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if base.reportAttempts != 3 {
		t.Errorf("ReportState attempts = %d, want 3 (RUNNING failed once, RUNNING, SUCCESS)", base.reportAttempts)
	}
	if cmd.argv == nil {
		t.Error("user code never ran")
	}
	if got := lastState(t, base); got != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("final state = %v, want SUCCESS", got)
	}
}

// An upstream value read at task start retries a transient error too.
func TestRunnerRetriesTransientFetchXCom(t *testing.T) {
	base := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator: "python", Entrypoint: "dag:t",
			XcomInputMapping: map[string]*agentv1.XComUpstreams{"x": {TaskIds: []string{"up"}}},
		},
		xcom: map[string]*agentv1.FetchXComResponse{"up": {Value: []byte(`1`)}},
	}
	c := newFlakyClient(base, codes.Unavailable, map[string]int{"FetchXCom": 1})
	cmd := &fakeCmd{exitCode: 0}
	r := newRunner(base, cmd, &recordingSink{})
	r.Client = c

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c.calls["FetchXCom"] != 2 {
		t.Errorf("FetchXCom calls = %d, want 2", c.calls["FetchXCom"])
	}
	if got := lastState(t, base); got != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("final state = %v, want SUCCESS", got)
	}
}

// Variables and connections are best effort, but a transient error must not
// silently start the task without them.
func TestRunnerRetriesTransientSecretsFetch(t *testing.T) {
	base := &fakeClient{
		spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"},
		vars: map[string]string{"region": "eu"}, conns: map[string]string{"wh": "postgres://h/db"},
	}
	c := newFlakyClient(base, codes.Unavailable, map[string]int{"GetVariables": 1, "GetConnections": 1})
	cmd := &fakeCmd{exitCode: 0}
	r := newRunner(base, cmd, &recordingSink{})
	r.Client = c

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !hasEnv(cmd.env, "AIRFLOW_VAR_REGION=eu") {
		t.Error("the variable fetched after a retry did not reach the task")
	}
	if !hasEnv(cmd.env, "AIRFLOW_CONN_WH=postgres://h/db") {
		t.Error("the connection fetched after a retry did not reach the task")
	}
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// The retry is bounded: a control plane that keeps failing ends the attempt
// after about transientRetryBudget of backoff, never later, with jittered
// delays.
func TestTransientRetryIsBounded(t *testing.T) {
	base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"}}
	c := newFlakyClient(base, codes.Internal, map[string]int{"GetTaskSpec": 1 << 30})
	rec := newDelayRecorder()
	cmd := &fakeCmd{exitCode: 0}
	r := newRunner(base, cmd, &recordingSink{})
	r.Client = c
	r.afterFunc = rec.after
	r.now = rec.clk.now

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded against a control plane that never answered")
	}
	if cmd.argv != nil {
		t.Error("user code ran without a task spec")
	}
	if rec.total() > transientRetryBudget {
		t.Errorf("total backoff = %v, want at most %v", rec.total(), transientRetryBudget)
	}
	if rec.total() < transientRetryBudget/2 {
		t.Errorf("total backoff = %v, gave up well before the %v budget", rec.total(), transientRetryBudget)
	}
	if c.calls["GetTaskSpec"] != len(rec.delays)+1 {
		t.Errorf("GetTaskSpec calls = %d, want one more than the %d backoffs", c.calls["GetTaskSpec"], len(rec.delays))
	}
}

// The terminal report retries Internal within the same budget and then gives
// up, leaving the durable outcome record to the reconciler, while Unavailable
// keeps its unbounded retry (TestReportOutlastsControlPlaneRestart).
func TestReportInternalRetryIsBounded(t *testing.T) {
	base := &fakeClient{reportFailCode: codes.Internal, reportFailTimes: 1 << 30}
	rec := newDelayRecorder()
	r := instantRunner(base)
	r.afterFunc = rec.after

	if err := r.report(context.Background(), agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err == nil {
		t.Fatal("report succeeded against a control plane that never answered")
	}
	if base.reportAttempts < 2 {
		t.Errorf("ReportState attempts = %d, want Internal retried", base.reportAttempts)
	}
	if rec.total() > transientRetryBudget {
		t.Errorf("total backoff = %v, want at most %v", rec.total(), transientRetryBudget)
	}
}

// A logical rejection is not retried.
func TestTransientRetrySkipsLogicalErrors(t *testing.T) {
	base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"}}
	c := newFlakyClient(base, codes.InvalidArgument, map[string]int{"GetTaskSpec": 1 << 30})
	r := newRunner(base, &fakeCmd{}, &recordingSink{})
	r.Client = c

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded")
	}
	if c.calls["GetTaskSpec"] != 1 {
		t.Errorf("GetTaskSpec calls = %d, want 1 (InvalidArgument is not retried)", c.calls["GetTaskSpec"])
	}
}

// A canceled context (SIGTERM, pod deletion) stops the retry at once.
func TestTransientRetryStopsOnCancel(t *testing.T) {
	base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"}}
	c := newFlakyClient(base, codes.Unavailable, map[string]int{"GetTaskSpec": 1 << 30})
	r := newRunner(base, &fakeCmd{}, &recordingSink{})
	r.Client = c
	ctx, cancel := context.WithCancel(context.Background())
	r.afterFunc = func(time.Duration) <-chan time.Time {
		cancel()
		return make(chan time.Time)
	}

	if err := r.runOneAttempt(ctx); err == nil {
		t.Fatal("runOneAttempt succeeded")
	}
	if c.calls["GetTaskSpec"] != 1 {
		t.Errorf("GetTaskSpec calls = %d, want 1 (canceled during the first backoff)", c.calls["GetTaskSpec"])
	}
}
