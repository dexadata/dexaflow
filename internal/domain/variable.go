package domain

// Variable is a tenant-scoped key/value setting consumed by DAGs and managed
// from the Admin UI. Value is stored as-is (plaintext for the MVP); the API
// masks values of secret-ish keys.
type Variable struct {
	Key         string
	Value       string
	Description string
	// Encrypted reports that the value is stored sealed with the secret key
	// (#507). It is read-only: writes follow the server's variable_encryption.
	Encrypted bool
	// Unreadable reports a sealed value that no configured secret key opens
	// (the key was rotated without keeping the old one, for example). Value is
	// empty then. Only listings set it; reading the variable alone is an
	// ErrValueUnreadable error.
	Unreadable bool
}

// VariablePatch is a tri-state write to a Variable (#887). Description mirrors
// domain.ConnectionPatch: nil preserves the stored value (COALESCE), non-nil ""
// clears, and a value sets. Value is subtly different because the `value` column
// is NOT NULL: the caller resolves an omitted or masked ("***" for a sensitive
// key) value to the stored value BEFORE building the patch, so Value is expected
// non-nil here — a non-nil "" still clears. Key is always written.
type VariablePatch struct {
	Key         string
	Value       *string
	Description *string
}
