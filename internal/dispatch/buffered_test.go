package dispatch_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingInner is a scheduler.Dispatcher fake that records every call. It is
// the dispatcher the BufferedDispatcher wraps in tests.
type recordingInner struct {
	mu        sync.Mutex
	calls     []string
	err       error
	delay     time.Duration
	callCount atomic.Int64
}

func (r *recordingInner) Dispatch(_ context.Context, _, _, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	r.callCount.Add(1)
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	r.calls = append(r.calls, task.TaskID)
	r.mu.Unlock()
	if r.err != nil {
		return executor.Rejected, r.err
	}
	return executor.Dispatched, nil
}

func (r *recordingInner) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// recordingSink records dispatch-failure callbacks the worker makes when the
// inner dispatcher returns an error in async mode.
type recordingSink struct {
	mu      sync.Mutex
	failed  []failedDispatch
	failErr error
}

type failedDispatch struct {
	runID, taskID, reason string
}

func (r *recordingSink) MarkTaskDispatchFailed(_ context.Context, runID, taskID, reason string) error {
	r.mu.Lock()
	r.failed = append(r.failed, failedDispatch{runID, taskID, reason})
	r.mu.Unlock()
	return r.failErr
}

func (r *recordingSink) snapshot() []failedDispatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]failedDispatch(nil), r.failed...)
}

// TestBuffered_Passthrough_BufferSizeZero pins the Lite contract: BufferSize=0
// means no goroutines, no channel — the call goes straight to the inner
// dispatcher and the result (including errors) bubbles up unchanged. This is
// the zero-overhead path Lite relies on.
func TestBuffered_Passthrough_BufferSizeZero(t *testing.T) {
	inner := &recordingInner{}
	d := dispatch.NewBuffered(inner, nil, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 0, Workers: 0})
	defer d.Close()
	_, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: "extract"})
	if err != nil {
		t.Fatalf("passthrough err = %v", err)
	}
	if got := inner.seen(); len(got) != 1 || got[0] != "extract" {
		t.Errorf("inner saw %v, want [extract]", got)
	}
}

// TestBuffered_Passthrough_PropagatesInnerError: in Lite mode an inner error
// surfaces directly — same shape as the original sync dispatcher contract.
func TestBuffered_Passthrough_PropagatesInnerError(t *testing.T) {
	innerErr := errors.New("kube down")
	inner := &recordingInner{err: innerErr}
	d := dispatch.NewBuffered(inner, nil, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 0, Workers: 0})
	defer d.Close()
	if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: "t"}); !errors.Is(err, innerErr) {
		t.Errorf("passthrough err = %v, want wrap of innerErr", err)
	}
}

// TestBuffered_Async_AcceptsAndDrains: BufferSize>0 returns immediately
// (the dispatcher's contract from the scheduler's POV: nil = "I have accepted
// responsibility for this task") and workers drain the channel into the
// inner dispatcher.
func TestBuffered_Async_AcceptsAndDrains(t *testing.T) {
	inner := &recordingInner{}
	d := dispatch.NewBuffered(inner, &recordingSink{}, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 4, Workers: 2})
	defer d.Close()
	for _, id := range []string{"a", "b", "c"} {
		if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: id}); err != nil {
			t.Fatalf("Dispatch %s: %v", id, err)
		}
	}
	// Wait for the inner dispatcher to see all three (workers run async).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && inner.callCount.Load() < 3 {
		time.Sleep(5 * time.Millisecond)
	}
	if inner.callCount.Load() != 3 {
		t.Errorf("inner saw %d calls, want 3", inner.callCount.Load())
	}
}

