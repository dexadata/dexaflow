package logs

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
)

// tailChannel is the Redis pub/sub channel a task attempt's live log lines are
// published on. Both the agent-facing writer and the read API derive it from
// the same Ref, so they meet without an extra lookup.
func tailChannel(r Ref) string {
	return "log_tail:" + r.TenantID + ":" + r.DagID + ":" + r.RunID + ":" + r.TaskID + ":" + strconv.Itoa(r.TryNumber)
}

// Tailer publishes and subscribes to a task's live log lines for the UI's live
// tail. RedisTailer is the production implementation.
type Tailer interface {
	Publish(ctx context.Context, ref Ref, line string) error
	Subscribe(ctx context.Context, ref Ref) (<-chan string, func())
}

// RedisTailer fans task log lines out over Redis pub/sub so the API can stream
// them live to the UI without polling.
type RedisTailer struct {
	client redis.UniversalClient
}

// NewRedisTailer builds a RedisTailer over the given go-redis client.
func NewRedisTailer(client redis.UniversalClient) *RedisTailer { return &RedisTailer{client: client} }

// Publish sends one log line to the task's tail channel.
func (t *RedisTailer) Publish(ctx context.Context, ref Ref, line string) error {
	if err := t.client.Publish(ctx, tailChannel(ref), line).Err(); err != nil {
		return fmt.Errorf("publishing log tail: %w", err)
	}
	return nil
}

// HasSubscribers reports whether any client, on any replica, may be listening
// on the attempt's tail channel, so the writer can skip publishing lines nobody
// would receive. NUMSUB counts the channel's own subscribers; NUMPAT counts
// pattern subscriptions, which NUMSUB cannot attribute to a channel, so any
// pattern subscriber (say, a forwarder on log_tail:*) keeps every attempt
// publishing. Both go in one round trip. On a Redis Cluster both count only
// the node they run on, while a subscriber may hold its connection on any
// node, so every node is asked and the counts are summed (#2358).
func (t *RedisTailer) HasSubscribers(ctx context.Context, ref Ref) (bool, error) {
	ch := tailChannel(ref)
	if cc, ok := t.client.(*redis.ClusterClient); ok {
		var heard atomic.Bool
		err := cc.ForEachShard(ctx, func(ctx context.Context, node *redis.Client) error {
			n, err := countSubscribers(ctx, node, ch)
			if n {
				heard.Store(true)
			}
			return err
		})
		if heard.Load() {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("counting log tail subscribers: %w", err)
		}
		return false, nil
	}
	n, err := countSubscribers(ctx, t.client, ch)
	if err != nil {
		return false, fmt.Errorf("counting log tail subscribers: %w", err)
	}
	return n, nil
}

// countSubscribers asks one node for the channel's subscribers and the
// pattern subscriptions, in one round trip.
func countSubscribers(ctx context.Context, c redis.Cmdable, ch string) (bool, error) {
	var numSub *redis.MapStringIntCmd
	var numPat *redis.IntCmd
	if _, err := c.Pipelined(ctx, func(p redis.Pipeliner) error {
		numSub = p.PubSubNumSub(ctx, ch)
		numPat = p.PubSubNumPat(ctx)
		return nil
	}); err != nil {
		return false, err
	}
	return numSub.Val()[ch] > 0 || numPat.Val() > 0, nil
}

// Subscribe returns a channel of live log lines for the task and a cancel
// function that ends the subscription. The line channel closes when the
// subscription is canceled or the context is done.
func (t *RedisTailer) Subscribe(ctx context.Context, ref Ref) (lines <-chan string, cancel func()) {
	sub := t.client.Subscribe(ctx, tailChannel(ref))
	ch := make(chan string)
	go func() {
		defer close(ch)
		for msg := range sub.Channel() {
			select {
			case ch <- msg.Payload:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, func() {
		if err := sub.Close(); err != nil {
			slog.Warn("closing log tail subscription", "error", err)
		}
	}
}
