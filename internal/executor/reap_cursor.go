package executor

import "time"

// ReapPageSize is how many candidates a paged reaper list returns per sweep.
// It bounds one sweep's work after a long outage, as the old fixed LIMIT did.
const ReapPageSize = 100

// ReapCursor is a keyset position in a reaper's candidate list: the time the
// list orders by (started_at for pod-lost, queued_at for dispatch-lost) and the
// task instance id that breaks ties. The zero cursor is the start of the list.
//
// A list that always returned the oldest ReapPageSize rows let that many live
// attempts hide a lost one behind them forever (R-07, #1597): every sweep
// deferred the same live rows and never reached the rest. The reapers now keep
// a cursor across sweeps and ask for the page after it.
type ReapCursor struct {
	At time.Time
	ID string
}

// IsZero reports whether the cursor is the start of the list.
func (c ReapCursor) IsZero() bool { return c.At.IsZero() && c.ID == "" }

// after reports whether a row at c comes after the cursor o in (time, id)
// order. Task instance ids are canonical lowercase UUIDs, whose text order is
// their byte order, the order Postgres sorts uuid in.
func (c ReapCursor) after(o ReapCursor) bool {
	if !c.At.Equal(o.At) {
		return c.At.After(o.At)
	}
	return c.ID > o.ID
}

// reapPager keeps one reaper's cursor across sweeps. It is used from the
// reaper's own sweep only, which never runs concurrently with itself.
type reapPager struct{ cur ReapCursor }

// after is the cursor to list from this sweep.
func (p *reapPager) after() ReapCursor { return p.cur }

// advance records the page a sweep listed: n rows, the last at last. A full
// page means more rows may follow, so the next sweep continues after it; a
// short page is the end of the list, so the next sweep starts over.
func (p *reapPager) advance(n int, last ReapCursor) {
	if n < ReapPageSize {
		p.cur = ReapCursor{}
		return
	}
	p.cur = last
}

// cursor is the candidate's keyset position in the running list.
func (c PodLostCandidate) cursor() ReapCursor {
	return ReapCursor{At: c.RunningSince, ID: c.TaskInstanceID}
}

// cursor is the candidate's keyset position in the queued list.
func (c StaleQueuedCandidate) cursor() ReapCursor {
	return ReapCursor{At: c.QueuedAt, ID: c.TaskInstanceID}
}

// advancePager records a listed page of candidates on the pager.
func advancePager[T interface{ cursor() ReapCursor }](p *reapPager, page []T) {
	var last ReapCursor
	if len(page) > 0 {
		last = page[len(page)-1].cursor()
	}
	p.advance(len(page), last)
}