// TestBuffered_Async_BackpressureWhenChannelFull is the load-bearing
// backpressure contract: when the channel cannot accept a new request,
// Dispatch returns (Deferred, ErrAtCapacity). The scheduler leaves the TI
// scheduled so a later tick re-tries, without charging a dispatch attempt
// (review item S2). This is what bounds tick latency under load.
func TestBuffered_Async_BackpressureWhenChannelFull(t *testing.T) {
	// One slow worker, channel-of-one — the second Dispatch must fail fast
	// because the first is blocking the only worker and the channel is full.
	inner := &recordingInner{delay: 200 * time.Millisecond}
	d := dispatch.NewBuffered(inner, &recordingSink{}, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 1, Workers: 1})
	defer d.Close()

	if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: "slow"}); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	// At least one more must end up "at capacity": the worker is asleep on the
	// first call, the channel has only one slot, and the test floods the queue.
	hitCapacity := false
	for i := 0; i < 10 && !hitCapacity; i++ {
		disp, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: "flood"})
		if errors.Is(err, dispatch.ErrAtCapacity) {
			hitCapacity = true
			if disp != executor.Deferred {
				t.Errorf("a full buffer must classify as Deferred, got %v", disp)
			}
		}
	}
	if !hitCapacity {
		t.Errorf("expected at least one Dispatch to return ErrAtCapacity under flood")
	}
}

// TestBuffered_Async_WorkerFailureMarksTIFailed: when the inner dispatcher
// errors, the worker calls back via the sink so the scheduler observes the
// failure (and the TI is failed with reason 'dispatch_failed: ...' — the
// retry budget governs retry, matching Airflow's KubernetesExecutor).
// Without this callback the TI would sit `queued` forever (no reaper catches
// a queued TI; ADR 0031 #128 only targets `running`).
func TestBuffered_Async_WorkerFailureMarksTIFailed(t *testing.T) {
	innerErr := errors.New("kube create pod refused")
	inner := &recordingInner{err: innerErr}
	sink := &recordingSink{}
	d := dispatch.NewBuffered(inner, sink, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 4, Workers: 1})
	defer d.Close()

	if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: "doomed"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(sink.snapshot()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	got := sink.snapshot()
	if len(got) != 1 || got[0].taskID != "doomed" || got[0].runID != "r1" {
		t.Errorf("sink failed = %+v, want one failure for r1/doomed", got)
	}
}

// TestBuffered_Async_FailureReportToSinkSurvivesSinkError covers the
// reportFailure error path (the comment on it spells the contract out: "A sink
// error is logged but cannot itself be propagated — the worker has already
// done all it can"). The combination — inner dispatch fails AND the failure
// report itself fails — is a realistic outage shape (the K8s API hiccups on
// the dispatch, then the metadata DB is briefly unreachable when marking the
// TI failed). The worker must keep draining instead of getting stuck or
// crashing. Without this test the path was only exercised at 50%.
func TestBuffered_Async_FailureReportToSinkSurvivesSinkError(t *testing.T) {
	innerErr := errors.New("kube apiserver timeout")
	sinkErr := errors.New("postgres briefly unreachable")
	inner := &recordingInner{err: innerErr}
	sink := &recordingSink{failErr: sinkErr}
	d := dispatch.NewBuffered(inner, sink, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 4, Workers: 1})
	defer d.Close()

	// Two tasks: both will fail to dispatch, both will fail to report. The
	// second task only drains if the worker stayed alive after the first
	// double-failure — that is the regression this guards against.
	for _, taskID := range []string{"first", "second"} {
		if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: taskID}); err != nil {
			t.Fatalf("Dispatch(%s): %v", taskID, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(sink.snapshot()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	got := sink.snapshot()
	if len(got) != 2 {
		t.Fatalf("worker stalled after sink error: failures = %+v (want 2)", got)
	}
	if got[0].taskID != "first" || got[1].taskID != "second" {
		t.Errorf("unexpected failure order: %+v", got)
	}
}

// TestBuffered_Async_FailureSilentlyDroppedWithoutSink: if there is no sink at
// all (NewBuffered called with nil), an inner dispatch error must NOT crash
// the worker; the failure is simply lost — the scheduler's reaper picks the TI
// up later. This pins the nil-sink early-return half of reportFailure.
func TestBuffered_Async_FailureSilentlyDroppedWithoutSink(t *testing.T) {
	innerErr := errors.New("kube apiserver timeout")
	inner := &recordingInner{err: innerErr}
	d := dispatch.NewBuffered(inner, nil, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 2, Workers: 1})
	defer d.Close()

	if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: "lost"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// The inner dispatcher records every call (success or error), so we use
	// that as the drain signal — sink is nil so we cannot observe via it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(inner.seen()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := inner.seen(); len(got) != 1 || got[0] != "lost" {
		t.Errorf("worker did not invoke inner; calls = %+v", got)
	}
	// If we get here without panic the early-return path is exercised. The
	// next Dispatch keeps draining, confirming the worker survived.
	if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: "alive"}); err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
}

