package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeClock is the agent's clock in these tests: a backoff advances it by the
// delay at once, and a hung call advances it by the time the call would block.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) after(d time.Duration) <-chan time.Time {
	c.advance(d)
	return instantAfter(d)
}

// perCallDeadline is the deadline the production dialer puts on every unary
// call but Heartbeat (#946).
const perCallDeadline = 30 * time.Second

// hangUntilDeadline plays a call to a control plane that accepts the
// connection and never answers: it blocks until its deadline, the per-call 30s
// or the caller's own shorter one, and fails with DeadlineExceeded.
func hangUntilDeadline(ctx context.Context, clk *fakeClock) error {
	d := perCallDeadline
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left < d {
			d = max(left, 0)
		}
	}
	clk.advance(d)
	return status.Error(codes.DeadlineExceeded, "context deadline exceeded")
}

// hungSpecClient answers every GetTaskSpec as a hung control plane does.
type hungSpecClient struct {
	*fakeClient
	clk   *fakeClock
	calls int
}

func (h *hungSpecClient) GetTaskSpec(ctx context.Context, _ *agentv1.GetTaskSpecRequest, _ ...grpc.CallOption) (*agentv1.TaskSpec, error) {
	h.calls++
	return nil, hangUntilDeadline(ctx, h.clk)
}

func clockedRunner(base *fakeClient, cmd *fakeCmd, clk *fakeClock) *Runner {
	r := newRunner(base, cmd, &recordingSink{})
	r.afterFunc = clk.after
	r.now = clk.now
	return r
}

// The budget is wall time, not backoff: with #946's 30s per-call deadline, a
// control plane that hangs every call used to cost about 14 tries of 30s on
// top of 2 minutes of backoff, about 9 minutes for GetTaskSpec alone, against
// a documented 2 minutes (review finding 1594-1).
func TestTransientRetryBudgetCountsTimeInsideTries(t *testing.T) {
	// The backoff is jittered, so where the last try starts against the
	// deadline varies; enough rounds make a try that overruns it certain.
	for round := range 20 {
		clk := newFakeClock()
		base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"}}
		c := &hungSpecClient{fakeClient: base, clk: clk}
		cmd := &fakeCmd{exitCode: 0}
		r := clockedRunner(base, cmd, clk)
		r.Client = c

		start := clk.now()
		if err := r.runOneAttempt(context.Background()); err == nil {
			t.Fatal("runOneAttempt succeeded against a control plane that never answered")
		}
		elapsed := clk.now().Sub(start)
		if elapsed > transientRetryBudget {
			t.Fatalf("round %d: start phase took %v against a hung control plane, want at most %v", round, elapsed, transientRetryBudget)
		}
		if elapsed < transientRetryBudget/2 {
			t.Fatalf("round %d: start phase took %v, gave up well before the %v budget", round, elapsed, transientRetryBudget)
		}
		if c.calls < 2 {
			t.Fatalf("round %d: GetTaskSpec calls = %d, want the hung call retried", round, c.calls)
		}
		if cmd.argv != nil {
			t.Fatalf("round %d: user code ran without a task spec", round)
		}
	}
}

// failingStartClient fails the start-phase reads with Unavailable: the first
// specFailures GetTaskSpec calls, and every GetVariables and GetConnections.
type failingStartClient struct {
	*fakeClient
	specFailures int
	calls        map[string]int
}

func (f *failingStartClient) unavailable(method string) error {
	f.calls[method]++
	return status.Error(codes.Unavailable, "injected: control plane store unavailable")
}

func (f *failingStartClient) GetTaskSpec(ctx context.Context, in *agentv1.GetTaskSpecRequest, opts ...grpc.CallOption) (*agentv1.TaskSpec, error) {
	if f.calls["GetTaskSpec"] < f.specFailures {
		return nil, f.unavailable("GetTaskSpec")
	}
	f.calls["GetTaskSpec"]++
	return f.fakeClient.GetTaskSpec(ctx, in, opts...)
}

