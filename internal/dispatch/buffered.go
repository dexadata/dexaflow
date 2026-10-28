package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// ErrAtCapacity is returned by BufferedDispatcher.Dispatch when the buffered
// queue cannot accept another request. It travels with executor.Deferred: the
// scheduler leaves the TI scheduled so a later tick re-tries, without counting
// it as a failed dispatch. It is the backpressure signal that bounds tick
// latency under load (ADR 0031: tick rate decoupled from executor latency).
var ErrAtCapacity = errors.New("dispatch buffer at capacity; will retry next tick")

// ErrDrainTimeout is returned by Close when workers did not finish within the
// drain timeout. Shutdown continues: blocking forever is worse, because the
// runtime then kills the process and no drain happens at all (#463).
var ErrDrainTimeout = errors.New("drain timed out")

// ErrBufferWaitExceeded is the cause a worker hands to a RetrySink, with
// executor.Backpressure, for a request that waited in the buffer longer than
// BufferConfig.MaxWait. The task is re-offered instead of dispatched (#1607).
var ErrBufferWaitExceeded = errors.New("dispatch request waited in the buffer past its bound; re-offering")

// Inner is the underlying synchronous dispatcher BufferedDispatcher wraps —
// matches scheduler.Dispatcher exactly so production wires through one type.
type Inner interface {
	Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error)
}

// FailureSink lets a worker report that an asynchronously-dispatched task
// failed inside the inner dispatcher, so the scheduler can fail the TI with a
// clear reason. Without this callback a `queued` TI whose dispatch failed
// would sit forever (no reaper targets `queued`; ADR 0031 #128 only targets
// `running`).
type FailureSink interface {
	MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error
}

// RetrySink is a FailureSink that can also re-offer a task whose dispatch failed
// in a worker, given the disposition the inner dispatcher classified the error
// as. When the sink implements it, a worker failure is handled the way the
// scheduler handles a synchronous one (scheduler.AsyncDispatchFailures):
// backpressure and transient errors put the task back to scheduled with a
// backoff instead of failing it. A plain FailureSink keeps failing the task.
type RetrySink interface {
	FailureSink
	HandleDispatchFailure(ctx context.Context, runID, taskID string, disp executor.Disposition, cause error) error
}

// MetricsRecorder records dispatch-pool observability signals.
type MetricsRecorder interface {
	RecordDispatchQueueDepth(depth int)
	RecordDispatchAtCapacity()
	RecordDispatchLatencySeconds(seconds float64)
	RecordDispatchInnerError()
}

// BufferConfig sizes the BufferedDispatcher's worker pool. BufferSize=0 means
// "passthrough sync" (Lite mode, zero overhead): no goroutines spawned, no
// channel, the inner dispatcher is called inline. Any BufferSize>0 spawns
// max(Workers, 1) worker goroutines and a buffered channel of BufferSize
// slots.
type BufferConfig struct {
	BufferSize int
	Workers    int
	// DrainTimeout bounds how long Close waits for workers to finish. Zero
	// selects defaultDrainTimeout. Without a bound, one worker stuck in a
	// remote call that never answers blocks shutdown until the runtime kills
	// the process, which loses the drain entirely (#463).
	DrainTimeout time.Duration
	// MaxWait bounds how long a request may wait in the buffer before a worker
	// takes it. The scheduler records the task queued, stamping queued_at, as
	// soon as the buffer accepts it, so time spent in the buffer counts toward
	// the dispatch-lost threshold; a request older than MaxWait when a worker
	// takes it is not dispatched. When the sink is a RetrySink it is handed back
	// as executor.Backpressure, which re-offers the task with no dispatch
	// attempt charged and a fresh queued_at on its next queued episode (#1607).
	// Zero selects half the default dispatch-lost threshold; a negative value
	// disables the bound.
	MaxWait time.Duration
}

// defaultDrainTimeout leaves room for an in-flight dispatch to finish while
// staying well inside a typical Kubernetes terminationGracePeriodSeconds (30s),
// so an abandoned drain is still reported before the pod is SIGKILLed.
const defaultDrainTimeout = 15 * time.Second

// dispatchRequest carries one queued dispatch from the scheduler to a worker.
// runID, dagID, dagVersionID, task are the same arguments the synchronous
// interface takes; ctx is the scheduler's caller context — the worker uses a
// derived background context so a cancellation of the caller does not abandon
// work the scheduler already considers "accepted".
type dispatchRequest struct {
	runID, dagID, dagVersionID string
	task                       domain.TaskSpec
	// enqueuedAt is when the buffer accepted the request, the moment the
	// scheduler goes on to stamp queued_at. It also starts the dispatch
	// latency the worker observes.
	enqueuedAt time.Time
}