// TestBuffered_Async_PanicInInnerDoesNotCrash is the resilience pin: a panic
// in the wrapped dispatcher (a bug, a malformed task, anything) must not
// crash the worker goroutine. The worker recovers, the failure is reported
// via the sink, and the pool keeps draining.
func TestBuffered_Async_PanicInInnerDoesNotCrash(t *testing.T) {
	panicker := &panicInner{}
	sink := &recordingSink{}
	d := dispatch.NewBuffered(panicker, sink, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 2, Workers: 1})
	defer d.Close()

	if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: "boom"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(sink.snapshot()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sink.snapshot(); len(got) != 1 || got[0].taskID != "boom" {
		t.Errorf("panic must be reported via sink, got %+v", got)
	}
}

type panicInner struct{}

func (panicInner) Dispatch(context.Context, string, string, string, domain.TaskSpec) (executor.Disposition, error) {
	panic("boom: inner dispatcher")
}

// TestBuffered_Close_DrainsPendingWork: Close blocks until in-flight workers
// finish, so a shutdown does not abandon a task that the scheduler already
// recorded as `queued`. After Close returns, every accepted Dispatch is
// either delivered to the inner dispatcher or reported failed via the sink.
func TestBuffered_Close_DrainsPendingWork(t *testing.T) {
	inner := &recordingInner{}
	d := dispatch.NewBuffered(inner, &recordingSink{}, discardLogger(), nil, dispatch.BufferConfig{BufferSize: 8, Workers: 2})
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: id}); err != nil {
			t.Fatalf("Dispatch %s: %v", id, err)
		}
	}
	d.Close()
	if inner.callCount.Load() != 4 {
		t.Errorf("after Close, inner saw %d calls, want 4 (every accepted Dispatch must drain)", inner.callCount.Load())
	}
}

// TestBuffered_DispatchAfterClose_ReturnsAtCapacity: a Dispatch after Close must
// return ErrAtCapacity, not panic on send-to-closed-channel (#133 race fix).
func TestBuffered_DispatchAfterClose_ReturnsAtCapacity(t *testing.T) {
	d := dispatch.NewBuffered(&recordingInner{}, &recordingSink{}, discardLogger(), nil,
		dispatch.BufferConfig{BufferSize: 4, Workers: 1})
	if err := d.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}
	disp, err := d.Dispatch(context.Background(), "r", "d", "", domain.TaskSpec{TaskID: "t"})
	if !errors.Is(err, dispatch.ErrAtCapacity) {
		t.Fatalf("Dispatch after Close = %v, want ErrAtCapacity (must not panic)", err)
	}
	if disp != executor.Deferred {
		t.Errorf("Dispatch after Close must classify as Deferred, got %v", disp)
	}
}

// TestBuffered_ConcurrentDispatchAndClose_NoPanic: Dispatch racing Close must never
// panic (send-on-closed). Run with -race to also catch the data race (#133).
func TestBuffered_ConcurrentDispatchAndClose_NoPanic(t *testing.T) {
	d := dispatch.NewBuffered(&recordingInner{}, &recordingSink{}, discardLogger(), nil,
		dispatch.BufferConfig{BufferSize: 8, Workers: 2})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = d.Dispatch(context.Background(), "r", "d", "", domain.TaskSpec{TaskID: "t"})
		}()
	}
	_ = d.Close() // races with the in-flight Dispatch goroutines
	wg.Wait()     // no panic reaching here = pass
}

