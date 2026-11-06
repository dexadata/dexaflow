package api

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// The grid's duration axis prints the longest run and half of it through dayjs,
// which keeps the float remainder of a fractional seconds value (#2366). A grid
// run's duration is therefore whole seconds, at least 1 once the run started.
func TestGridDurationIsWholeSeconds(t *testing.T) {
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { e := start.Add(d); return &e }
	cases := []struct {
		name string
		run  domain.DagRun
		want float64
	}{
		{"not started", domain.DagRun{}, 0},
		{"fractional rounds", domain.DagRun{StartedAt: &start, EndedAt: at(8002 * time.Millisecond)}, 8},
		{"half rounds up", domain.DagRun{StartedAt: &start, EndedAt: at(16500 * time.Millisecond)}, 17},
		{"under a second is 1", domain.DagRun{StartedAt: &start, EndedAt: at(300 * time.Millisecond)}, 1},
		{"zero length is 0", domain.DagRun{StartedAt: &start, EndedAt: &start}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toGridRunDTO(c.run).Duration
			if got != c.want {
				t.Fatalf("grid duration = %v, want %v", got, c.want)
			}
			// What the axis formats: the value and its half, as milliseconds,
			// must be integers in float arithmetic.
			for _, v := range []float64{got, got / 2} {
				if ms := v * 1000; ms != float64(int64(ms)) {
					t.Errorf("%v s is %v ms, not a whole number", v, ms)
				}
			}
		})
	}
}

// Other views keep the precise duration.
func TestDagRunLightKeepsThePreciseDuration(t *testing.T) {
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	end := start.Add(8002 * time.Millisecond)
	got := toDagRunLightDTO(domain.DagRun{StartedAt: &start, EndedAt: &end}).Duration
	if got == nil || *got != 8.002 {
		t.Fatalf("light duration = %v, want 8.002", got)
	}
}
