package agent

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
)

// gatedTokenSetter records every Token.Set call; each Set signals setCalled and
// then blocks on setGate. That lets a test freeze a heartbeat mid-Set and observe
// whether runOneAttempt waits for the heartbeat goroutine (the join) before it
// returns. It satisfies the tokenSetter seam that Runner.Token is typed to.
type gatedTokenSetter struct {
	setCalled chan string
	setGate   chan struct{}
	mu        sync.Mutex
	values    []string
}

func (g *gatedTokenSetter) Set(token string) {
	g.mu.Lock()
	g.values = append(g.values, token)
	g.mu.Unlock()
	g.setCalled <- token
	<-g.setGate
}

func (g *gatedTokenSetter) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.values)
}

// gatedHeartbeatClient embeds the standard fake client and overrides Heartbeat:
// the FIRST beat returns a renewed token (so applyHeartbeatResponse calls
// Token.Set exactly once), and every later beat blocks until ctx cancel and then
// returns promptly — so the loop issues no further Set.
type gatedHeartbeatClient struct {
	*fakeClient
	mu    sync.Mutex
	calls int
}

func (g *gatedHeartbeatClient) Heartbeat(ctx context.Context, _ *agentv1.HeartbeatRequest, _ ...grpc.CallOption) (*agentv1.HeartbeatResponse, error) {
	g.mu.Lock()
	g.calls++
	first := g.calls == 1
	g.mu.Unlock()
	if first {
		return &agentv1.HeartbeatResponse{RenewedToken: "hb-renewed"}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// blockingCmd blocks in Run until release is closed, guaranteeing the heartbeat
// fires (and its Set is in-flight) while the attempt is still running.
type blockingCmd struct {
	release chan struct{}
}

func (c *blockingCmd) Run(_ context.Context, _, _ []string, _, _ io.Writer) (int, error) {
	<-c.release
	return 0, nil
}

// TestRunOneAttemptJoinsHeartbeat is the M3 regression: the per-attempt heartbeat
// goroutine must be JOINED before runOneAttempt returns, so its last Token.Set
// (adopting a renewed bearer) completes before the warm loop swaps to the next
// attempt's token on the SHARED AttemptTokens. The test freezes a heartbeat
// inside Token.Set and proves runOneAttempt blocks until that Set finishes — with
// the join it does; without it (bare `go r.heartbeat` + `defer cancel()`)
// runOneAttempt returns while the Set is still in-flight, which is the bug: a
// lingering heartbeat clobbers the next attempt's token.
func TestRunOneAttemptJoinsHeartbeat(t *testing.T) {
	setter := &gatedTokenSetter{setCalled: make(chan string, 1), setGate: make(chan struct{})}
	client := &gatedHeartbeatClient{fakeClient: &fakeClient{
		spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:f"},
	}}
	cmd := &blockingCmd{release: make(chan struct{})}
	r := &Runner{
		Client:            client,
		Cmd:               cmd,
		Sink:              &recordingSink{},
		Hostname:          "pod-1",
		Version:           "test",
		Token:             setter,
		HeartbeatInterval: time.Millisecond,
	}

	done := make(chan struct{})
	go func() { _ = r.runOneAttempt(context.Background()); close(done) }()

	// A heartbeat Set is now in-flight, frozen on setGate.
	select {
	case v := <-setter.setCalled:
		if v != "hb-renewed" {
			t.Fatalf("Token.Set(%q), want the renewed token hb-renewed", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat never called Token.Set — cannot exercise the join")
	}

	// Let the user command finish so execute() reaches its deferred join.
	close(cmd.release)

	// With the join, runOneAttempt must still be blocked at hbWG.Wait(): the
	// heartbeat goroutine is frozen inside Set and has not returned. Without the
	// join it would already have returned.
	select {
	case <-done:
		t.Fatal("runOneAttempt returned while a heartbeat Set was still in-flight: the heartbeat goroutine is not joined (M3 bug)")
	case <-time.After(200 * time.Millisecond):
		// Expected: blocked on the join.
	}

	// Release the frozen Set; the heartbeat can finish its cycle, observe the
	// cancel, and exit — unblocking the join so the attempt returns.
	close(setter.setGate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runOneAttempt did not return after the heartbeat Set completed")
	}

	if n := setter.count(); n != 1 {
		t.Errorf("Token.Set called %d times, want exactly 1", n)
	}
}
