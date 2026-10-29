package dispatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/pkg/admission"
)

// The dispatch admission hooks (ADR 0100) run after the built-in refusals and
// before the agent token is minted.

type fakeAdmissionRecorder struct {
	mu       sync.Mutex
	outcomes []string
}

func (r *fakeAdmissionRecorder) RecordAdmission(outcome string, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, outcome)
}

func (r *fakeAdmissionRecorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.outcomes) == 0 {
		return ""
	}
	return r.outcomes[len(r.outcomes)-1]
}

func admissionDispatcher(t *testing.T, opts AdmissionOptions, hooks ...admission.Hook) (*Dispatcher, *fakeExecutor, *fakeIssuer, *fakeAdmissionRecorder) {
	t.Helper()
	exec := &fakeExecutor{}
	iss := &fakeIssuer{token: "t"}
	rec := &fakeAdmissionRecorder{}
	if opts.Recorder == nil {
		opts.Recorder = rec
	}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", TenantID: "acme", Image: "etl:v1", TryNumber: 2}}, iss, exec)
	d.SetAdmission(hooks, opts)
	return d, exec, iss, rec
}

func admit(dec admission.Decision) admission.Hook {
	return admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) { return dec, nil })
}

func TestAdmissionWithoutHooksLeavesDispatchAlone(t *testing.T) {
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{})
	if d.admission != nil {
		t.Fatal("no hooks should install no chain")
	}
	if disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); err != nil || disp != executor.Dispatched {
		t.Fatalf("Dispatch = %v, %v", disp, err)
	}
	if exec.req.AuthoredExecution != nil {
		t.Error("AuthoredExecution set without hooks")
	}
}

func TestAdmissionHookSeesTheEffectiveAttempt(t *testing.T) {
	var got *admission.Request
	hook := admission.HookFunc(func(_ context.Context, r *admission.Request) (admission.Decision, error) {
		got = r
		r.Execution.Labels["mutated"] = "x" // a hook editing its copy must change nothing
		return admission.Admit(), nil
	})
	d, exec, _, rec := admissionDispatcher(t, AdmissionOptions{}, hook)
	d.SetDefaultTaskServiceAccount("default-sa")
	limits, err := domain.ParseDefaultLimits("", "1Gi")
	if err != nil {
		t.Fatal(err)
	}
	d.SetPlatformDefaults(PlatformDefaults{Limits: limits})
	task := pythonTask()
	task.Execution = &domain.Execution{Labels: map[string]string{"team": "data"}}
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", task); err != nil {
		t.Fatal(err)
	}
	if got.TenantID != "acme" || got.DagID != "etl" || got.RunID != "run" || got.TaskID != "extract" || got.TryNumber != 2 ||
		got.Operator != "python" || got.Image != "etl:v1" {
		t.Errorf("identity wrong: %+v", got)
	}
	if got.Resources.Requests.CPU != "500m" || got.Resources.Limits.Memory != "1Gi" {
		t.Errorf("resources should be the effective ones: %+v", got.Resources)
	}
	if got.Execution.ServiceAccount != "default-sa" || got.ServiceAccountChosen {
		t.Errorf("service account = %q chosen=%v; want the default, not chosen", got.Execution.ServiceAccount, got.ServiceAccountChosen)
	}
	if _, ok := exec.req.Execution.Labels["mutated"]; ok || task.Execution.Labels["mutated"] != "" {
		t.Error("a hook editing its request changed the attempt")
	}
	if rec.last() != AdmissionAdmitted {
		t.Errorf("outcome = %q", rec.last())
	}
	if exec.req.AuthoredExecution != nil {
		t.Error("an unchanged attempt carries AuthoredExecution")
	}
}