func (f *failingStartClient) GetVariables(context.Context, *agentv1.GetVariablesRequest, ...grpc.CallOption) (*agentv1.GetVariablesResponse, error) {
	return nil, f.unavailable("GetVariables")
}

func (f *failingStartClient) GetConnections(context.Context, *agentv1.GetConnectionsRequest, ...grpc.CallOption) (*agentv1.GetConnectionsResponse, error) {
	return nil, f.unavailable("GetConnections")
}

// The start phase shares one budget: every call before the task runs used to
// get its own 2 minutes, so a start with several failing reads waited a
// multiple of the documented bound with the task instance still queued.
func TestTransientRetryBudgetIsSharedAcrossStartCalls(t *testing.T) {
	clk := newFakeClock()
	base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"}}
	c := &failingStartClient{fakeClient: base, specFailures: 3, calls: map[string]int{}}
	cmd := &fakeCmd{exitCode: 0}
	r := clockedRunner(base, cmd, clk)
	r.Client = c

	start := clk.now()
	if err := r.runOneAttempt(context.Background()); err != nil {
		t.Fatalf("runOneAttempt: %v", err)
	}
	if elapsed := clk.now().Sub(start); elapsed > transientRetryBudget {
		t.Errorf("start phase took %v over GetTaskSpec, GetVariables and GetConnections, want at most %v shared", elapsed, transientRetryBudget)
	}
	if c.calls["GetTaskSpec"] != 4 {
		t.Errorf("GetTaskSpec calls = %d, want 4 (three failures, then the spec)", c.calls["GetTaskSpec"])
	}
	if c.calls["GetVariables"] < 2 {
		t.Errorf("GetVariables calls = %d, want it retried within the budget", c.calls["GetVariables"])
	}
	if cmd.argv == nil {
		t.Error("user code never ran; variables and connections are best effort")
	}
}

// failingPushClient fails the first returnFailures pushes of return_value and
// every push of a key in failKeys with Unavailable.
type failingPushClient struct {
	*fakeClient
	returnFailures int
	failKeys       map[string]bool
	calls          map[string]int
}

func (f *failingPushClient) PushXCom(ctx context.Context, in *agentv1.PushXComRequest, opts ...grpc.CallOption) (*agentv1.PushXComResponse, error) {
	key := in.GetKey()
	f.calls[key]++
	if f.failKeys[key] || (key == "return_value" && f.calls[key] <= f.returnFailures) {
		return nil, status.Error(codes.Unavailable, "injected: control plane store unavailable")
	}
	return f.fakeClient.PushXCom(ctx, in, opts...)
}

// The output pushes share one budget: the return value and every custom XCom
// key used to get 2 minutes each.
func TestTransientRetryBudgetIsSharedAcrossOutputPushes(t *testing.T) {
	clk := newFakeClock()
	base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"}}
	c := &failingPushClient{fakeClient: base, returnFailures: 8, failKeys: map[string]bool{"a": true}, calls: map[string]int{}}
	r := clockedRunner(base, &fakeCmd{exitCode: 0}, clk)
	r.Client = c
	r.ReturnPath = writeReturnValue(t)
	r.PushesPath = filepath.Join(t.TempDir(), "pushes.json")
	if err := os.WriteFile(r.PushesPath, []byte(`{"a": 1, "b": 2}`), 0o600); err != nil {
		t.Fatal(err)
	}

	start := clk.now()
	if err := r.runOneAttempt(context.Background()); err == nil {
		t.Fatal("runOneAttempt succeeded with a push that never landed")
	}
	if elapsed := clk.now().Sub(start); elapsed > transientRetryBudget {
		t.Errorf("output pushes took %v, want at most %v shared", elapsed, transientRetryBudget)
	}
	if c.calls["return_value"] != 9 {
		t.Errorf("return_value pushes = %d, want 9 (eight failures, then it lands)", c.calls["return_value"])
	}
	if c.calls["a"] < 2 {
		t.Errorf("pushes of key a = %d, want it retried within what is left of the budget", c.calls["a"])
	}
	if got := lastState(t, base); got != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("final state = %v, want FAILED once the budget is spent", got)
	}
}
