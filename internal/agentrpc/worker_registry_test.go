package agentrpc

import (
	"errors"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// registryClock is a settable clock for the registry's liveness grace.
type registryClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *registryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *registryClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClockedRegistry() (*WorkerRegistry, *registryClock) {
	clk := &registryClock{now: time.Unix(1_700_000_000, 0)}
	reg := NewWorkerRegistry(nil)
	reg.now = clk.Now
	return reg, clk
}

// A second registration under an identity whose first stream is still connected
// and heartbeating is refused, and the live entry is left untouched.
func TestRegisterRefusesDuplicateWhileLive(t *testing.T) {
	reg, clk := newClockedRegistry()
	live := make(chan struct{})
	first, err := reg.Register("w1", "v1", "pod-a", make(chan *agentv1.WorkAssignment, 1), live)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}

	clk.Advance(workerLivenessGrace / 2)
	second, err := reg.Register("w1", "v1", "pod-b", make(chan *agentv1.WorkAssignment, 1), make(chan struct{}))
	if !errors.Is(err, ErrWorkerAlreadyRegistered) {
		t.Fatalf("duplicate Register while live: err = %v, want ErrWorkerAlreadyRegistered", err)
	}
	if second != nil {
		t.Fatal("a refused Register must not return an entry")
	}
	if got := reg.podNameOf("w1"); got != "pod-a" {
		t.Fatalf("live entry pod = %q, want pod-a (the refused one must not replace it)", got)
	}
	select {
	case <-first.superseded:
		t.Fatal("a refused duplicate must not supersede the live stream")
	default:
	}

	// Heartbeats keep the entry live past the grace: a Touch at half the grace,
	// then another half, is never stale.
	reg.Touch(first)
	clk.Advance(workerLivenessGrace / 2)
	reg.Touch(first)
	clk.Advance(workerLivenessGrace / 2)
	if _, err := reg.Register("w1", "v1", "pod-c", make(chan *agentv1.WorkAssignment, 1), make(chan struct{})); !errors.Is(err, ErrWorkerAlreadyRegistered) {
		t.Fatalf("duplicate Register against a heartbeating stream: err = %v, want ErrWorkerAlreadyRegistered", err)
	}
	if reg.size() != 1 {
		t.Fatalf("registry size = %d, want 1", reg.size())
	}
}

// A reconnect after the previous stream has ended is accepted, whether or not
// the old stream's Deregister has run yet.
func TestRegisterAcceptsReconnectAfterStreamEnded(t *testing.T) {
	t.Run("stream ended, deregister not yet run", func(t *testing.T) {
		reg, _ := newClockedRegistry()
		done := make(chan struct{})
		first, err := reg.Register("w1", "v1", "pod-a", make(chan *agentv1.WorkAssignment, 1), done)
		if err != nil {
			t.Fatalf("first Register: %v", err)
		}
		close(done)
		if _, err := reg.Register("w1", "v1", "pod-b", make(chan *agentv1.WorkAssignment, 1), make(chan struct{})); err != nil {
			t.Fatalf("reconnect after the stream ended: %v", err)
		}
		if got := reg.podNameOf("w1"); got != "pod-b" {
			t.Fatalf("pod = %q, want pod-b", got)
		}
		// The ended stream's late Deregister must not evict the new entry.
		reg.Deregister(first)
		if !reg.registered("w1") || reg.size() != 1 {
			t.Fatal("the old stream's Deregister evicted the reconnected entry")
		}
	})
	t.Run("stream ended and deregistered", func(t *testing.T) {
		reg, _ := newClockedRegistry()
		done := make(chan struct{})
		first, err := reg.Register("w1", "v1", "pod-a", make(chan *agentv1.WorkAssignment, 1), done)
		if err != nil {
			t.Fatalf("first Register: %v", err)
		}
		close(done)
		reg.Deregister(first)
		if _, err := reg.Register("w1", "v1", "pod-b", make(chan *agentv1.WorkAssignment, 1), make(chan struct{})); err != nil {
			t.Fatalf("reconnect after deregister: %v", err)
		}
		if !reg.Assign("v1", &agentv1.WorkAssignment{AssignmentId: "as-1"}) {
			t.Fatal("the reconnected worker must start free")
		}
	})
}

// A stream that is still open at the transport level but has sent no heartbeat
// for longer than the grace is stale: a new registration replaces it, and the
// stale stream is told it was superseded so its handler ends.
func TestRegisterReplacesStaleEntryAfterGrace(t *testing.T) {
	reg, clk := newClockedRegistry()
	first, err := reg.Register("w1", "v1", "pod-a", make(chan *agentv1.WorkAssignment, 1), make(chan struct{}))
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}

	clk.Advance(workerLivenessGrace + time.Second)
	second, err := reg.Register("w1", "v1", "pod-b", make(chan *agentv1.WorkAssignment, 1), make(chan struct{}))
	if err != nil {
		t.Fatalf("Register over a stale entry: %v", err)
	}
	if got := reg.podNameOf("w1"); got != "pod-b" {
		t.Fatalf("pod = %q, want pod-b", got)
	}
	select {
	case <-first.superseded:
	default:
		t.Fatal("the stale entry must be told it was superseded")
	}
	select {
	case <-second.superseded:
		t.Fatal("the new entry must not be superseded")
	default:
	}
	// A heartbeat from the superseded stream must not refresh the new entry, and
	// its Deregister must not evict it.
	reg.Touch(first)
	reg.Deregister(first)
	if !reg.registered("w1") || reg.size() != 1 {
		t.Fatal("the superseded stream's Deregister evicted the replacement")
	}
}

// Without a stream-done signal (nil) the entry is judged by heartbeats alone.
func TestRegisterNilDoneUsesHeartbeatsOnly(t *testing.T) {
	reg, clk := newClockedRegistry()
	if _, err := reg.Register("w1", "v1", "pod-a", make(chan *agentv1.WorkAssignment, 1), nil); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if _, err := reg.Register("w1", "v1", "pod-b", make(chan *agentv1.WorkAssignment, 1), nil); !errors.Is(err, ErrWorkerAlreadyRegistered) {
		t.Fatalf("duplicate while fresh: err = %v, want ErrWorkerAlreadyRegistered", err)
	}
	clk.Advance(workerLivenessGrace + time.Second)
	if _, err := reg.Register("w1", "v1", "pod-b", make(chan *agentv1.WorkAssignment, 1), nil); err != nil {
		t.Fatalf("Register after the grace: %v", err)
	}
}

// mustRegister registers a worker with no stream-done signal and fails the test
// on refusal.
func mustRegister(t *testing.T, reg *WorkerRegistry, identity, dagVersion, podName string, send chan *agentv1.WorkAssignment) *registeredWorker {
	t.Helper()
	w, err := reg.Register(identity, dagVersion, podName, send, nil)
	if err != nil {
		t.Fatalf("Register(%q): %v", identity, err)
	}
	return w
}
