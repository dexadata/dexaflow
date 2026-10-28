package executor

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// keysetPage serves one page of rows ordered by (at, id), strictly after the
// cursor, at most ReapPageSize long: what the store's keyset query returns.
func keysetPage[T any](rows []T, key func(T) ReapCursor, after ReapCursor) []T {
	var out []T
	for _, r := range rows {
		if !after.IsZero() && !key(r).after(after) {
			continue
		}
		out = append(out, r)
		if len(out) == ReapPageSize {
			break
		}
	}
	return out
}

// pagingPodLostStore serves running rows page by page and records each
// cursor the reaper asked with.
type pagingPodLostStore struct {
	rows   []PodLostCandidate
	asked  []ReapCursor
	marked []string
}

func (s *pagingPodLostStore) ListRunningTasks(_ context.Context, _ time.Duration, after ReapCursor) ([]PodLostCandidate, error) {
	s.asked = append(s.asked, after)
	return keysetPage(s.rows, PodLostCandidate.cursor, after), nil
}

func (s *pagingPodLostStore) MarkTaskPodLost(_ context.Context, id string, _, _ int) (bool, error) {
	s.marked = append(s.marked, id)
	return true, nil
}

// TestPodLostReaperPagesPastLiveRows is R-07 (#1597) for the pod-lost reaper:
// 100 older attempts whose pods are alive fill the first page, and a newer
// attempt whose pod vanished sits behind them. Before the fix every sweep
// re-listed the same 100 oldest rows and the lost attempt was never examined;
// now the cursor moves past the live page and the next sweep reaps it, then
// wraps to the start.
func TestPodLostReaperPagesPastLiveRows(t *testing.T) {
	base := time.Now().UTC().Add(-2 * time.Hour)
	pods := &fakePodManager{active: map[string]bool{}}
	store := &pagingPodLostStore{}
	for i := 0; i < ReapPageSize; i++ {
		task := fmt.Sprintf("live%03d", i)
		store.rows = append(store.rows, PodLostCandidate{
			TaskInstanceID: fmt.Sprintf("ti-%03d", i), DagRunID: "run", TaskID: task, TryNumber: 1,
			RunningSince: base.Add(time.Duration(i) * time.Second),
		})
		pods.active["run/"+task+"/1"] = true
	}
	store.rows = append(store.rows, PodLostCandidate{
		TaskInstanceID: "ti-lost", DagRunID: "run", TaskID: "lost", TryNumber: 1,
		RunningSince: base.Add(time.Hour),
	})
	r := newPodLostReaper(store, reapTestLogger(), time.Minute, nil)
	r.pods = pods

	for sweep := 0; sweep < 3; sweep++ {
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("sweep %d: %v", sweep, err)
		}
	}
	if len(store.marked) != 1 || store.marked[0] != "ti-lost" {
		t.Fatalf("the lost attempt behind a full page of live ones must be reaped, marked %v", store.marked)
	}
	last := store.rows[ReapPageSize-1].cursor()
	want := []ReapCursor{{}, last, {}}
	if len(store.asked) != len(want) {
		t.Fatalf("asked %d pages, want %d", len(store.asked), len(want))
	}
	for i := range want {
		if store.asked[i] != want[i] {
			t.Errorf("sweep %d asked after %+v, want %+v", i, store.asked[i], want[i])
		}
	}
}

// pagingQueuedStore is pagingPodLostStore for the dispatch-lost list.
type pagingQueuedStore struct {
	rows       []StaleQueuedCandidate
	asked      []ReapCursor
	thresholds []time.Duration
	marked     []string
}

func (s *pagingQueuedStore) ListStaleQueuedCandidates(_ context.Context, threshold time.Duration, after ReapCursor) ([]StaleQueuedCandidate, error) {
	s.asked = append(s.asked, after)
	s.thresholds = append(s.thresholds, threshold)
	return keysetPage(s.rows, StaleQueuedCandidate.cursor, after), nil
}

func (s *pagingQueuedStore) MarkTaskDispatchLost(_ context.Context, id string, _, _ int) (bool, error) {
	s.marked = append(s.marked, id)
	return true, nil
}

func (s *pagingQueuedStore) MarkTaskStartExpired(context.Context, string, int, int) (bool, error) {
	return false, nil
}

// TestDispatchLostReaperPagesPastPendingRows is R-07 for the dispatch-lost
// reaper: 100 older queued attempts whose pods are Pending (a capacity
// shortage) no longer hide a newer attempt whose create never happened. The
// threshold goes to the store, so rows inside it never take a page slot.
func TestDispatchLostReaperPagesPastPendingRows(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	pods := &fakePodManager{active: map[string]bool{}}
	store := &pagingQueuedStore{}
	for i := 0; i < ReapPageSize; i++ {
		task := fmt.Sprintf("pending%03d", i)
		store.rows = append(store.rows, StaleQueuedCandidate{
			TaskInstanceID: fmt.Sprintf("ti-%03d", i), DagRunID: "run", TaskID: task, TryNumber: 1,
			QueuedAt: base.Add(time.Duration(i) * time.Second),
		})
		pods.active["run/"+task+"/1"] = true
	}
	store.rows = append(store.rows, StaleQueuedCandidate{
		TaskInstanceID: "ti-lost", DagRunID: "run", TaskID: "lost", TryNumber: 1,
		QueuedAt: base.Add(30 * time.Minute),
	})
	const threshold = 3 * time.Minute
	r := newDispatchLostReaper(store, reapTestLogger(), threshold, nil)
	r.pods = pods

	for sweep := 0; sweep < 2; sweep++ {
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("sweep %d: %v", sweep, err)
		}
	}
	if len(store.marked) != 1 || store.marked[0] != "ti-lost" {
		t.Fatalf("the lost dispatch behind a full page of Pending ones must be reaped, marked %v", store.marked)
	}
	for i, th := range store.thresholds {
		if th != threshold {
			t.Errorf("sweep %d passed threshold %v to the store, want %v", i, th, threshold)
		}
	}
}

// TestReapPagerWrapsOnAShortPage: a page shorter than ReapPageSize is the end
// of the list, so the next sweep starts from the beginning again; a full page
// moves the cursor to its last row.
func TestReapPagerWrapsOnAShortPage(t *testing.T) {
	var p reapPager
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	last := ReapCursor{At: at, ID: "b"}
	p.advance(ReapPageSize, last)
	if p.after() != last {
		t.Fatalf("after a full page the cursor = %+v, want %+v", p.after(), last)
	}
	p.advance(ReapPageSize-1, ReapCursor{At: at.Add(time.Second), ID: "c"})
	if !p.after().IsZero() {
		t.Fatalf("after a short page the cursor must wrap, got %+v", p.after())
	}
}

// TestReapCursorOrder: rows are ordered by time, then by id, matching the
// store's ORDER BY.
func TestReapCursorOrder(t *testing.T) {
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	c := ReapCursor{At: at, ID: "m"}
	cases := []struct {
		row  ReapCursor
		want bool
	}{
		{ReapCursor{At: at.Add(time.Microsecond), ID: "a"}, true},
		{ReapCursor{At: at, ID: "n"}, true},
		{ReapCursor{At: at, ID: "m"}, false},
		{ReapCursor{At: at, ID: "l"}, false},
		{ReapCursor{At: at.Add(-time.Microsecond), ID: "z"}, false},
	}
	for _, tc := range cases {
		if got := tc.row.after(c); got != tc.want {
			t.Errorf("%+v after %+v = %v, want %v", tc.row, c, got, tc.want)
		}
	}
}