func TestAdmissionHookChangesPlacementAndMetadata(t *testing.T) {
	first := admit(admission.AdmitWith(admission.Changes{
		NodeSelector: map[string]string{"pool": "batch"},
		Tolerations:  []admission.Toleration{{Key: "batch", Operator: "Exists"}},
		Labels:       map[string]string{"cost-center": "42"},
	}))
	var secondSaw admission.Execution
	second := admission.HookFunc(func(_ context.Context, r *admission.Request) (admission.Decision, error) {
		secondSaw = r.Execution
		return admission.AdmitWith(admission.Changes{PriorityClassName: "low", Labels: map[string]string{"cost-center": "43"}}), nil
	})
	d, exec, _, rec := admissionDispatcher(t, AdmissionOptions{}, first, second)
	task := pythonTask()
	task.Execution = &domain.Execution{NodeSelector: map[string]string{"zone": "a"}, Labels: map[string]string{"team": "data"}}
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", task); err != nil {
		t.Fatal(err)
	}
	if secondSaw.NodeSelector["pool"] != "batch" {
		t.Errorf("the second hook should see the first one's changes: %+v", secondSaw)
	}
	e := exec.req.Execution
	if e.NodeSelector["zone"] != "a" || e.NodeSelector["pool"] != "batch" || len(e.Tolerations) != 1 ||
		e.PriorityClassName != "low" || e.Labels["team"] != "data" || e.Labels["cost-center"] != "43" {
		t.Errorf("changes not applied: %+v", e)
	}
	a := exec.req.AuthoredExecution
	if a == nil || a.NodeSelector["pool"] != "" || a.PriorityClassName != "" || a.Labels["cost-center"] != "" {
		t.Errorf("AuthoredExecution should be the task's own settings: %+v", a)
	}
	if task.Execution.NodeSelector["pool"] != "" {
		t.Error("the task's own execution settings were modified")
	}
	if rec.last() != AdmissionChanged {
		t.Errorf("outcome = %q", rec.last())
	}
}

func TestAdmissionRefusalIsPermanentAndMintsNoToken(t *testing.T) {
	called := false
	after := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		called = true
		return admission.Admit(), nil
	})
	d, exec, iss, rec := admissionDispatcher(t, AdmissionOptions{}, admit(admission.Refuse("over budget\nfor team a")), after)
	disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask())
	if disp != executor.Refused || err == nil {
		t.Fatalf("Dispatch = %v, %v; want Refused", disp, err)
	}
	if err.Error() != "admission: over budget for team a" {
		t.Errorf("error = %q", err)
	}
	if called || iss.id.TaskID != "" || exec.req.TaskID != "" {
		t.Error("a refused attempt reached a later hook, the issuer or the executor")
	}
	if rec.last() != AdmissionRefused {
		t.Errorf("outcome = %q", rec.last())
	}
}

func TestAdmissionRefusalReasonIsCut(t *testing.T) {
	d, _, _, _ := admissionDispatcher(t, AdmissionOptions{}, admit(admission.Refuse(strings.Repeat("é", 400))))
	_, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask())
	reason := strings.TrimPrefix(err.Error(), "admission: ")
	if len(reason) > maxAdmissionText || !strings.HasPrefix(strings.Repeat("é", 256), reason) {
		t.Errorf("reason not cut at a rune boundary within %d bytes: %d bytes", maxAdmissionText, len(reason))
	}
}

func TestAdmissionHookFailuresHoldTheAttempt(t *testing.T) {
	for name, hook := range map[string]admission.Hook{
		"error": admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
			return admission.Decision{}, errors.New("policy service down")
		}),
		"panic": admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
			panic("boom")
		}),
		"timeout": admission.HookFunc(func(ctx context.Context, _ *admission.Request) (admission.Decision, error) {
			<-ctx.Done()
			return admission.Refuse("too late"), nil
		}),
		"ignores its context": admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
			time.Sleep(200 * time.Millisecond)
			return admission.Admit(), nil
		}),
	} {
		t.Run(name, func(t *testing.T) {
			d, exec, iss, rec := admissionDispatcher(t, AdmissionOptions{Timeout: 20 * time.Millisecond}, hook)
			disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask())
			if disp != executor.Unavailable || err == nil || !strings.HasPrefix(err.Error(), "admission hook unavailable: ") {
				t.Fatalf("Dispatch = %v, %v; want Unavailable", disp, err)
			}
			if iss.id.TaskID != "" || exec.req.TaskID != "" {
				t.Error("a held attempt reached the issuer or the executor")
			}
			if rec.last() != AdmissionError {
				t.Errorf("outcome = %q", rec.last())
			}
		})
	}
}

func TestAdmissionFailOpenAdmitsUnchanged(t *testing.T) {
	failing := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		return admission.Decision{}, errors.New("down")
	})
	d, exec, _, rec := admissionDispatcher(t, AdmissionOptions{FailOpen: true}, failing)
	disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask())
	if err != nil || disp != executor.Dispatched {
		t.Fatalf("Dispatch = %v, %v; want Dispatched", disp, err)
	}
	if exec.req.TaskID != "extract" || exec.req.AuthoredExecution != nil {
		t.Errorf("fail open should dispatch the attempt unchanged: %+v", exec.req)
	}
	if rec.last() != AdmissionFailOpen {
		t.Errorf("outcome = %q", rec.last())
	}
}

