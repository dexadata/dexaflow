package domain

import "time"

// XComEntryMeta is the metadata for one stored XCom value (without the value
// payload) — the source for a task instance's XCom list. Dexaflow XComs are
// unmapped, so MapIndex is -1.
type XComEntryMeta struct {
	Key       string
	Timestamp time.Time
	MapIndex  int
}
