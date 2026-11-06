//go:build integration

// Package storage_test: Redis as a Cluster (#2358). XCom keys of one run
// hash to different slots, so a multi-key read and a pattern listing must
// reach every master instead of failing with CROSSSLOT or seeing one node.
package storage_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/xcom"
)

func TestRedisClusterXComAcrossSlots(t *testing.T) {
	addr := os.Getenv("DEXAFLOW_TEST_REDIS_CLUSTER")
	if addr == "" {
		t.Skip("DEXAFLOW_TEST_REDIS_CLUSTER is not set; start one with scripts/redis-cluster-up.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rd, err := storage.NewRedis(ctx, config.RedisSection{Cluster: config.RedisClusterSection{Addrs: []string{addr}}})
	if err != nil {
		t.Fatalf("NewRedis as a Cluster: %v", err)
	}
	defer rd.Close()
	if !rd.Cluster {
		t.Fatal("NewRedis did not report cluster mode")
	}
	cc, ok := rd.Client.(*redis.ClusterClient)
	if !ok {
		t.Fatalf("client is %T, want *redis.ClusterClient", rd.Client)
	}
	backend := xcom.NewRedisBackend(rd.Client)

	run := fmt.Sprintf("run-cluster-%d", time.Now().UnixNano())
	var keys []string
	slots := map[int64]bool{}
	for i := 0; i < 32; i++ {
		key := fmt.Sprintf("xcom:t:dag:%s:task%d:return_value", run, i)
		keys = append(keys, key)
		slot, err := cc.ClusterKeySlot(ctx, key).Result()
		if err != nil {
			t.Fatalf("CLUSTER KEYSLOT: %v", err)
		}
		slots[slot] = true
		entry := xcom.Entry{Value: []byte(fmt.Sprintf(`{"n":%d}`, i)), ContentType: "application/json", CreatedAt: time.Now()}
		if err := backend.Push(ctx, key, entry, time.Minute); err != nil {
			t.Fatalf("push %s: %v", key, err)
		}
	}
	if len(slots) < 2 {
		t.Fatalf("the keys share one slot, so the test proves nothing")
	}
	t.Cleanup(func() {
		for _, k := range keys {
			_ = backend.Delete(context.Background(), k)
		}
	})

	missing := fmt.Sprintf("xcom:t:dag:%s:absent:return_value", run)
	entries, found, err := backend.FetchMany(ctx, append(append([]string{}, keys...), missing))
	if err != nil {
		t.Fatalf("FetchMany across %d slots: %v", len(slots), err)
	}
	for i := range keys {
		if !found[i] || string(entries[i].Value) != fmt.Sprintf(`{"n":%d}`, i) {
			t.Errorf("key %d: found %v value %s", i, found[i], entries[i].Value)
		}
	}
	if found[len(keys)] {
		t.Error("an absent key was reported found")
	}

	listed, err := backend.List(ctx, fmt.Sprintf("xcom:t:dag:%s:*", run))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(listed)
	want := append([]string{}, keys...)
	sort.Strings(want)
	if fmt.Sprint(listed) != fmt.Sprint(want) {
		t.Errorf("List saw %d keys, want %d", len(listed), len(want))
	}
}

// The live log tail's subscriber count must see a subscriber whatever node
// holds its connection: NUMSUB counts one node only, so a count on a random
// node would stop the agent publishing (#2358).
func TestRedisClusterLogTailSeesEverySubscriber(t *testing.T) {
	addr := os.Getenv("DEXAFLOW_TEST_REDIS_CLUSTER")
	if addr == "" {
		t.Skip("DEXAFLOW_TEST_REDIS_CLUSTER is not set; start one with scripts/redis-cluster-up.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rd, err := storage.NewRedis(ctx, config.RedisSection{Cluster: config.RedisClusterSection{Addrs: []string{addr}}})
	if err != nil {
		t.Fatalf("NewRedis as a Cluster: %v", err)
	}
	defer rd.Close()
	tailer := logs.NewRedisTailer(rd.Client)
	for i := 0; i < 12; i++ {
		ref := logs.Ref{TenantID: "t", DagID: "dag", RunID: fmt.Sprintf("run-%d", i), TaskID: "task", TryNumber: 1}
		if has, err := tailer.HasSubscribers(ctx, ref); err != nil || has {
			t.Fatalf("ref %d before subscribing: has %v, err %v", i, has, err)
		}
		lines, stop := tailer.Subscribe(ctx, ref)
		deadline := time.Now().Add(5 * time.Second)
		for {
			has, err := tailer.HasSubscribers(ctx, ref)
			if err != nil {
				t.Fatalf("ref %d: %v", i, err)
			}
			if has {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("ref %d: a subscriber is listening and HasSubscribers says no one is", i)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err := tailer.Publish(ctx, ref, "line"); err != nil {
			t.Fatalf("publish: %v", err)
		}
		select {
		case got := <-lines:
			if got != "line" {
				t.Errorf("ref %d: got %q", i, got)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("ref %d: the published line did not arrive", i)
		}
		stop()
	}
}