func TestAdmissionBreakerSkipsHooksAfterRepeatedFailures(t *testing.T) {
	calls := 0
	fail := true
	hook := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		calls++
		if fail {
			return admission.Decision{}, errors.New("down")
		}
		return admission.Admit(), nil
	})
	d, _, _, rec := admissionDispatcher(t, AdmissionOptions{}, hook)
	now := time.Unix(1_000, 0)
	d.admission.now = func() time.Time { return now }
	for range admissionBreakerThreshold {
		if disp, _ := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); disp != executor.Unavailable {
			t.Fatalf("disp = %v", disp)
		}
	}
	disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask())
	if disp != executor.Unavailable || calls != admissionBreakerThreshold || rec.last() != AdmissionBreakerOpen {
		t.Fatalf("open breaker: disp=%v err=%v calls=%d outcome=%q", disp, err, calls, rec.last())
	}
	fail = false
	now = now.Add(admissionBreakerOpen)
	if disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); disp != executor.Dispatched {
		t.Fatalf("after the open window: %v, %v", disp, err)
	}
}

func TestAdmissionRefusesReservedMetadataKeys(t *testing.T) {
	for _, ch := range []admission.Changes{
		{Labels: map[string]string{"leoflow.io/task-id": "x"}},
		{Annotations: map[string]string{"dexaflow.io/anything": "x"}},
	} {
		d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{}, admit(admission.AdmitWith(ch)))
		disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask())
		if disp != executor.Refused || err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%+v: Dispatch = %v, %v; want Refused", ch, disp, err)
		}
		if exec.req.TaskID != "" {
			t.Error("refused attempt reached the executor")
		}
	}
}

func TestAdmissionServiceAccountMustBeAllowed(t *testing.T) {
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{ServiceAccounts: []string{"etl-sa"}})
	if err != nil {
		t.Fatal(err)
	}
	for sa, want := range map[string]executor.Disposition{
		"etl-sa":     executor.Dispatched,
		"default-sa": executor.Dispatched, // the operator's default is always allowed
		"admin":      executor.Refused,
	} {
		d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{}, admit(admission.AdmitWith(admission.Changes{ServiceAccount: sa})))
		d.SetDefaultTaskServiceAccount("default-sa")
		d.SetPlatformDefaults(PlatformDefaults{ExecutionPolicy: policy})
		disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask())
		if disp != want {
			t.Errorf("%s: Dispatch = %v, %v; want %v", sa, disp, err, want)
		}
		if want == executor.Dispatched && exec.req.Execution.ServiceAccount != sa {
			t.Errorf("%s: pod service account = %q", sa, exec.req.Execution.ServiceAccount)
		}
	}
}

// A hook's changes do not go through the field allowlist: the operator wrote
// the hook. The task's own fields still do, before the hook runs.
func TestAdmissionChangesBypassTheFieldAllowlistButTheTaskDoesNot(t *testing.T) {
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{Fields: []string{domain.ExecutionPolicyNoFields}})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	hook := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		called = true
		return admission.AdmitWith(admission.Changes{NodeSelector: map[string]string{"pool": "batch"}}), nil
	})
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{}, hook)
	d.SetPlatformDefaults(PlatformDefaults{ExecutionPolicy: policy})
	if disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); disp != executor.Dispatched {
		t.Fatalf("hook change: %v, %v", disp, err)
	}
	if exec.req.Execution.NodeSelector["pool"] != "batch" {
		t.Error("hook change not applied")
	}

	called = false
	task := pythonTask()
	task.Execution = &domain.Execution{NodeSelector: map[string]string{"pool": "gpu"}}
	if disp, _ := d.Dispatch(context.Background(), "run", "etl", "ver", task); disp != executor.Refused || called {
		t.Errorf("a task field outside the allowlist: disp=%v hook called=%v; want Refused before the hook", disp, called)
	}
}

// A hook that changes placement sends a warm-eligible attempt to a dedicated
// pod, since DedicatedReason sees the changed settings.
func TestAdmissionChangesSendTheAttemptToADedicatedPod(t *testing.T) {
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{}, admit(admission.AdmitWith(admission.Changes{NodeSelector: map[string]string{"pool": "batch"}})))
	placer := &fakePlacer{ok: true}
	d.SetWarmPlacer(placer)
	task := pythonTask()
	task.Resources = nil
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", task); err != nil {
		t.Fatal(err)
	}
	if placer.calls != 0 || exec.req.TaskID != "extract" {
		t.Errorf("changed attempt went warm (calls=%d) or skipped the pod path", placer.calls)
	}
}

