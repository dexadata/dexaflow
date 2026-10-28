package scheduler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// podEchoServer is a minimal apiserver stub: POST .../pods answers 201 with the
// posted object and counts the create per task (read from the pod's labels).
type podEchoServer struct {
	mu      sync.Mutex
	created map[string]int
}

func (p *podEchoServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pods") {
		// the task id is in the "leoflow.io/task-id" label; a substring scan is
		// enough for a counter.
		s := string(body)
		key := "?"
		if i := strings.Index(s, `"leoflow.io/task-id":"`); i >= 0 {
			tail := s[i+len(`"leoflow.io/task-id":"`):]
			key = tail[:strings.IndexByte(tail, '"')]
		}
		p.mu.Lock()
		p.created[key]++
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(body)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// simTask is one task instance of the simulated store.
type simTask struct {
	state    domain.TaskState
	attempts int
	next     *time.Time
	reason   string
}

// simStore is a stateful Store over a simulated clock: ActiveRuns rebuilds the
// runs from the per-task rows with Now = clock, and the dispatch-failure writes
// update attempts and next_dispatch_at exactly as the SQL does. Every DB call
// on the failure path sleeps lat, a stand-in for a round trip.
type simStore struct {
	*fakeStore
	mu      sync.Mutex
	clock   time.Time
	lat     time.Duration
	order   []string // run ids in queued_at order
	tasks   map[string][]domain.TaskSpec
	rows    map[string]map[string]*simTask
	created int
}

func (s *simStore) ActiveRuns(context.Context) ([]RunState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RunState, 0, len(s.order))
	for _, id := range s.order {
		st := map[string]domain.TaskState{}
		na := map[string]*time.Time{}
		da := map[string]int{}
		for tid, r := range s.rows[id] {
			st[tid] = r.state
			na[tid] = r.next
			da[tid] = r.attempts
		}
		out = append(out, RunState{RunID: id, DagID: "d" + id, State: domain.DagRunStateRunning,
			Tasks: s.tasks[id], States: st, NextDispatchAt: na, DispatchAttempts: da, Now: s.clock})
	}
	return out, nil
}

func (s *simStore) MarkQueued(ctx context.Context, runID, taskID string, _ *time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err // pgx refuses a query on a done context
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[runID][taskID].state = domain.TaskStateQueued
	return true, nil
}

func (s *simStore) RecordDispatchFailure(ctx context.Context, runID, taskID string, nextAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	time.Sleep(s.lat)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rows[runID][taskID]
	r.attempts++
	n := nextAt
	r.next = &n
	return nil
}

// ApplyTransitions applies a plain state set (none -> scheduled and the like)
// to the rows still in the from state, as the store's source-state guard does,
// and returns the ids it moved.
func (s *simStore) ApplyTransitions(ctx context.Context, runID string, taskIDs []string, from, to domain.TaskState) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var applied []string
	for _, id := range taskIDs {
		r := s.rows[runID][id]
		if r.state != from {
			continue
		}
		r.state = to
		applied = append(applied, id)
	}
	return applied, nil
}

// ScheduledDAGs reports one cron DAG that is always due, so a tick that reaches
// createDueRuns creates a run.
func (s *simStore) ScheduledDAGs(ctx context.Context, _ time.Time) ([]ScheduledDAG, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	last := s.clock.Add(-2 * time.Hour)
	return []ScheduledDAG{{TenantID: "t2", DagID: "hourly", Schedule: "0 * * * *", LastLogical: &last}}, nil
}

func (s *simStore) CreateScheduledRun(ctx context.Context, tenantID, dagID string, logical time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.created++
	s.mu.Unlock()
	return nil
}

func (s *simStore) FailDispatchExhausted(ctx context.Context, runID, taskID, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	time.Sleep(s.lat)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rows[runID][taskID]
	r.state = domain.TaskStateFailed
	r.reason = reason
	return nil
}

// k8sDispatcher dispatches through the real KubernetesExecutor (real client-go
// rest client, so the real client-side token bucket), after a sleep standing in
// for ResolveTask's round trips.
type k8sDispatcher struct {
	exec *executor.KubernetesExecutor
	lat  time.Duration
	mu   sync.Mutex
	errs map[string]int
}

