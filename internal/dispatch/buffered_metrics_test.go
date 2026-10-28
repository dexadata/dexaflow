package dispatch_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
)

// metricsSpy records the dispatch-pool signals the BufferedDispatcher emits.
type metricsSpy struct {
	mu        sync.Mutex
	depths    []int
	latencies []float64
}

func (m *metricsSpy) RecordDispatchQueueDepth(depth int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.depths = append(m.depths, depth)
}
func (m *metricsSpy) RecordDispatchAtCapacity() {}
func (m *metricsSpy) RecordDispatchLatencySeconds(s float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latencies = append(m.latencies, s)
}
func (m *metricsSpy) RecordDispatchInnerError() {}

func (m *metricsSpy) snapshot() (depths []int, latencies []float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int(nil), m.depths...), append([]float64(nil), m.latencies...)
}

// Each buffered dispatch observes dexaflow_dispatch_latency_seconds, from
// enqueue to the worker's inner-dispatcher return, and the queue depth gauge
// follows the queue down as workers drain it (#1186). Before, the latency
// histogram was never observed and the depth gauge was only set on enqueue, so
// it kept the last high value after the queue emptied.
func TestBufferedDispatchRecordsLatencyAndDrainedDepth(t *testing.T) {
	inner := &recordingInner{delay: 20 * time.Millisecond}
	spy := &metricsSpy{}
	d := dispatch.NewBuffered(inner, &recordingSink{}, discardLogger(), spy, dispatch.BufferConfig{BufferSize: 4, Workers: 1})
	for _, id := range []string{"a", "b", "c"} {
		if _, err := d.Dispatch(context.Background(), "r1", "etl", "", domain.TaskSpec{TaskID: id}); err != nil {
			t.Fatalf("Dispatch %s: %v", id, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	depths, latencies := spy.snapshot()
	if len(latencies) != 3 {
		t.Fatalf("latency samples = %d, want one per dispatch (3)", len(latencies))
	}
	for _, l := range latencies {
		if l < 0.02 {
			t.Errorf("latency %v s is shorter than the inner dispatch itself (20 ms)", l)
		}
	}
	if len(depths) == 0 || depths[len(depths)-1] != 0 {
		t.Errorf("the depth gauge must end at 0 once the queue drained, got %v", depths)
	}
}