type admissionSink struct {
	got chan executor.Disposition
}

func (s *admissionSink) MarkTaskDispatchFailed(context.Context, string, string, string) error {
	s.got <- executor.Rejected
	return nil
}

func (s *admissionSink) HandleDispatchFailure(_ context.Context, _, _ string, disp executor.Disposition, _ error) error {
	s.got <- disp
	return nil
}

// The buffered dispatcher runs the hooks on its workers and hands a refusal
// or a held attempt to its sink with the disposition the hook chain chose.
func TestAdmissionRunsInTheBufferedDispatcher(t *testing.T) {
	for want, hook := range map[executor.Disposition]admission.Hook{
		executor.Refused: admit(admission.Refuse("no")),
		executor.Unavailable: admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
			return admission.Decision{}, errors.New("down")
		}),
	} {
		testBufferedAdmission(t, want, hook)
	}
	testBufferedAdmission(t, executor.Unavailable, admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		panic("boom")
	}))
	testBufferedAdmission(t, executor.Unavailable, admission.HookFunc(func(ctx context.Context, _ *admission.Request) (admission.Decision, error) {
		<-ctx.Done()
		return admission.Admit(), nil
	}))
	testBufferedAdmission(t, executor.Unavailable, admit(admission.Hold("freeze")))
}

func testBufferedAdmission(t *testing.T, want executor.Disposition, hook admission.Hook) {
	t.Helper()
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{Timeout: 20 * time.Millisecond}, hook)
	sink := &admissionSink{got: make(chan executor.Disposition, 1)}
	b := NewBuffered(d, sink, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, BufferConfig{BufferSize: 4, Workers: 1})
	if _, err := b.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case got := <-sink.got:
		if got != want {
			t.Errorf("sink disposition = %v, want %v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sink was never called")
	}
	_ = b.Close()
	if exec.req.TaskID != "" {
		t.Error("attempt reached the executor")
	}
}

func TestAdmissionHoldIsNeverFailedOpenNorCounted(t *testing.T) {
	calls := 0
	hook := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		calls++
		return admission.Hold("freeze window\u202e"), nil
	})
	d, exec, iss, rec := admissionDispatcher(t, AdmissionOptions{FailOpen: true}, hook)
	for range admissionBreakerThreshold + 2 {
		disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask())
		if disp != executor.Unavailable || err == nil || err.Error() != "admission: held: freeze window" {
			t.Fatalf("Dispatch = %v, %v; want Unavailable with the cleaned reason", disp, err)
		}
		var s interface{ UnavailableSubject() string }
		if !errors.As(err, &s) || s.UnavailableSubject() != "held by an admission hook" {
			t.Errorf("cause should name the hold: %v", err)
		}
	}
	if calls != admissionBreakerThreshold+2 {
		t.Errorf("holds tripped the breaker: %d calls", calls)
	}
	if iss.id.TaskID != "" || exec.req.TaskID != "" || rec.last() != AdmissionHeld {
		t.Errorf("held attempt went on (issuer %q, executor %q) or outcome %q", iss.id.TaskID, exec.req.TaskID, rec.last())
	}
}

func TestAdmissionCancelledContextIsNotAHookFailure(t *testing.T) {
	hook := admission.HookFunc(func(ctx context.Context, _ *admission.Request) (admission.Decision, error) {
		<-ctx.Done()
		return admission.Decision{}, ctx.Err()
	})
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{FailOpen: true}, hook)
	for range admissionBreakerThreshold + 1 {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(5 * time.Millisecond); cancel() }()
		disp, err := d.Dispatch(ctx, "run", "etl", "ver", pythonTask())
		cancel()
		if disp != executor.Unavailable || !errors.Is(err, context.Canceled) {
			t.Fatalf("Dispatch = %v, %v; want Unavailable, not failed open", disp, err)
		}
	}
	h := d.admission.hooks[0]
	h.mu.Lock()
	defer h.mu.Unlock()
	if exec.req.TaskID != "" || h.failures != 0 || !h.openUntil.IsZero() {
		t.Errorf("canceled attempts reached the executor or counted against the hook (failures=%d)", h.failures)
	}
}

