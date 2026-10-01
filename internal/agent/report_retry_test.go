package agent

import (
	"context"
	"io"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
)

// instantRunner builds a Runner whose report retry sleeps are instant, so the
// backoff logic is exercised without real wall-clock delay.
func instantRunner(client *fakeClient) *Runner {
	return &Runner{
		Client:   client,
		Hostname: "pod-1",
		Version:  "test",
		afterFunc: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
	}
}

// TestReportRetriesTransientThenSucceeds: a terminal report that hits a
// transient gRPC error (api momentarily Unavailable) is retried with backoff
// and eventually lands — the task result is not lost to a network blip, and no
// reaper has to fail a task that actually succeeded.
func TestReportRetriesTransientThenSucceeds(t *testing.T) {
	client := &fakeClient{reportFailCode: codes.Unavailable, reportFailTimes: 2}
	r := instantRunner(client)

	if err := r.report(context.Background(), agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err != nil {
		t.Fatalf("report should succeed after transient failures, got %v", err)
	}
	if client.reportAttempts != 3 {
		t.Errorf("expected 3 attempts (2 transient + 1 success), got %d", client.reportAttempts)
	}
	if len(client.reports) != 1 || client.reports[0].GetState() != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("exactly one successful SUCCESS report expected, got %+v", client.reports)
	}
}

// TestReportOutlastsControlPlaneRestart: a terminal report must keep retrying
// for as long as the control plane is down — it is never abandoned on an attempt
// count. The previous policy gave up after 6 attempts (~31s of backoff), shorter
// than a control-plane pod restart (48-80s observed), so a SUCCEEDED task's
// report was dropped and a reaper later marked it failed. The heartbeat loop
// already tolerates the outage indefinitely and renews the token when the
// server returns; the report must not give up earlier than the heartbeat.
// Here the server is Unavailable for twice the old attempt budget, then
// recovers — the report lands.
func TestReportOutlastsControlPlaneRestart(t *testing.T) {
	const outageAttempts = 12 // > the retired 6-attempt budget
	client := &fakeClient{reportFailCode: codes.Unavailable, reportFailTimes: outageAttempts}
	r := instantRunner(client)

	if err := r.report(context.Background(), agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err != nil {
		t.Fatalf("report must succeed once the control plane returns, got %v", err)
	}
	if want := outageAttempts + 1; client.reportAttempts != want {
		t.Errorf("expected %d attempts (%d failed + 1 success), got %d", want, outageAttempts, client.reportAttempts)
	}
	if len(client.reports) != 1 || client.reports[0].GetState() != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("exactly one successful SUCCESS report expected, got %+v", client.reports)
	}
}