func (d *k8sDispatcher) Dispatch(ctx context.Context, runID, dagID, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	// ResolveTask's round trips: on a done context they fail, and the
	// dispatcher returns Rejected, as dispatch.Dispatcher does.
	if err := ctx.Err(); err != nil {
		return executor.Rejected, fmt.Errorf("resolving task %s: %w", task.TaskID, err)
	}
	time.Sleep(d.lat)
	disp, err := d.exec.Execute(ctx, executor.Request{RunID: runID, DagID: dagID, TaskID: task.TaskID, TryNumber: 1, Image: "img"})
	if err != nil {
		msg := err.Error()
		if i := strings.Index(msg, ": "); i >= 0 && strings.HasPrefix(msg, "creating pod") {
			msg = "creating pod: " + msg[strings.LastIndex(msg, ": ")+2:]
		}
		d.mu.Lock()
		d.errs[disp.String()+": "+msg]++
		d.mu.Unlock()
	}
	return disp, err
}

// TestSyncDispatchThrottleDoesNotStallOtherRuns is the regression test for
// R-03 (#1596): with synchronous dispatch (scheduler.dispatch.buffer_size 0)
// and the default client rate limit, one wide fan-out at the head of the queue
// spent every tick's step timeout on throttled pod creates, so no other run of
// any tenant advanced and no cron run was created. It drives the real
// KubernetesExecutor over a real client-go clientset (so the real client-side
// token bucket) to an httptest apiserver, with time scaled by 10 against
// production: client QPS 5 -> 50, step timeout 30 s -> 3 s, dispatch budget
// 5 s -> 500 ms, per-call DB latency 6 ms -> 0.6 ms.
func TestSyncDispatchThrottleDoesNotStallOtherRuns(t *testing.T) {
	srv := &podEchoServer{created: map[string]int{}}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: ts.URL, QPS: 50, Burst: 10, Timeout: 10 * time.Second,
		ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	store := &simStore{fakeStore: newFakeStore(), clock: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
		lat: 600 * time.Microsecond, tasks: map[string][]domain.TaskSpec{}, rows: map[string]map[string]*simTask{}}
	add := func(runID string, n int) {
		store.order = append(store.order, runID)
		store.rows[runID] = map[string]*simTask{}
		for i := 0; i < n; i++ {
			tid := fmt.Sprintf("%s-t%04d", runID, i)
			store.tasks[runID] = append(store.tasks[runID], domain.TaskSpec{TaskID: tid, Type: "python"})
			store.rows[runID][tid] = &simTask{state: domain.TaskStateScheduled}
		}
	}
	add("a", 2500) // a large fan-out at the head of the queue
	add("b", 40)   // a later run
	// run c (another tenant): a root that succeeded and 5 children in none,
	// which the tick moves to scheduled on its first pass.
	store.order = append(store.order, "c")
	store.rows["c"] = map[string]*simTask{"root": {state: domain.TaskStateSuccess}}
	store.tasks["c"] = []domain.TaskSpec{{TaskID: "root", Type: "python"}}
	for i := 0; i < 5; i++ {
		tid := fmt.Sprintf("c-%d", i)
		store.tasks["c"] = append(store.tasks["c"], domain.TaskSpec{TaskID: tid, Type: "python", DependsOn: []string{"root"}})
		store.rows["c"][tid] = &simTask{state: domain.TaskStateNone}
	}
	disp := &k8sDispatcher{exec: executor.NewKubernetesExecutor(cs, "ns"), lat: 600 * time.Microsecond, errs: map[string]int{}}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	s.SetStepTimeout(3 * time.Second)
	s.SetDispatchTickBudget(DefaultDispatchTickBudget / 10)

	for tick := 1; tick <= 4; tick++ {
		st := time.Now()
		s.tick(context.Background())
		took := time.Since(st)
		store.mu.Lock()
		store.clock = store.clock.Add(31 * time.Second)
		created := store.created
		store.mu.Unlock()
		if took > 2*time.Second {
			t.Errorf("tick %d took %s; the dispatch budget must keep it well under the 3s step timeout", tick, took)
		}
		if created < tick {
			t.Errorf("after tick %d only %d cron runs were created; every tick must reach run creation", tick, created)
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	for _, runID := range []string{"b", "c"} {
		for tid, r := range store.rows[runID] {
			if tid != "root" && r.state != domain.TaskStateQueued {
				t.Errorf("run %s task %s is %s after 4 ticks; the wide run ahead of it must not starve it", runID, tid, r.state)
			}
		}
	}
	for runID, rows := range store.rows {
		for tid, r := range rows {
			if r.state == domain.TaskStateFailed || r.attempts > 0 {
				t.Errorf("run %s task %s was charged a dispatch failure (state %s, attempts %d, %q); the rate limiter must never fail a healthy task",
					runID, tid, r.state, r.attempts, r.reason)
			}
		}
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for tid, n := range srv.created {
		if n > 1 {
			t.Errorf("task %s got %d pods", tid, n)
		}
	}
	if len(disp.errs) != 0 {
		t.Errorf("no dispatch may fail, got %v", disp.errs)
	}
}