// Each hook has its own breaker, and after the open window a single call
// probes the hook: a failed probe opens it again at once.
func TestAdmissionBreakerIsPerHookWithOneProbe(t *testing.T) {
	badCalls, goodCalls := 0, 0
	bad := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		badCalls++
		return admission.Decision{}, errors.New("down")
	})
	good := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		goodCalls++
		return admission.Admit(), nil
	})
	d, _, _, _ := admissionDispatcher(t, AdmissionOptions{FailOpen: true}, good, bad)
	now := time.Unix(1_000, 0)
	d.admission.now = func() time.Time { return now }
	dispatch := func() {
		if disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); disp != executor.Dispatched {
			t.Fatalf("fail open: %v, %v", disp, err)
		}
	}
	for range admissionBreakerThreshold + 3 {
		dispatch()
	}
	if badCalls != admissionBreakerThreshold || goodCalls != admissionBreakerThreshold+3 {
		t.Fatalf("bad=%d good=%d; the open breaker of one hook must not skip the other", badCalls, goodCalls)
	}
	now = now.Add(admissionBreakerOpen)
	dispatch() // the probe
	dispatch()
	if badCalls != admissionBreakerThreshold+1 {
		t.Errorf("after the window: bad=%d; want one probe, then open again", badCalls)
	}
}

func TestAdmissionRefusesChangesKubernetesWouldReject(t *testing.T) {
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{PriorityClasses: []string{"low"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]admission.Changes{
		"priority class outside the allowlist": {PriorityClassName: "system-cluster-critical"},
		"label key":                            {Labels: map[string]string{"bad key": "x"}},
		"label value":                          {Labels: map[string]string{"team": "has spaces"}},
		"node selector key":                    {NodeSelector: map[string]string{"-x": "y"}},
		"annotation key":                       {Annotations: map[string]string{"a/b/c": "anything"}},
		"toleration operator":                  {Tolerations: []admission.Toleration{{Key: "k", Operator: "Maybe"}}},
		"toleration exists with value":         {Tolerations: []admission.Toleration{{Key: "k", Operator: "Exists", Value: "v"}}},
		"toleration seconds":                   {Tolerations: []admission.Toleration{{Key: "k", Effect: "NoSchedule", TolerationSeconds: new(int64)}}},
	} {
		d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{}, admit(admission.AdmitWith(ch)))
		d.SetPlatformDefaults(PlatformDefaults{ExecutionPolicy: policy})
		disp, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask())
		if disp != executor.Refused || err == nil || !strings.Contains(err.Error(), "hook 1") {
			t.Errorf("%s: Dispatch = %v, %v; want Refused naming the hook", name, disp, err)
		}
		if exec.req.TaskID != "" {
			t.Errorf("%s: refused attempt reached the executor", name)
		}
	}
}

func TestAdmissionTolerationsRoundTrip(t *testing.T) {
	secs := int64(60)
	var seen []admission.Toleration
	hook := admission.HookFunc(func(_ context.Context, r *admission.Request) (admission.Decision, error) {
		seen = r.Execution.Tolerations
		return admission.AdmitWith(admission.Changes{Tolerations: []admission.Toleration{{Key: "spot", Operator: "Exists", Effect: "NoExecute", TolerationSeconds: &secs}}}), nil
	})
	d, exec, _, _ := admissionDispatcher(t, AdmissionOptions{}, hook)
	task := pythonTask()
	task.Execution = &domain.Execution{Tolerations: []map[string]any{{"key": "gpu", "operator": "Equal", "value": "a100", "effect": "NoSchedule"}}}
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", task); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != (admission.Toleration{Key: "gpu", Operator: "Equal", Value: "a100", Effect: "NoSchedule"}) {
		t.Errorf("hook saw %+v", seen)
	}
	got := exec.req.Execution.Tolerations
	if len(got) != 2 || got[1]["key"] != "spot" || got[1]["tolerationSeconds"] != int64(60) || got[1]["effect"] != "NoExecute" {
		t.Errorf("pod tolerations = %+v", got)
	}
}

func BenchmarkDispatchAdmission(b *testing.B) {
	for name, hooks := range map[string][]admission.Hook{
		"none":  nil,
		"no-op": {admit(admission.Admit())},
		// A hook that never answers costs one timeout per attempt until its
		// breaker opens; the timeout here is 1ms instead of 500ms.
		"at-timeout": {admission.HookFunc(func(ctx context.Context, _ *admission.Request) (admission.Decision, error) {
			<-ctx.Done()
			return admission.Admit(), nil
		})},
	} {
		b.Run(name, func(b *testing.B) {
			d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, &fakeExecutor{})
			d.SetAdmission(hooks, AdmissionOptions{Timeout: time.Millisecond, FailOpen: true})
			task := pythonTask()
			b.ReportAllocs()
			for b.Loop() {
				if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", task); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
