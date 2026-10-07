package agentrpc

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/metadata"

	"github.com/dexadata/dexaflow/internal/xcom"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// BenchmarkFetchXComRedis compares an attempt reading eight upstream values
// value by value against one FetchXComBatch, server side against a real Redis
// (LEOFLOW_TEST_REDIS_URL or a local default; skipped when none answers).
// Each per-value fetch is also one agent to control plane round trip, which
// this benchmark does not count.
func BenchmarkFetchXComRedis(b *testing.B) {
	url := os.Getenv("LEOFLOW_TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		b.Fatal(err)
	}
	client := redis.NewClient(opts)
	b.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if perr := client.Ping(ctx).Err(); perr != nil {
		b.Skipf("redis unavailable: %v", perr)
	}
	svc := xcom.NewService(xcom.NewRedisBackend(client), noIndex{}, time.Hour)
	store := &fakeStore{spec: TaskSpec{XComInputMapping: map[string][]string{}}}
	req := &agentv1.FetchXComBatchRequest{}
	id := testIdentity()
	for i := 0; i < 8; i++ {
		task := "bench" + string(rune('a'+i))
		store.spec.XComInputMapping[task] = []string{task}
		if perr := svc.Push(ctx, xcomKey(id, task, ""), []byte(`{"rows":12345,"path":"s3://bucket/key"}`), "", nil); perr != nil {
			b.Fatal(perr)
		}
		req.Items = append(req.Items, &agentv1.FetchXComRequest{UpstreamTaskId: task})
	}
	srv, a := newServerX(store, svc)
	token, err := a.IssueAgentToken(id, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	actx := metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))

	b.Run("per-value", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, it := range req.Items {
				if _, ferr := srv.FetchXCom(actx, it); ferr != nil {
					b.Fatal(ferr)
				}
			}
		}
	})
	b.Run("batch", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ferr := srv.FetchXComBatch(actx, req); ferr != nil {
				b.Fatal(ferr)
			}
		}
	})
}

type noIndex struct{}

func (noIndex) RecordXCom(context.Context, xcom.IndexEntry) error { return nil }
