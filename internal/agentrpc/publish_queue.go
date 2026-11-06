package agentrpc

import (
	"context"
	"log/slog"
	"sync"

	"github.com/dexadata/dexaflow/internal/logs"
)

// LogBatchPublisher is implemented by a LogPublisher that can publish several
// lines of one attempt in one round trip (logs.RedisTailer pipelines them).
// Without it the queue publishes one line at a time.
type LogBatchPublisher interface {
	PublishBatch(ctx context.Context, ref logs.Ref, lines []string) error
}

// tailPublishBatch is the most lines one round trip publishes;
// tailPublishQueueDepth and tailPublishQueueBytes bound what one stream may
// have waiting for the publisher, in lines and in bytes. The byte bound is the
// replay ring's (tailReplayMaxBytes), so a stalled Redis holds at most what an
// on_demand gate may already hold per stream; a single line larger than it
// waits for the queue to empty and then goes alone. vars (not consts) so tests
// can change them.
var (
	tailPublishBatch      = 256
	tailPublishQueueDepth = 1024
	tailPublishQueueBytes = 1 << 20 // 1 MiB
)

// publishQueue moves one log stream's live-tail publishing off its receive
// loop (#2356). A PUBLISH per line inside the loop capped a stream at one line
// per Redis round trip (about 18k lines a second on localhost, 1k to 2k
// against a managed Redis) whether anyone followed the attempt or not. The loop
// now hands each line to this queue and goes on storing; one goroutine per
// stream publishes what is waiting, up to tailPublishBatch lines per round
// trip, in the order the lines arrived. The queue is bounded in lines and in
// bytes: when the publisher falls a whole queue behind, push waits, which is
// the old behavior's back pressure at a much higher rate, and never a dropped
// line.
//
// push and close are called from the stream's receive loop only.
type publishQueue struct {
	pub   LogPublisher
	batch LogBatchPublisher // nil: one Publish per line
	ref   logs.Ref

	lines chan string
	done  chan struct{}
	once  sync.Once
	start sync.Once

	mu     sync.Mutex
	room   *sync.Cond // signaled when queued bytes are published
	queued int        // bytes pushed and not yet published
}

func newPublishQueue(pub LogPublisher, ref logs.Ref) *publishQueue {
	q := &publishQueue{pub: pub, ref: ref, lines: make(chan string, tailPublishQueueDepth), done: make(chan struct{})}
	q.batch, _ = pub.(LogBatchPublisher)
	q.room = sync.NewCond(&q.mu)
	return q
}

// push queues one encoded line. The first push starts the publisher with the
// stream's context, which stays live until the stream handler returns, after
// close has drained the queue.
func (q *publishQueue) push(ctx context.Context, line string) {
	q.start.Do(func() { go q.run(ctx) })
	q.mu.Lock()
	for q.queued > 0 && q.queued+len(line) > tailPublishQueueBytes {
		q.room.Wait()
	}
	q.queued += len(line)
	q.mu.Unlock()
	q.lines <- line
}

// published returns the room the lines of one round trip held.
func (q *publishQueue) published(lines []string) {
	n := 0
	for _, line := range lines {
		n += len(line)
	}
	q.mu.Lock()
	q.queued -= n
	q.mu.Unlock()
	q.room.Broadcast()
}

// close publishes what is still queued and waits for it, so every line the
// stream received is offered to the tail before StreamLogs returns.
func (q *publishQueue) close() {
	q.once.Do(func() {
		close(q.lines)
		started := true
		q.start.Do(func() { started = false })
		if started {
			<-q.done
		}
	})
}

func (q *publishQueue) run(ctx context.Context) {
	defer close(q.done)
	buf := make([]string, 0, tailPublishBatch)
	warned := false
	for line := range q.lines {
		buf = append(buf[:0], line)
	more:
		for len(buf) < tailPublishBatch {
			select {
			case next, ok := <-q.lines:
				if !ok {
					break more
				}
				buf = append(buf, next)
			default:
				break more
			}
		}
		err := q.publish(ctx, buf)
		q.published(buf)
		if err != nil {
			// One warning per failure streak, not one per batch: a Redis outage
			// under a chatty task would otherwise log at the task's line rate.
			if !warned {
				slog.Warn("publishing log tail", "task", q.ref.TaskID, "lines", len(buf), "error", err)
				warned = true
			}
			continue
		}
		warned = false
	}
}

func (q *publishQueue) publish(ctx context.Context, lines []string) error {
	if q.batch != nil {
		return q.batch.PublishBatch(ctx, q.ref, lines)
	}
	var first error
	for _, line := range lines {
		if err := q.pub.Publish(ctx, q.ref, line); err != nil && first == nil {
			first = err
		}
	}
	return first
}
