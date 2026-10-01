package agent

import (
	"sync"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// TestTokenSourceSwap: the credential reads the live token, and a concurrent
// swap is observed by subsequent reads. Empty swaps never blank a working token.
func TestTokenSourceSwap(t *testing.T) {
	src := NewTokenSource("initial")
	cred := tokenAuth{source: src}

	md, err := cred.GetRequestMetadata(t.Context())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if got := md["authorization"]; got != "Bearer initial" {
		t.Errorf("authorization = %q, want %q", got, "Bearer initial")
	}

	src.Set("renewed")
	md, _ = cred.GetRequestMetadata(t.Context())
	if got := md["authorization"]; got != "Bearer renewed" {
		t.Errorf("after swap authorization = %q, want %q", got, "Bearer renewed")
	}

	src.Set("") // no-op: keep the current bearer
	if got := src.Token(); got != "renewed" {
		t.Errorf("empty swap changed the token to %q, want %q", got, "renewed")
	}
}

// TestTokenSourceConcurrent exercises the lock under -race: concurrent readers
// and a writer must not data-race.
func TestTokenSourceConcurrent(t *testing.T) {
	src := NewTokenSource("t0")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = src.Token() }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); src.Set("t1") }()
	wg.Wait()
}

// TestApplyHeartbeatResponseSwapsBearer: a heartbeat carrying a renewed token
// swaps the agent's bearer for subsequent RPCs; an empty one is left untouched;
// and should_terminate is surfaced.
func TestApplyHeartbeatResponseSwapsBearer(t *testing.T) {
	src := NewTokenSource("old")
	r := &Runner{Token: src}

	if term := r.applyHeartbeatResponse(&agentv1.HeartbeatResponse{RenewedToken: "new"}); term {
		t.Error("a live heartbeat must not signal terminate")
	}
	if got := src.Token(); got != "new" {
		t.Errorf("bearer after renewal = %q, want %q", got, "new")
	}

	// No renewed token: keep the current bearer.
	if term := r.applyHeartbeatResponse(&agentv1.HeartbeatResponse{}); term {
		t.Error("empty heartbeat must not signal terminate")
	}
	if got := src.Token(); got != "new" {
		t.Errorf("bearer after empty heartbeat = %q, want %q (unchanged)", got, "new")
	}

	// Superseded: terminate, and by construction no renewed token on that branch.
	if term := r.applyHeartbeatResponse(&agentv1.HeartbeatResponse{ShouldTerminate: true}); !term {
		t.Error("superseded heartbeat must signal terminate")
	}
}

// TestApplyHeartbeatResponseNilToken: with no TokenSource wired, a renewed token
// is ignored rather than panicking.
func TestApplyHeartbeatResponseNilToken(t *testing.T) {
	r := &Runner{}
	if term := r.applyHeartbeatResponse(&agentv1.HeartbeatResponse{RenewedToken: "x"}); term {
		t.Error("unexpected terminate")
	}
}

// TestAttemptTokenTTL: the derived per-attempt TTL always exceeds a single
// heartbeat interval (so one missed beat survives), honors the floor for small
// intervals, and scales past the floor for large ones.
func TestAttemptTokenTTL(t *testing.T) {
	// Production interval (15s): floor dominates and gives dozens of beats of
	// tolerance.
	if ttl := AttemptTokenTTL(DefaultHeartbeatInterval); ttl != attemptTokenTTLFloor {
		t.Errorf("AttemptTokenTTL(15s) = %v, want the floor %v", ttl, attemptTokenTTLFloor)
	}
	// The floor exceeds the interval × missed-beat tolerance: several consecutive
	// missed beats still leave the credential valid.
	if attemptTokenTTLFloor <= DefaultHeartbeatInterval*attemptTokenTTLBeats {
		t.Errorf("floor %v must exceed interval×beats %v", attemptTokenTTLFloor, DefaultHeartbeatInterval*attemptTokenTTLBeats)
	}
	// A single missed beat is always survivable at any interval.
	for _, iv := range []time.Duration{time.Second, 15 * time.Second, time.Minute, 10 * time.Minute} {
		if got := AttemptTokenTTL(iv); got <= iv {
			t.Errorf("AttemptTokenTTL(%v) = %v, must exceed one interval", iv, got)
		}
	}
	// A large interval scales past the floor: max(floor, beats×interval).
	if ttl := AttemptTokenTTL(time.Hour); ttl != attemptTokenTTLBeats*time.Hour {
		t.Errorf("AttemptTokenTTL(1h) = %v, want %v", ttl, attemptTokenTTLBeats*time.Hour)
	}
}
