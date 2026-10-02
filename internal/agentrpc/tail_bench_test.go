package agentrpc

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dexadata/dexaflow/internal/logs"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// discardWriter is a LogWriter that keeps nothing, so the benchmark measures
// encoding and publishing.
type discardWriter struct{}

func (discardWriter) WriteEvent(logs.Event) error { return nil }
func (discardWriter) WriteLine(string) error      { return nil }
func (discardWriter) Close() error                { return nil }

// benchRedisTailer connects to the Redis named by LEOFLOW_TEST_REDIS_URL (or a
// local default) and skips the benchmark when none answers.
func benchRedisTailer(b *testing.B) *logs.RedisTailer {
	b.Helper()
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
	if perr := client.Ping(context.Background()).Err(); perr != nil {
		b.Skipf("redis unavailable: %v", perr)
	}
	return logs.NewRedisTailer(client)
}

// BenchmarkStreamLogsLineNoTail is the per-line cost of StreamLogs for an
// attempt nobody is tailing, against a real Redis: store the line and offer it
// to the live tail.
func BenchmarkStreamLogsLineNoTail(b *testing.B) {
	tailer := benchRedisTailer(b)
	ref := logs.Ref{TenantID: "t", DagID: "d", RunID: "bench", TaskID: "task", TryNumber: 1}
	gate := newTailGate(tailer, ref, time.Now)
	publish := func(line string) { gate.publish(context.Background(), line) }
	line := &agentv1.LogLine{Time: timestamppb.Now(), Message: "INFO processed batch 42 of 1000 in 12ms", Stream: "stdout"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := writeLine(discardWriter{}, line, publish, nil); err != nil {
			b.Fatal(err)
		}
	}
}
