package cli

import (
	"context"
	"time"
)

// saveWatcher is the mtime-polling watch loop of `dexaflow lite`, shared with
// `dexaflow deploy --watch`: it polls the files scan returns and calls onSave
// when one of them appears, vanishes or has its modification time move.
type saveWatcher struct {
	// interval is how often the files are polled.
	interval time.Duration
	// settle, when positive, debounces a burst of saves: onSave runs once
	// no file has moved for settle, with the snapshot from before the first
	// save of the burst. Zero runs onSave on the poll that sees a save, as
	// Lite always has.
	settle time.Duration
	// scan returns the watched files and their modification times now
	// (projectMtimes), or false to skip this poll.
	scan func() (map[string]time.Time, bool)
	// onSave handles a save, given the snapshots before and after it.
	onSave func(before, after map[string]time.Time)
}

// run polls from the snapshot snap until ctx is canceled. A save made while
// onSave runs is seen by the next poll.
func (w saveWatcher) run(ctx context.Context, snap map[string]time.Time) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	// before is the snapshot from before a save not yet handled, nil when
	// there is none; moved is when a file last moved.
	var before map[string]time.Time
	var moved time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cur, ok := w.scan()
		if !ok {
			continue
		}
		if mtimesChanged(snap, cur) {
			if before == nil {
				before = snap
			}
			snap, moved = cur, time.Now()
		}
		if before != nil && time.Since(moved) >= w.settle {
			saved := before
			before = nil
			w.onSave(saved, snap)
		}
	}
}

// changedPaths returns the paths that appeared, vanished or moved between
// two snapshots.
func changedPaths(before, after map[string]time.Time) map[string]bool {
	out := map[string]bool{}
	for p, t := range after {
		if old, ok := before[p]; !ok || !old.Equal(t) {
			out[p] = true
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out[p] = true
		}
	}
	return out
}
