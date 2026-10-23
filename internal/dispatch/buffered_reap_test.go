package dispatch_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/util/flowcontrol"

	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

// throttledCluster stands in for the apiserver behind the dispatch client: a
// create first waits on client-go's own token bucket (the limiter every
// clientset carries), then the pod exists. Presence answers from what was
// created.
type throttledCluster struct {
	limiter flowcontrol.RateLimiter
	mu      sync.Mutex
	pods    map[string]bool
}

func (c *throttledCluster) Dispatch(ctx context.Context, _, _, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return executor.Rejected, err
	}
	c.mu.Lock()
	c.pods[task.TaskID] = true
	c.mu.Unlock()
	return executor.Dispatched, nil
}

func (c *throttledCluster) hasPod(taskID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pods[taskID]
}

func (c *throttledCluster) DeleteTaskPod(context.Context, executor.Attempt) error { return nil }
func (c *throttledCluster) DeleteRunPods(context.Context, string) error           { return nil }
func (c *throttledCluster) TaskPodPresence(_ context.Context, a executor.Attempt) (executor.PodPresence, error) {
	if c.hasPod(a.TaskID) {
		return executor.PodPresenceLive, nil
	}
	return executor.PodPresenceAbsent, nil
}

// TaskPodPending reports no pod as pending: every pod this cluster creates is running.
func (c *throttledCluster) TaskPodPending(context.Context, executor.Attempt) (bool, error) {
	return false, nil
}

// queuedRows is the metadatabase side of one run. A task the buffer accepts is
// written queued with queued_at = now, as MarkTaskInstanceQueued does after
// Dispatch returns Dispatched. RequeueDispatch moves it back to scheduled and
// clears queued_at, so it leaves the dispatch-lost reaper's view; the reaper
// marks fail a queued row.
type queuedRows struct {
	mu       sync.Mutex
	queued   map[string]time.Time
	reaped   map[string]bool
	requeued map[string]bool
	counted  int
}

func (q *queuedRows) markQueued(taskID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queued[taskID] = time.Now()
}

// ListStaleQueuedCandidates keeps the store's contract: rows queued at least
// threshold ago, ordered by (queued_at, id), strictly after the cursor, at
// most executor.ReapPageSize of them.
func (q *queuedRows) ListStaleQueuedCandidates(_ context.Context, threshold time.Duration, after executor.ReapCursor) ([]executor.StaleQueuedCandidate, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cutoff := time.Now().Add(-threshold)
	out := make([]executor.StaleQueuedCandidate, 0, len(q.queued))
	for id, at := range q.queued {
		if at.After(cutoff) || !afterCursor(at, id, after) {
			continue
		}
		out = append(out, executor.StaleQueuedCandidate{TaskInstanceID: id, DagRunID: "r", TaskID: id, TryNumber: 1, QueuedAt: at})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].QueuedAt.Equal(out[j].QueuedAt) {
			return out[i].QueuedAt.Before(out[j].QueuedAt)
		}
		return out[i].TaskInstanceID < out[j].TaskInstanceID
	})
	if len(out) > executor.ReapPageSize {
		out = out[:executor.ReapPageSize]
	}
	return out, nil
}

// afterCursor reports whether a row at (at, id) comes strictly after c.
func afterCursor(at time.Time, id string, c executor.ReapCursor) bool {
	if c.IsZero() {
		return true
	}
	if !at.Equal(c.At) {
		return at.After(c.At)
	}
	return id > c.ID
}

func (q *queuedRows) MarkTaskDispatchLost(_ context.Context, id string, _, _ int) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.queued[id]; !ok {
		return false, nil
	}
	delete(q.queued, id)
	q.reaped[id] = true
	return true, nil
}

func (q *queuedRows) DispatchAttempts(_ context.Context, _, taskID string) (attempts int, active bool, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, active = q.queued[taskID]
	return 0, active, nil
}

func (q *queuedRows) RequeueDispatch(_ context.Context, _, taskID string, countAttempt, _ bool, _ time.Time) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.queued[taskID]; !ok {
		return false, nil
	}
	delete(q.queued, taskID)
	q.requeued[taskID] = true
	if countAttempt {
		q.counted++
	}
	return true, nil
}

func (q *queuedRows) RecordDispatchUnavailable(_ context.Context, _, taskID string, now time.Time, minDelay, _ time.Duration) (since, nextAt time.Time, applied bool, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.queued[taskID]; !ok {
		return time.Time{}, time.Time{}, false, nil
	}
	delete(q.queued, taskID)
	q.requeued[taskID] = true
	return now, now.Add(minDelay), true, nil
}

func (q *queuedRows) MarkTaskDispatchFailed(context.Context, string, string, string) error {
	return nil
}

func (q *queuedRows) FailDispatchRefused(context.Context, string, string, string) error {
	return nil
}

