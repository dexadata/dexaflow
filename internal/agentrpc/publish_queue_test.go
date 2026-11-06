package agentrpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// gatedBatchPublisher publishes in batches and blocks every call until release
// is closed, so a test can let lines pile up behind a slow Redis.
type gatedBatchPublisher struct {
	release chan struct{}
	mu      sync.Mutex
	batches [][]string
	single  int
}

func newGatedBatchPublisher() *gatedBatchPublisher {
	return &gatedBatchPublisher{release: make(chan struct{})}
}

func (p *gatedBatchPublisher) Publish(_ context.Context, _ logs.Ref, line string) error {
	<-p.release
	p.mu.Lock()
	defer p.mu.Unlock()
	p.single++
	p.batches = append(p.batches, []string{line})
	return nil
}

func (p *gatedBatchPublisher) PublishBatch(_ context.Context, _ logs.Ref, lines []string) error {
	<-p.release
	p.mu.Lock()
	defer p.mu.Unlock()
	p.batches = append(p.batches, append([]string(nil), lines...))
	return nil
}

func (p *gatedBatchPublisher) snapshot() (lines []string, batches int, single int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.batches {
		lines = append(lines, b...)
	}
	return lines, len(p.batches), p.single
}

// orderedPublisher records every line from Publish, safe across goroutines.
type orderedPublisher struct {
	mu    sync.Mutex
	lines []string
}

func (p *orderedPublisher) Publish(_ context.Context, _ logs.Ref, line string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, line)
	return nil
}

func numbered(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("l%d", i)
	}
	return out
}

// TestPublishQueueBatchesWhatPilesUp (#2356): while one round trip is in
// flight, the lines that arrive wait in the queue and leave together, at most
// tailPublishBatch per round trip, in the order they arrived.
func TestPublishQueueBatchesWhatPilesUp(t *testing.T) {
	pub := newGatedBatchPublisher()
	q := newPublishQueue(pub, logs.Ref{TaskID: "t"})
	want := numbered(600)
	for _, line := range want {
		q.push(context.Background(), line)
	}
	close(pub.release)
	q.close()
	got, batches, single := pub.snapshot()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("published %d lines out of order or with gaps, want %d in order", len(got), len(want))
	}
	if single != 0 {
		t.Errorf("%d lines went out one by one, want every line through PublishBatch", single)
	}
	// The first line may leave alone; the 599 behind it need at least 3 batches of 256.
	if batches > 4 {
		t.Errorf("published in %d round trips, want at most 4 for 600 lines that piled up", batches)
	}
	pub.mu.Lock()
	defer pub.mu.Unlock()
	for i, b := range pub.batches {
		if len(b) > tailPublishBatch {
			t.Errorf("batch %d has %d lines, want at most %d", i, len(b), tailPublishBatch)
		}
	}
}

// TestPublishQueueFallsBackToOneLineAtATime: a publisher without PublishBatch
// (the in-process tailer of Lite) still receives every line, in order.
func TestPublishQueueFallsBackToOneLineAtATime(t *testing.T) {
	pub := &orderedPublisher{}
	q := newPublishQueue(pub, logs.Ref{TaskID: "t"})
	want := numbered(300)
	for _, line := range want {
		q.push(context.Background(), line)
	}
	q.close()
	if strings.Join(pub.lines, ",") != strings.Join(want, ",") {
		t.Fatalf("published %d lines, want %d in order", len(pub.lines), len(want))
	}
}

// TestPublishQueueCloseWithoutLines: a stream that never published has no
// goroutine to wait for, and closing it twice is harmless.
func TestPublishQueueCloseWithoutLines(t *testing.T) {
	q := newPublishQueue(&orderedPublisher{}, logs.Ref{TaskID: "t"})
	q.close()
	q.close()
}

// TestStreamLogsStoresWhileTheTailPublishIsSlow (#2356): the receive loop no
// longer waits for a PUBLISH per line. With the publisher stuck, every line of
// the stream still reaches the store; the published lines follow, complete and
// in order, once the publisher answers, before StreamLogs returns.
func TestStreamLogsStoresWhileTheTailPublishIsSlow(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sink := &captureSink{}
	srv.SetLogSink(sink)
	pub := newGatedBatchPublisher()
	srv.SetLogPublisher(pub)
	n := tailPublishQueueDepth / 2
	msgs := make([]*agentv1.LogLine, n)
	for i := range msgs {
		msgs[i] = &agentv1.LogLine{Message: fmt.Sprintf("l%d", i), Stream: "stdout"}
	}
	done := make(chan error, 1)
	go func() { done <- srv.StreamLogs(&fakeStreamLogsServer{ctx: ctxWithToken(t, a), msgs: msgs}) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if stored, _ := sink.snapshot(); len(stored) == n {
			break
		}
		if time.Now().After(deadline) {
			stored, _ := sink.snapshot()
			t.Fatalf("stored %d of %d lines while the publisher was stuck: the receive loop waits for the publish", len(stored), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(pub.release)
	if err := <-done; err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	got, _, _ := pub.snapshot()
	if len(got) != n {
		t.Fatalf("published %d of %d lines", len(got), n)
	}
	for i, line := range got {
		if msg := logs.DecodeLine(line).Message; msg != msgs[i].GetMessage() {
			t.Fatalf("published line %d = %q, want %q", i, msg, msgs[i].GetMessage())
		}
	}
}

// TestPublishQueueBoundsWaitingBytes: with the publisher stuck, a stream holds
// at most tailPublishQueueBytes of lines (a lone line may exceed it); the next
// push waits until a round trip frees room, and nothing is dropped.
func TestPublishQueueBoundsWaitingBytes(t *testing.T) {
	defer func(b int) { tailPublishQueueBytes = b }(tailPublishQueueBytes)
	tailPublishQueueBytes = 10
	pub := newGatedBatchPublisher()
	q := newPublishQueue(pub, logs.Ref{TaskID: "t"})
	q.push(context.Background(), "aaaaaaaaaaaa") // 12 bytes: alone it may exceed the bound
	pushed := make(chan struct{})
	go func() {
		q.push(context.Background(), "bbbb")
		close(pushed)
	}()
	select {
	case <-pushed:
		t.Fatal("a push went through with the queue over its byte bound")
	case <-time.After(50 * time.Millisecond):
	}
	close(pub.release)
	select {
	case <-pushed:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting push never went through after the publisher answered")
	}
	q.close()
	got, _, _ := pub.snapshot()
	if strings.Join(got, ",") != "aaaaaaaaaaaa,bbbb" {
		t.Fatalf("published %q, want both lines in order", got)
	}
}
