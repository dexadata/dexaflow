package domain

import "time"

// PageCursor is a keyset position in a list ordered newest first: the next
// page holds the rows strictly before (At, Key). Key breaks ties between rows
// with the same At: the run id for dag runs, the entry id for the audit log.
type PageCursor struct {
	At  time.Time
	Key string
}