// BufferedDispatcher fronts a synchronous Inner dispatcher with a bounded
// worker pool, so the scheduler tick is never blocked by a slow remote API
// call. ADR 0031: two-phase scheduler — planning sync, dispatch async.
type BufferedDispatcher struct {
	inner   Inner
	sink    FailureSink
	logger  *slog.Logger
	metrics MetricsRecorder
	cfg     BufferConfig
	queue   chan dispatchRequest
	wg      sync.WaitGroup
	// mu guards closed and serializes Dispatch's send against Close's channel
	// close, so a Dispatch racing Close returns ErrAtCapacity instead of
	// panicking on send-to-closed-channel (#133).
	mu     sync.RWMutex
	closed bool
	once   sync.Once
	// maxWait is cfg.MaxWait resolved: zero means no bound.
	maxWait time.Duration
}

// NewBuffered constructs a BufferedDispatcher. BufferSize=0 returns a
// passthrough that is byte-for-byte equivalent to using the inner dispatcher
// directly (Lite path). BufferSize>0 spawns the worker pool.
func NewBuffered(inner Inner, sink FailureSink, logger *slog.Logger, metrics MetricsRecorder, cfg BufferConfig) *BufferedDispatcher {
	b := &BufferedDispatcher{
		inner:   inner,
		sink:    sink,
		logger:  logger,
		metrics: metrics,
		cfg:     cfg,
	}
	if cfg.BufferSize <= 0 {
		return b
	}
	switch {
	case cfg.MaxWait == 0:
		b.maxWait = executor.DefaultReaperConfig().DispatchLostThreshold / 2
	case cfg.MaxWait > 0:
		b.maxWait = cfg.MaxWait
	}
	workers := cfg.Workers
	if workers <= 0 {
		workers = 1
	}
	b.queue = make(chan dispatchRequest, cfg.BufferSize)
	for i := 0; i < workers; i++ {
		b.wg.Add(1)
		go b.worker()
	}
	return b
}

// Dispatch hands a task off to the inner dispatcher. In passthrough mode the
// inner call happens inline. In buffered mode the request is enqueued non-
// blockingly: success returns (Dispatched, nil) immediately (the scheduler then
// records the TI as `queued`); a full or closed channel returns (Deferred,
// ErrAtCapacity). Deferred tells the scheduler nothing was attempted, so the TI
// stays scheduled for a later tick without touching its dispatch-attempt budget
// (review item S2: a full buffer is backpressure, not a dispatch failure).
func (b *BufferedDispatcher) Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error) {
	if b.queue == nil {
		return b.inner.Dispatch(ctx, runID, dagID, dagVersionID, task)
	}
	// RLock pairs with Close's Lock: while any Dispatch holds it, Close can't
	// close the queue channel, so the non-blocking send below can never hit a
	// closed channel. Once Close has run, closed is true and we refuse (#133).
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		if b.metrics != nil {
			b.metrics.RecordDispatchAtCapacity()
		}
		return executor.Deferred, ErrAtCapacity
	}
	// A request accepted here is dispatched by a worker on its own context, but
	// the scheduler records it as queued with the caller's context. When that
	// context is already done (the tick overran its step timeout, or leadership
	// was lost mid-tick) the queued write fails, the task stays scheduled and the
	// next tick offers it again: two pods for one try. Defer instead.
	if ctx.Err() != nil {
		return executor.Deferred, ErrAtCapacity
	}
	select {
	case b.queue <- dispatchRequest{runID: runID, dagID: dagID, dagVersionID: dagVersionID, task: task, enqueuedAt: time.Now()}:
		if b.metrics != nil {
			b.metrics.RecordDispatchQueueDepth(len(b.queue))
		}
		return executor.Dispatched, nil
	default:
		if b.metrics != nil {
			b.metrics.RecordDispatchAtCapacity()
		}
		return executor.Deferred, ErrAtCapacity
	}
}

// Close stops accepting new dispatches, drains the in-flight queue (each buffered
// request is still processed by the inner dispatcher, or failed or re-offered
// via the sink, so no TI is left stuck `queued`), and waits for every worker to finish. Under
// mu so a concurrent Dispatch never sends on the closed channel. Idempotent.
//
// The wait is bounded by cfg.DrainTimeout (#463). A worker sits in the inner
// dispatcher with a detached context, so a remote API that accepts the
// connection and never answers would otherwise block shutdown forever — the
// process then dies by SIGKILL and loses the drain #133 added. On expiry Close
// returns an error naming how many workers were abandoned; the caller logs it
// and proceeds with shutdown rather than hanging. The abandoned goroutines die
// with the process.
func (b *BufferedDispatcher) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		if b.queue != nil {
			close(b.queue)
		}
		b.mu.Unlock()
	})
	timeout := b.cfg.DrainTimeout
	if timeout <= 0 {
		timeout = defaultDrainTimeout
	}
	drained := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("draining dispatch pool: %w after %s; in-flight dispatches were abandoned "+
			"(their task instances stay queued until the stale-queued reaper picks them up)",
			ErrDrainTimeout, timeout)
	}
}