// TestReportBackoffRampsThenHoldsAtHeartbeatInterval pins the per-attempt delay
// policy: an exponential ramp (1s, 2s, 4s, 8s) that is CAPPED at the heartbeat
// interval and then holds there for every further attempt — never longer. Capping
// the delay rather than the duration means the agent's next attempt is at most
// one heartbeat away once the server returns (plus the gRPC channel's own
// reconnect backoff), however long the outage lasted.
func TestReportBackoffRampsThenHoldsAtHeartbeatInterval(t *testing.T) {
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		DefaultHeartbeatInterval, DefaultHeartbeatInterval, DefaultHeartbeatInterval, DefaultHeartbeatInterval,
	}
	for i, w := range want {
		if got := reportBackoff(i + 1); got != w {
			t.Errorf("reportBackoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	for _, n := range []int{0, -3, 100, 1 << 20} {
		if got := reportBackoff(n); got <= 0 || got > DefaultHeartbeatInterval {
			t.Errorf("reportBackoff(%d) = %v, want a positive delay no longer than the heartbeat interval", n, got)
		}
	}

	// The loop uses exactly that schedule: capture the delays it sleeps for.
	var delays []time.Duration
	client := &fakeClient{reportFailCode: codes.Unavailable, reportFailTimes: len(want)}
	r := &Runner{
		Client:   client,
		Hostname: "pod-1",
		Version:  "test",
		afterFunc: func(d time.Duration) <-chan time.Time {
			delays = append(delays, d)
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
	}
	if err := r.report(context.Background(), agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(delays) != len(want) {
		t.Fatalf("slept %d times, want %d", len(delays), len(want))
	}
	for i, d := range delays {
		if d != want[i] {
			t.Errorf("sleep %d = %v, want %v", i+1, d, want[i])
		}
		if d > DefaultHeartbeatInterval {
			t.Errorf("sleep %d = %v exceeds the heartbeat-interval cap", i+1, d)
		}
	}
}

// TestReportDoesNotRetryUnauthenticated: a credential rejection is not a
// transient outage — retrying it can never succeed and would only hold the pod.
// It is returned at once, and the durable outcome record (written before the
// report) is what the reconciler recovers from.
func TestReportDoesNotRetryUnauthenticated(t *testing.T) {
	client := &fakeClient{reportFailCode: codes.Unauthenticated, reportFailTimes: 1000}
	r := instantRunner(client)

	if err := r.report(context.Background(), agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err == nil {
		t.Fatal("an Unauthenticated report error must be returned, not retried")
	}
	if client.reportAttempts != 1 {
		t.Errorf("Unauthenticated must not be retried, got %d attempts", client.reportAttempts)
	}
}

// TestReportDoesNotRetryNonRetryable: a logical rejection (e.g. InvalidArgument)
// is returned immediately — retrying it would never apply and would only delay
// the failure.
func TestReportDoesNotRetryNonRetryable(t *testing.T) {
	client := &fakeClient{reportFailCode: codes.InvalidArgument, reportFailTimes: 1000}
	r := instantRunner(client)

	if err := r.report(context.Background(), agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err == nil {
		t.Fatal("a non-retryable report error must be returned")
	}
	if client.reportAttempts != 1 {
		t.Errorf("a non-retryable error must not be retried, got %d attempts", client.reportAttempts)
	}
}

// TestReportRetryAbortsOnContextCancel: while backing off, a canceled context
// (parent shutdown / SIGTERM) aborts the retry promptly rather than sleeping
// out the whole backoff schedule.
func TestReportRetryAbortsOnContextCancel(t *testing.T) {
	client := &fakeClient{reportFailCode: codes.Unavailable, reportFailTimes: 1000}
	r := &Runner{
		Client:   client,
		Hostname: "pod-1",
		Version:  "test",
		// Never fires, so the retry can only leave via ctx cancellation.
		afterFunc: func(time.Duration) <-chan time.Time { return make(chan time.Time) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := r.report(ctx, agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err == nil {
		t.Fatal("report must return the context error when canceled mid-backoff")
	}
	if client.reportAttempts != 1 {
		t.Errorf("expected 1 attempt before the canceled backoff aborted, got %d", client.reportAttempts)
	}
}

// holdingCmd is a CommandRunner that records, at the moment user code would
// start, how many reports the control plane had accepted — so a test can prove
// user code is held back until the RUNNING pre-flight report has landed.
type holdingCmd struct {
	client             *fakeClient
	acceptedAtStart    int
	attemptsAtStart    int
	stateAcceptedFirst agentv1.TaskState
	ran                bool
}

func (c *holdingCmd) Run(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
	c.ran = true
	c.acceptedAtStart = len(c.client.reports)
	c.attemptsAtStart = c.client.reportAttempts
	if len(c.client.reports) > 0 {
		c.stateAcceptedFirst = c.client.reports[0].GetState()
	}
	return 0, nil
}

// TestRunningReportOutlastsControlPlaneRestart: the RUNNING pre-flight report
// shares the terminal report's retry loop and therefore also outlasts a
// control-plane outage — deliberately. An agent that cannot reach the control
// plane does not start user code until it can: the pod sits in its pre-flight,
// not in a half-observed run. Here the control plane is Unavailable for twice
// the retired 6-attempt budget while the agent tries to report RUNNING; user
// code must not have started before that report was accepted, and the task
// then completes normally.
func TestRunningReportOutlastsControlPlaneRestart(t *testing.T) {
	const outageAttempts = 12
	client := &fakeClient{
		spec:            &agentv1.TaskSpec{Operator: "bash", Entrypoint: "true"},
		reportFailCode:  codes.Unavailable,
		reportFailTimes: outageAttempts,
	}
	cmd := &holdingCmd{client: client}
	r := &Runner{
		Client:   client,
		Cmd:      cmd,
		Sink:     &recordingSink{},
		Hostname: "pod-1",
		Version:  "test",
		afterFunc: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
	}

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("the task must complete once the control plane returns, got %v", err)
	}
	if !cmd.ran {
		t.Fatal("user code must run once the RUNNING report landed")
	}
	if cmd.acceptedAtStart != 1 || cmd.stateAcceptedFirst != agentv1.TaskState_TASK_STATE_RUNNING {
		t.Errorf("user code started with %d accepted reports (first %v); want exactly the RUNNING report accepted first",
			cmd.acceptedAtStart, cmd.stateAcceptedFirst)
	}
	if want := outageAttempts + 1; cmd.attemptsAtStart != want {
		t.Errorf("user code started after %d report attempts, want %d (%d refused + the accepted RUNNING)", cmd.attemptsAtStart, want, outageAttempts)
	}
	if n := len(client.reports); n != 2 || client.reports[1].GetState() != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("accepted reports = %d (last %v), want RUNNING then SUCCESS", n, client.reports[n-1].GetState())
	}
}
