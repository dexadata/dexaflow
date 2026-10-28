package domain

import "time"

// PageCursor is a keyset position in a list ordered newest first: the next
// page holds the rows strictly before (At, Key). Key breaks ties between rows
// with the same At: the run id for dag runs, the entry id for the audit log.
// Total is the number of rows the list had when the cursor was issued, so a
// later page reports it instead of counting the whole filtered set again; 0
// means unknown, and the page counts.
type PageCursor struct {
	At    time.Time
	Key   string
	Total int
}
