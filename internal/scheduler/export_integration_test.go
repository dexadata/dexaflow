//go:build integration

package scheduler

// TryLeadership and WatchLeadership let the external integration tests drive
// the campaign attempt and the leadership watchdog against a real session.
// Those tests open the leader pool through internal/storage, which imports
// this package, so they cannot live in it.
var (
	TryLeadership   = tryLeadership
	WatchLeadership = watchLeadership
)