func (q *queuedRows) MarkTaskPodLostOOMKilled(context.Context, string, int, int) (bool, error) {
	return false, nil
}

func (q *queuedRows) MarkTaskStartExpired(context.Context, string, int, int) (bool, error) {
	return false, nil
}

// Every other reaper sees nothing.
func (q *queuedRows) ListReapCandidates(context.Context) ([]executor.ReapCandidate, error) {
	return nil, nil
}
func (q *queuedRows) ReapRun(context.Context, string, time.Time) (bool, error) { return false, nil }
func (q *queuedRows) ListAgentLostCandidates(context.Context) ([]executor.AgentLostCandidate, error) {
	return nil, nil
}
func (q *queuedRows) MarkTaskAgentLost(context.Context, string, int, int) (bool, error) {
	return false, nil
}
func (q *queuedRows) MarkTaskCredentialCeiling(context.Context, string, int, int) (bool, error) {
	return false, nil
}
func (q *queuedRows) ListRunningTasks(context.Context, time.Duration, executor.ReapCursor) ([]executor.PodLostCandidate, error) {
	return nil, nil
}
func (q *queuedRows) MarkTaskPodLost(context.Context, string, int, int) (bool, error) {
	return false, nil
}
func (q *queuedRows) ListWarmBoundRunningTIs(context.Context) ([]executor.WarmBoundTI, error) {
	return nil, nil
}

// TestBuffered_SlowBufferTailIsNotReapedAsDispatchLost: with buffered dispatch
// the task is written queued when the buffer accepts it, not when its pod is
// created. When the buffer holds more than the dispatch-lost threshold's worth
// of creates, its tail used to pass the threshold with no pod yet, and the
// reaper failed it as dispatch_lost though nothing was lost (#1607). With the
// wait bound, a request that waited past MaxWait goes back to the scheduler's
// failure policy as backpressure: the task is re-offered with no attempt
// charged and queued_at cleared, so the reaper never sees it stale.
//
// Scaled 1:100 against production: a buffer of 512 at an effective create rate
// of 2.5/s drains in about 205 s against the 180 s threshold; here 512 at
// 250/s drains in about 2.05 s against 1.8 s, with MaxWait at half of it.
func TestBuffered_SlowBufferTailIsNotReapedAsDispatchLost(t *testing.T) {
	const tasks = 512
	cluster := &throttledCluster{limiter: flowcontrol.NewTokenBucketRateLimiter(250, 1), pods: map[string]bool{}}
	rows := &queuedRows{queued: map[string]time.Time{}, reaped: map[string]bool{}, requeued: map[string]bool{}}
	logger := discardLogger()
	threshold := 1800 * time.Millisecond
	bd := dispatch.NewBuffered(cluster, scheduler.NewAsyncDispatchFailures(rows, logger), logger, nil,
		dispatch.BufferConfig{BufferSize: tasks, Workers: 16, MaxWait: threshold / 2})
	defer func() { _ = bd.Close() }()

	for i := 0; i < tasks; i++ {
		id := fmt.Sprintf("t%03d", i)
		// Recorded before the send: the worker may take the request and act on
		// the row before this loop would get to it otherwise.
		rows.markQueued(id)
		disp, err := bd.Dispatch(context.Background(), "r", "d", "v", domain.TaskSpec{TaskID: id})
		if err != nil || disp != executor.Dispatched {
			t.Fatalf("enqueue %s: %v %v", id, disp, err)
		}
	}

	cfg := executor.DefaultReaperConfig()
	cfg.DispatchLostThreshold = threshold
	cfg.SettlingGrace = 0
	reaper := executor.NewReaper(rows, cluster, nil, nil, nil, logger, cfg, nil)
	time.Sleep(threshold + 50*time.Millisecond)
	// The reaper lists one page per sweep, so sweep until it has walked every
	// page of the buffer, the tail included.
	for sweep := 0; sweep <= tasks/executor.ReapPageSize; sweep++ {
		if err := reaper.ReapOnce(context.Background()); err != nil {
			t.Fatalf("ReapOnce: %v", err)
		}
	}
	if err := bd.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows.mu.Lock()
	defer rows.mu.Unlock()
	if n := len(rows.reaped); n > 0 {
		t.Errorf("%d of %d tasks still waiting in the dispatch buffer were failed as dispatch_lost", n, tasks)
	}
	if len(rows.requeued) == 0 {
		t.Error("no request was re-offered: the scenario did not make the buffer wait past MaxWait")
	}
	if rows.counted != 0 {
		t.Errorf("%d re-offers charged a dispatch attempt, want none (buffer wait is backpressure)", rows.counted)
	}
	for id := range rows.requeued {
		if cluster.hasPod(id) {
			t.Errorf("task %s was re-offered and also got a pod", id)
		}
	}
}
