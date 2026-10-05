package agentrpc

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/xcom"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// countingStore counts task spec loads so a test can assert how often an
// attempt's RPCs reach the database.
type countingStore struct {
	fakeStore
	loads atomic.Int64
}

func (s *countingStore) TaskSpec(ctx context.Context, id auth.AgentIdentity) (TaskSpec, error) {
	s.loads.Add(1)
	return s.fakeStore.TaskSpec(ctx, id)
}

func xcomAttemptStore() *countingStore {
	return &countingStore{fakeStore: fakeStore{spec: TaskSpec{
		Operator:         "python",
		Environment:      map[string]string{"FOO": "bar"},
		XComInputMapping: map[string][]string{"a": {"up1"}, "b": {"up2"}},
		DependsOn:        []string{"up1", "up2", "up3"},
		XComSchema:       map[string]any{"type": "object"},
	}}}
}

func xcomAttemptEntries() *fakeXCom {
	return &fakeXCom{entries: map[string]xcom.Entry{
		"xcom:acme:etl:run-1:up1:return_value": {Value: []byte(`1`)},
		"xcom:acme:etl:run-1:up2:return_value": {Value: []byte(`2`)},
		"xcom:acme:etl:run-1:up3:return_value": {Value: []byte(`3`)},
	}}
}

func ctxFor(t testing.TB, a *auth.JWTAuthenticator, id auth.AgentIdentity) context.Context {
	t.Helper()
	token, err := a.IssueAgentToken(id, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

// runXComRPCs drives the XCom RPCs of one attempt: three upstream fetches and
// one push.
func runXComRPCs(t testing.TB, srv *Server, ctx context.Context) {
	t.Helper()
	for _, up := range []string{"up1", "up2", "up3"} {
		if _, err := srv.FetchXCom(ctx, &agentv1.FetchXComRequest{UpstreamTaskId: up}); err != nil {
			t.Fatalf("FetchXCom %s: %v", up, err)
		}
	}
	resp, err := srv.PushXCom(ctx, &agentv1.PushXComRequest{Value: []byte(`{"rows":1}`)})
	if err != nil || !resp.GetAccepted() {
		t.Fatalf("PushXCom: %v %v", resp, err)
	}
}

// TestAttemptSpecLoadedOnceForXComRPCs: every FetchXCom and PushXCom of an
// attempt used to reload and rebuild the whole task spec (a run lookup and a
// reschedule lookup each). The XCom fields they need are now loaded once per
// attempt, and GetTaskSpec seeds them.
func TestAttemptSpecLoadedOnceForXComRPCs(t *testing.T) {
	store := xcomAttemptStore()
	srv, a := newServerX(store, xcomAttemptEntries())
	ctx := ctxWithToken(t, a)

	if _, err := srv.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{}); err != nil {
		t.Fatalf("GetTaskSpec: %v", err)
	}
	runXComRPCs(t, srv, ctx)
	runXComRPCs(t, srv, ctx)
	if got := store.loads.Load(); got != 1 {
		t.Errorf("task spec loaded %d times for one attempt, want 1", got)
	}
}

// TestAttemptSpecStillAuthorizesFromCache: a cached view keeps denying an
// upstream the task did not declare.
func TestAttemptSpecStillAuthorizesFromCache(t *testing.T) {
	store := xcomAttemptStore()
	srv, a := newServerX(store, xcomAttemptEntries())
	ctx := ctxWithToken(t, a)
	runXComRPCs(t, srv, ctx)
	if _, err := srv.FetchXCom(ctx, &agentv1.FetchXComRequest{UpstreamTaskId: "other"}); err == nil {
		t.Fatal("cached spec authorized an undeclared upstream")
	}
}

// TestGetTaskSpecAlwaysReadsTheStore: the full spec (environment, call args,
// params) is never served from the cache.
func TestGetTaskSpecAlwaysReadsTheStore(t *testing.T) {
	store := xcomAttemptStore()
	srv, a := newServerX(store, xcomAttemptEntries())
	ctx := ctxWithToken(t, a)
	for i := 0; i < 3; i++ {
		if _, err := srv.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{}); err != nil {
			t.Fatalf("GetTaskSpec: %v", err)
		}
	}
	if got := store.loads.Load(); got != 3 {
		t.Errorf("GetTaskSpec loaded the spec %d times for 3 calls, want 3", got)
	}
}

// TestAttemptSpecCacheHoldsOnlyXComFields pins what may be cached: the XCom
// authorization and validation fields. Environment, call and operator args,
// params and the declared secret names are never kept.
func TestAttemptSpecCacheHoldsOnlyXComFields(t *testing.T) {
	typ := reflect.TypeOf(attemptSpec{})
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		got = append(got, typ.Field(i).Name)
	}
	sort.Strings(got)
	want := []string{"DependsOn", "XComInputMapping", "XComSchema"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("attemptSpec fields = %v, want exactly %v", got, want)
	}
}

