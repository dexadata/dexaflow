package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// scriptedHeartbeats answers heartbeats from a script: an entry true fails
// that heartbeat, false answers it. Past the script every heartbeat fails when
// failAfter is set and is answered otherwise.
type scriptedHeartbeats struct {
	fakeClient
	mu        sync.Mutex
	script    []bool
	failAfter bool
	calls     int
}

func (s *scriptedHeartbeats) Heartbeat(context.Context, *agentv1.HeartbeatRequest, ...grpc.CallOption) (*agentv1.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := s.failAfter
	if s.calls < len(s.script) {
		fail = s.script[s.calls]
	}
	s.calls++
	if fail {
		return nil, errors.New("unavailable")
	}
	return &agentv1.HeartbeatResponse{}, nil
}

func (s *scriptedHeartbeats) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// runHeartbeat drives the heartbeat loop until it returns or the client has
// answered calls heartbeats, and reports whether the loop canceled the task.
func runHeartbeat(t *testing.T, r *Runner, client *scriptedHeartbeats, calls int) bool {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.heartbeat(ctx, func() { close(canceled); cancel() })
	}()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-canceled:
			<-done
			return true
		case <-deadline:
			t.Fatal("heartbeat loop neither canceled nor reached the call count")
		default:
		}
		if client.count() >= calls {
			cancel()
			<-done
			select {
			case <-canceled:
				return true
			default:
				return false
			}
		}
		time.Sleep(time.Millisecond)
	}
}

// #2430: an agent that cannot reach the control plane for
// MaxFailedHeartbeats heartbeats in a row stops its task, as Airflow's
// supervisor does after max_failed_heartbeats. The control plane has failed
// the attempt by then, and no outcome it reports can land.
func TestHeartbeatStopsAfterMaxConsecutiveFailures(t *testing.T) {
	client := &scriptedHeartbeats{failAfter: true}
	r := &Runner{Client: client, HeartbeatInterval: time.Millisecond, MaxFailedHeartbeats: 3}

	if !runHeartbeat(t, r, client, 50) {
		t.Fatal("three failed heartbeats in a row did not stop the task")
	}
	if got := client.count(); got != 3 {
		t.Errorf("stopped after %d heartbeats, want 3", got)
	}
	if !r.lostControlPlane.Load() {
		t.Error("the stop is not recorded as a lost control plane")
	}
}

// One answered heartbeat resets the count: failures that never reach the
// limit in a row never stop the task.
func TestHeartbeatAnsweredResetsTheFailureCount(t *testing.T) {
	client := &scriptedHeartbeats{script: []bool{true, true, false, true, true, false, true, true, false}}
	r := &Runner{Client: client, HeartbeatInterval: time.Millisecond, MaxFailedHeartbeats: 3}

	if runHeartbeat(t, r, client, 12) {
		t.Fatal("two failed heartbeats in a row stopped the task, limit 3")
	}
	if r.lostControlPlane.Load() {
		t.Error("lostControlPlane set without a stop")
	}
}

// Zero keeps the previous behavior: an agent keeps its task running however
// long the control plane is unreachable.
func TestHeartbeatZeroLimitNeverStops(t *testing.T) {
	client := &scriptedHeartbeats{failAfter: true}
	r := &Runner{Client: client, HeartbeatInterval: time.Millisecond}

	if runHeartbeat(t, r, client, 20) {
		t.Fatal("MaxFailedHeartbeats 0 stopped the task")
	}
}

// The whole attempt: the task is stopped and its terminal report names the
// lost control plane, not a bare exit code.
func TestRunnerReportsTheLostControlPlane(t *testing.T) {
	client := &scriptedHeartbeats{failAfter: true}
	client.spec = &agentv1.TaskSpec{Operator: "bash", Entrypoint: "sleep 1000"}
	r := newRunner(&client.fakeClient, &fakeCmd{blockUntilCancel: true}, &recordingSink{})
	r.Client = client
	r.HeartbeatInterval = time.Millisecond
	r.MaxFailedHeartbeats = 2

	err := r.Run(context.Background())

	if err == nil || !strings.Contains(err.Error(), "control plane") {
		t.Fatalf("Run = %v, want a failure naming the control plane", err)
	}
	last := client.reports[len(client.reports)-1]
	if last.GetState() != agentv1.TaskState_TASK_STATE_FAILED || !strings.Contains(last.GetErrorMessage(), "control plane") {
		t.Errorf("last report = %v %q, want FAILED naming the control plane", last.GetState(), last.GetErrorMessage())
	}
}

// The default stops an agent only once its attempt credential has lapsed
// unrenewed: by then the attempt can never report, while a control plane
// outage shorter than that (a restart, a failover) stops nothing.
func TestDefaultMaxFailedHeartbeatsSpansTheTokenTTL(t *testing.T) {
	window := time.Duration(DefaultMaxFailedHeartbeats) * DefaultHeartbeatInterval
	if ttl := AttemptTokenTTL(DefaultHeartbeatInterval); window < ttl {
		t.Errorf("default window %v is shorter than the attempt token TTL %v", window, ttl)
	}
}

// A warm worker's attempts stop on a lost control plane too.
func TestWarmAttemptRunnerCarriesMaxFailedHeartbeats(t *testing.T) {
	w := &WarmRunner{ScratchDir: t.TempDir(), HeartbeatInterval: time.Second, MaxFailedHeartbeats: 7}
	if got := w.attemptRunner(&recordingSink{}).MaxFailedHeartbeats; got != 7 {
		t.Fatalf("attempt runner MaxFailedHeartbeats = %d, want 7", got)
	}
}