// TestBuffered_Close_DrainsAllBufferedRequests (Level A, #133): a Close with
// requests still sitting in the buffer must process every one (via the inner /
// sink) before returning — never drop them, which is what left TIs stuck
// `queued`. One slow worker guarantees the buffer is non-empty at Close time.
func TestBuffered_Close_DrainsAllBufferedRequests(t *testing.T) {
	inner := &recordingInner{delay: 15 * time.Millisecond}
	d := dispatch.NewBuffered(inner, &recordingSink{}, discardLogger(), nil,
		dispatch.BufferConfig{BufferSize: 8, Workers: 1})
	const n = 8
	for i := 0; i < n; i++ {
		if _, err := d.Dispatch(context.Background(), "r", "etl", "", domain.TaskSpec{TaskID: "t"}); err != nil {
			t.Fatalf("Dispatch %d: %v (all %d should fit the buffer)", i, err, n)
		}
	}
	// Close must BLOCK until the single slow worker has drained every buffered
	// request (~8 x 15ms), then return — nothing dropped.
	if err := d.Close(); err != nil {
		t.Fatalf("Close err = %v", err)
	}
	if got := inner.callCount.Load(); got != n {
		t.Fatalf("after Close the inner saw %d of %d requests — Close dropped buffered work (would strand TIs `queued`, #133)", got, n)
	}
}

// blockingInner hangs in Dispatch until released. It stands in for the real
// failure this guards: a kube-apiserver that accepts the connection and never
// answers, with no client-side deadline to cut it short.
type blockingInner struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingInner) Dispatch(context.Context, string, string, string, domain.TaskSpec) (executor.Disposition, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return executor.Dispatched, nil
}

// TestBuffered_Close_ReturnsWhenWorkerHangs: Close must not wait forever on a
// worker stuck in the inner dispatcher. Wiring Close() into shutdown (#133)
// turned a leaked worker into a blocked shutdown: the pod then rides out its
// termination grace period and is SIGKILLed, losing the very drain #133 added.
// Close bounds the wait and reports what it abandoned (#463).
func TestBuffered_Close_ReturnsWhenWorkerHangs(t *testing.T) {
	inner := &blockingInner{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(inner.release)
	d := dispatch.NewBuffered(inner, &recordingSink{}, discardLogger(), nil,
		dispatch.BufferConfig{BufferSize: 4, Workers: 1, DrainTimeout: 100 * time.Millisecond})
	if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: "stuck"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	<-inner.entered // the worker is now inside the hanging inner dispatcher

	done := make(chan error, 1)
	go func() { done <- d.Close() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Close returned nil; want an error naming the abandoned dispatches")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return within 3s while a worker was hung — shutdown would block until SIGKILL")
	}
}

// dispositionInner fails every dispatch with a fixed disposition, as the real
// executor classifies a quota 403 / 429 (Backpressure) or anything else
// (Rejected).
type dispositionInner struct {
	disp executor.Disposition
	err  error
}

func (d dispositionInner) Dispatch(context.Context, string, string, string, domain.TaskSpec) (executor.Disposition, error) {
	return d.disp, d.err
}

// retrySink is a FailureSink that also handles worker failures the way the
// scheduler handles synchronous ones.
type retrySink struct {
	recordingSink
	mu      sync.Mutex
	handled []executor.Disposition
}

func (r *retrySink) HandleDispatchFailure(_ context.Context, _, _ string, disp executor.Disposition, _ error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handled = append(r.handled, disp)
	return nil
}

// TestBuffered_WorkerFailureIsReofferedThroughARetrySink: a worker-side
// dispatch failure carries its disposition to a sink that can re-offer it, so
// cluster backpressure and transient errors are retried like on the
// synchronous path instead of failing the task at once.
func TestBuffered_WorkerFailureIsReofferedThroughARetrySink(t *testing.T) {
	for _, disp := range []executor.Disposition{executor.Backpressure, executor.Rejected} {
		t.Run(disp.String(), func(t *testing.T) {
			sink := &retrySink{}
			bd := dispatch.NewBuffered(dispositionInner{disp: disp, err: errors.New("dispatch failed")}, sink, discardLogger(), nil,
				dispatch.BufferConfig{BufferSize: 1, Workers: 1})
			if _, err := bd.Dispatch(context.Background(), "r1", "etl", "v1", domain.TaskSpec{TaskID: "a"}); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if err := bd.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			sink.mu.Lock()
			defer sink.mu.Unlock()
			if len(sink.handled) != 1 || sink.handled[0] != disp {
				t.Errorf("handled = %v, want [%v]", sink.handled, disp)
			}
			if got := sink.snapshot(); len(got) != 0 {
				t.Errorf("the task was failed outright: %v", got)
			}
		})
	}
}