// TestAttemptSpecScopedToTheAttempt: another try, run or tenant never reuses
// an attempt's cached view.
func TestAttemptSpecScopedToTheAttempt(t *testing.T) {
	store := xcomAttemptStore()
	srv, a := newServerX(store, xcomAttemptEntries())
	base := testIdentity()
	runXComRPCs(t, srv, ctxFor(t, a, base))

	for name, mutate := range map[string]func(*auth.AgentIdentity){
		"next try":     func(id *auth.AgentIdentity) { id.TryNumber = 2; id.TaskInstanceID = "ti-2" },
		"other tenant": func(id *auth.AgentIdentity) { id.TenantID = "globex" },
		"other run":    func(id *auth.AgentIdentity) { id.RunID = "run-2" },
		// A rail re-dispatches the same try under a new attempt_epoch (ADR
		// 0051): the new execution must not inherit the fenced one's entry.
		"next execution": func(id *auth.AgentIdentity) { id.AttemptEpoch++; id.HasAttemptEpoch = true },
	} {
		id := base
		mutate(&id)
		before := store.loads.Load()
		_, _ = srv.FetchXCom(ctxFor(t, a, id), &agentv1.FetchXComRequest{UpstreamTaskId: "up1"})
		if store.loads.Load() == before {
			t.Errorf("%s reused another attempt's cached spec", name)
		}
	}
}

// TestAttemptSpecDroppedWhenAttemptEnds: a terminal report or a reschedule
// ends the attempt, and its cached view goes with it.
func TestAttemptSpecDroppedWhenAttemptEnds(t *testing.T) {
	for name, req := range map[string]*agentv1.ReportStateRequest{
		"success":    {State: agentv1.TaskState_TASK_STATE_SUCCESS},
		"failed":     {State: agentv1.TaskState_TASK_STATE_FAILED, ExitCode: 1},
		"reschedule": {State: agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE, RescheduleAt: timestamppb.Now()},
	} {
		t.Run(name, func(t *testing.T) {
			store := xcomAttemptStore()
			srv, a := newServerX(store, xcomAttemptEntries())
			ctx := ctxWithToken(t, a)
			runXComRPCs(t, srv, ctx)
			if _, err := srv.ReportState(ctx, req); err != nil {
				t.Fatalf("ReportState: %v", err)
			}
			if n := srv.attemptSpecs.len(); n != 0 {
				t.Errorf("cache holds %d attempts after the attempt ended, want 0", n)
			}
		})
	}
}

// TestAttemptSpecKeptWhileRunning: a running report does not end the attempt.
func TestAttemptSpecKeptWhileRunning(t *testing.T) {
	store := xcomAttemptStore()
	srv, a := newServerX(store, xcomAttemptEntries())
	ctx := ctxWithToken(t, a)
	runXComRPCs(t, srv, ctx)
	if _, err := srv.ReportState(ctx, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_RUNNING}); err != nil {
		t.Fatalf("ReportState: %v", err)
	}
	runXComRPCs(t, srv, ctx)
	if got := store.loads.Load(); got != 1 {
		t.Errorf("task spec loaded %d times, want 1", got)
	}
}

// TestAttemptSpecLoadErrorNotCached: a failed load is retried on the next RPC.
func TestAttemptSpecLoadErrorNotCached(t *testing.T) {
	store := xcomAttemptStore()
	store.specErr = errors.New("db down")
	srv, a := newServerX(store, xcomAttemptEntries())
	ctx := ctxWithToken(t, a)
	if _, err := srv.FetchXCom(ctx, &agentv1.FetchXComRequest{UpstreamTaskId: "up1"}); err == nil {
		t.Fatal("FetchXCom succeeded with a failing store")
	}
	store.specErr = nil
	if _, err := srv.FetchXCom(ctx, &agentv1.FetchXComRequest{UpstreamTaskId: "up1"}); err != nil {
		t.Fatalf("FetchXCom after the store recovered: %v", err)
	}
}

// TestAttemptSpecCacheBoundedAndExpires: attempts that die without reporting
// must not grow the cache forever.
func TestAttemptSpecCacheBoundedAndExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newAttemptSpecCache(2, time.Minute, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		id := testIdentity()
		id.TryNumber = i + 1
		c.put(id, attemptSpec{})
	}
	if n := c.len(); n > 2 {
		t.Errorf("cache holds %d attempts, want at most 2", n)
	}
	id := testIdentity()
	id.TryNumber = 5
	if _, ok := c.get(id); !ok {
		t.Fatal("the newest attempt was evicted")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.get(id); ok {
		t.Error("an expired attempt was served from the cache")
	}
}

// BenchmarkAttemptXComRPCs measures one attempt's spec and XCom RPCs:
// GetTaskSpec, three FetchXCom and one PushXCom. loads/attempt is the number
// of task spec loads (two Postgres queries each with the real store).
func BenchmarkAttemptXComRPCs(b *testing.B) {
	store := xcomAttemptStore()
	srv, a := newServerX(store, xcomAttemptEntries())
	ctxs := make([]context.Context, 64)
	for i := range ctxs {
		id := testIdentity()
		id.TryNumber = i + 1
		ctxs[i] = ctxFor(b, a, id)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := ctxs[i%len(ctxs)]
		if _, err := srv.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{}); err != nil {
			b.Fatal(err)
		}
		runXComRPCs(b, srv, ctx)
		if _, err := srv.ReportState(ctx, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_SUCCESS}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(store.loads.Load())/float64(b.N), "loads/attempt")
}
