package scheduler

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

func backoffRun(try int, delay int, mult float64, maxDelay int) RunState {
	run := RunState{
		DagID:             "etl",
		LogicalDate:       "2026-11-01T00:00:00Z",
		Tries:             map[string]int{"a": try},
		RetryDelaySeconds: map[string]int{"a": delay},
		RetryBackoff:      map[string]float64{"a": mult},
	}
	if maxDelay > 0 {
		run.MaxRetryDelaySeconds = map[string]int{"a": maxDelay}
	}
	return run
}

// TestRetryDelayGrowsWithTheMultiplier pins Airflow 3's formula (#2454): the
// floor is retry_delay * multiplier^(try-1), and the jitter keeps the delay in
// [floor, 2*floor).
func TestRetryDelayGrowsWithTheMultiplier(t *testing.T) {
	for _, tc := range []struct {
		try   int
		mult  float64
		floor int64
	}{
		{1, 2, 60}, {2, 2, 120}, {3, 2, 240}, {4, 2, 480},
		{1, 5, 60}, {2, 5, 300}, {3, 5, 1500},
		{2, 1.5, 90}, {3, 0.5, 15},
	} {
		got := RetryDelay(backoffRun(tc.try, 60, tc.mult, 0), "a")
		lo, hi := time.Duration(tc.floor)*time.Second, time.Duration(2*tc.floor)*time.Second
		if got < lo || got >= hi {
			t.Errorf("try %d multiplier %v: delay %v, want in [%v, %v)", tc.try, tc.mult, got, lo, hi)
		}
	}
}

// TestRetryDelayWithoutAMultiplierIsConstant: 0, 1 and no entry all keep
// retry_delay_seconds, and max_retry_delay does not shorten it, as in Airflow.
func TestRetryDelayWithoutAMultiplierIsConstant(t *testing.T) {
	for _, m := range []float64{0, 1, -2} {
		if got := RetryDelay(backoffRun(4, 60, m, 10), "a"); got != time.Minute {
			t.Errorf("multiplier %v: delay %v, want 1m", m, got)
		}
	}
	run := RunState{Tries: map[string]int{"a": 3}, RetryDelaySeconds: map[string]int{"a": 45}}
	if got := RetryDelay(run, "a"); got != 45*time.Second {
		t.Errorf("no backoff entry: delay %v, want 45s", got)
	}
}

// TestRetryDelayIsCapped: max_retry_delay caps a growing delay, and a huge
// multiplier or try count stops at MaxRetryDelay instead of overflowing.
func TestRetryDelayIsCapped(t *testing.T) {
	if got := RetryDelay(backoffRun(6, 60, 2, 600), "a"); got != 10*time.Minute {
		t.Errorf("capped by max_retry_delay: delay %v, want 10m", got)
	}
	for _, tc := range []struct {
		try  int
		mult float64
	}{{40, 2}, {2000, 10}, {3, 1e308}} {
		if got := RetryDelay(backoffRun(tc.try, 60, tc.mult, 0), "a"); got != MaxRetryDelay {
			t.Errorf("try %d multiplier %v: delay %v, want %v", tc.try, tc.mult, got, MaxRetryDelay)
		}
	}
}

// TestRetryDelayZeroBaseWaitsAtLeastASecond: Airflow raises a zero floor to
// one second, so a backoff with retry_delay 0 still spreads retries out.
func TestRetryDelayZeroBaseWaitsAtLeastASecond(t *testing.T) {
	got := RetryDelay(backoffRun(3, 0, 2, 0), "a")
	if got < time.Second || got >= 2*time.Second {
		t.Errorf("delay %v, want in [1s, 2s)", got)
	}
}

// TestRetryJitterIsStablePerTry: every tick computes the same retry time for a
// try, so a task is not held and released on alternate ticks; another run or
// try gets its own jitter.
func TestRetryJitterIsStablePerTry(t *testing.T) {
	run := backoffRun(3, 600, 2, 0)
	first := RetryDelay(run, "a")
	for range 5 {
		if got := RetryDelay(run, "a"); got != first {
			t.Fatalf("delay changed between calls: %v then %v", first, got)
		}
	}
	seen := map[time.Duration]bool{}
	for d := range 20 {
		r := backoffRun(3, 600, 2, 0)
		r.LogicalDate = time.Date(2026, 11, 1+d, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
		seen[RetryDelay(r, "a")] = true
	}
	if len(seen) < 10 {
		t.Errorf("20 runs gave %d distinct delays; want the jitter to spread them", len(seen))
	}
}

// TestPlanRunHoldsUpForRetryForTheBackoff: the planner waits out the grown
// delay, not the base one (#2454).
func TestPlanRunHoldsUpForRetryForTheBackoff(t *testing.T) {
	endedAt := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		DagID:             "etl",
		LogicalDate:       "2026-05-31T00:00:00Z",
		Tasks:             linear(),
		States:            map[string]domain.TaskState{"a": domain.TaskStateUpForRetry, "b": domain.TaskStateNone},
		Tries:             map[string]int{"a": 3},
		MaxTries:          map[string]int{"a": 5},
		EndedAt:           map[string]*time.Time{"a": &endedAt},
		RetryDelaySeconds: map[string]int{"a": 60},
		RetryBackoff:      map[string]float64{"a": 2},
	}
	delay := RetryDelay(run, "a")
	run.Now = endedAt.Add(delay - time.Second)
	if _, ok := planMap(run)["a"]; ok {
		t.Errorf("a released 1s before its backoff of %v", delay)
	}
	run.Now = endedAt.Add(delay)
	if got := planMap(run)["a"]; got != domain.TaskStateNone {
		t.Errorf("a = %q once its backoff of %v elapsed; want none", got, delay)
	}
}
