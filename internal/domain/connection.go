package domain

// Connection is an Airflow-style connection: credentials/endpoints for operators,
// managed from the Admin UI. Password and Extra are encrypted at rest (ADR 0019);
// Password is write-only and never returned by the API.
type Connection struct {
	ConnID      string
	ConnType    string
	Host        string
	Schema      string
	Login       string
	Password    string
	Port        *int
	Extra       string
	Description string
	// ExtraUnreadable reports a stored extra that no configured secret key
	// opens (the key was rotated without keeping the old one, for example).
	// Extra is empty then. Only listings set it; reading the connection alone
	// is an ErrValueUnreadable error (#1677).
	ExtraUnreadable bool
}

// ConnectionPatch is a tri-state write to a Connection (#887). Each nullable
// field is one of three states the write path must keep distinct:
//   - nil pointer   -> absent: preserve the stored value (the upsert passes NULL
//     and COALESCE keeps the current column).
//   - non-nil ""    -> present and empty: clear the field to empty.
//   - non-nil value -> present: set the field.
//
// A plain string cannot carry "absent" vs "present and empty", which is why the
// safe-merge upsert alone (v0.4.4) could neither clear a field nor stop a
// round-tripped mask from overwriting a secret. ConnType is always written
// (required on every upsert). Secret-mask handling (the mask means "unchanged")
// is resolved by the caller before it builds the patch, so the repository only
// ever sees the three states above.
type ConnectionPatch struct {
	ConnID      string
	ConnType    string
	Host        *string
	Schema      *string
	Login       *string
	Password    *string
	Port        *int
	Extra       *string
	Description *string
}