// worker drains the queue, calling the inner dispatcher and reporting failures
// via the sink so the scheduler can fail the TI. A panic in the inner
// dispatcher is recovered so one poison task never kills the worker
// goroutine (ADR 0031 "the scheduler must never die" extends to the workers).
func (b *BufferedDispatcher) worker() {
	defer b.wg.Done()
	for req := range b.queue {
		// The depth gauge follows the queue down as well as up; set only on
		// enqueue, it kept its last high value after the workers drained it.
		if b.metrics != nil {
			b.metrics.RecordDispatchQueueDepth(len(b.queue))
		}
		b.dispatchOne(req)
	}
}

// dispatchOne calls the inner dispatcher with full panic isolation; a panic
// is reported via the sink as a dispatch failure so the scheduler can fail
// the TI rather than leaving it stuck `queued`.
func (b *BufferedDispatcher) dispatchOne(req dispatchRequest) {
	defer func() {
		if rec := recover(); rec != nil {
			b.logger.Error("dispatch worker panic recovered",
				"run", req.runID, "dag", req.dagID, "task", req.task.TaskID,
				"panic", rec, "stack", string(debug.Stack()))
			b.reportFailure(req, "dispatch_failed: worker panic")
		}
	}()
	if b.reofferExpired(req) {
		return
	}
	// Use a background context: the caller's ctx may already be canceled by
	// the time the worker picks the request up (a long tick has rolled over),
	// but we already accepted responsibility for this dispatch — abandoning it
	// would leave a `queued` TI without a runner. Hanging is bounded from two
	// sides instead: the Kubernetes client carries a per-call timeout, and
	// Close stops waiting after cfg.DrainTimeout (#463).
	// The disposition goes to a RetrySink, which re-offers backpressure and
	// transient errors like the synchronous path does; a plain sink fails the TI.
	disp, err := b.inner.Dispatch(context.Background(), req.runID, req.dagID, req.dagVersionID, req.task) //nolint:contextcheck // worker intentionally detaches from the caller's ctx
	if b.metrics != nil {
		b.metrics.RecordDispatchLatencySeconds(time.Since(req.enqueuedAt).Seconds())
	}
	if err == nil {
		return
	}
	b.logger.Error("dispatch failed in worker",
		"run", req.runID, "dag", req.dagID, "task", req.task.TaskID, "disposition", disp.String(), "error", err)
	if b.metrics != nil {
		b.metrics.RecordDispatchInnerError()
	}
	if rs, ok := b.sink.(RetrySink); ok {
		if herr := rs.HandleDispatchFailure(context.Background(), req.runID, req.task.TaskID, disp, err); herr != nil { //nolint:contextcheck // worker intentionally uses a fresh context for the failure report
			b.logger.Error("re-offering a failed dispatch",
				"run", req.runID, "task", req.task.TaskID, "error", herr)
		}
		return
	}
	b.reportFailure(req, "dispatch_failed: "+err.Error())
}

// reofferExpired hands a request that waited in the buffer past maxWait back
// to the RetrySink as backpressure instead of dispatching it, and reports
// whether it did. Its task has been queued since the buffer accepted it, so
// dispatching it now would leave the dispatch-lost reaper free to fail it
// before its pod exists (#1607). The re-offer charges no dispatch attempt and
// clears queued_at, so the task starts a fresh clock when it is queued again.
// Without a RetrySink the request is dispatched as before: failing the task
// would be worse than the late dispatch.
func (b *BufferedDispatcher) reofferExpired(req dispatchRequest) bool {
	if b.maxWait <= 0 {
		return false
	}
	waited := time.Since(req.enqueuedAt)
	if waited <= b.maxWait {
		return false
	}
	rs, ok := b.sink.(RetrySink)
	if !ok {
		return false
	}
	b.logger.Warn("dispatch request waited in the buffer past its bound; re-offering the task",
		"run", req.runID, "dag", req.dagID, "task", req.task.TaskID,
		"waited", waited, "max_wait", b.maxWait)
	if err := rs.HandleDispatchFailure(context.Background(), req.runID, req.task.TaskID, executor.Backpressure, ErrBufferWaitExceeded); err != nil { //nolint:contextcheck // worker intentionally uses a fresh context for the re-offer
		b.logger.Error("re-offering an expired buffered dispatch",
			"run", req.runID, "task", req.task.TaskID, "error", err)
	}
	return true
}

// reportFailure marks the TI failed via the sink if one is configured. A sink
// error is logged but cannot itself be propagated — the worker has already
// done all it can.
func (b *BufferedDispatcher) reportFailure(req dispatchRequest, reason string) {
	if b.sink == nil {
		return
	}
	if err := b.sink.MarkTaskDispatchFailed(context.Background(), req.runID, req.task.TaskID, reason); err != nil { //nolint:contextcheck // worker intentionally uses a fresh context for the failure report
		b.logger.Error("reporting dispatch failure",
			"run", req.runID, "task", req.task.TaskID, "error", err)
	}
}
