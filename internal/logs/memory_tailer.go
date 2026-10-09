package logs

import (
	"context"
	"sync"
)

// memTailerBuffer is the per-subscriber buffer for the in-process tailer. Sends
// are non-blocking, so a slow UI consumer drops lines rather than stalling the
// agent log path; the buffer absorbs ordinary bursts.
const memTailerBuffer = 256

// MemoryTailer is an in-process Tailer for Dexaflow Lite: it fans task log lines
// to live subscribers over Go channels, with no Redis. It is valid only within a
// single process (Lite runs the agent gRPC and the read API together); a
// multi-replica deployment must use RedisTailer instead (ADR 0026).
type MemoryTailer struct {
	mu   sync.Mutex
	subs map[string]map[int]chan string
	next int
}

// NewMemoryTailer builds an empty in-process tailer.
func NewMemoryTailer() *MemoryTailer {
	return &MemoryTailer{subs: make(map[string]map[int]chan string)}
}

// Publish delivers one line to every live subscriber of the task. Delivery is
// non-blocking: a full subscriber buffer drops the line rather than stalling the
// publisher. Holding the lock across the sends keeps it safe against a
// concurrent cancel closing a channel.
func (t *MemoryTailer) Publish(_ context.Context, ref Ref, line string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ch := range t.subs[ref.Channel()] {
		select {
		case ch <- line:
		default:
		}
	}
	return nil
}

// HasSubscribers reports whether the attempt has a live subscriber.
func (t *MemoryTailer) HasSubscribers(_ context.Context, ref Ref) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.subs[ref.Channel()]) > 0, nil
}

// Subscribe returns a channel of live log lines for the task and a cancel
// function. The channel is closed on cancel or when the context is done.
func (t *MemoryTailer) Subscribe(ctx context.Context, ref Ref) (lines <-chan string, cancel func()) {
	key := ref.Channel()
	ch := make(chan string, memTailerBuffer)
	t.mu.Lock()
	id := t.next
	t.next++
	if t.subs[key] == nil {
		t.subs[key] = make(map[int]chan string)
	}
	t.subs[key][id] = ch
	t.mu.Unlock()

	cancelFn := func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if _, ok := t.subs[key][id]; !ok {
			return // already canceled
		}
		delete(t.subs[key], id)
		if len(t.subs[key]) == 0 {
			delete(t.subs, key)
		}
		close(ch)
	}
	go func() {
		<-ctx.Done()
		cancelFn()
	}()
	return ch, cancelFn
}
