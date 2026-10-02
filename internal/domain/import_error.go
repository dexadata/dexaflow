package domain

import "time"

// ImportError is a DAG parse/compile failure surfaced as Airflow's "Import
// Errors" banner on the home dashboard. It is keyed by Filename; a successful
// re-import of the same file clears it. The `dexaflow lite` watcher writes these
// on a failed compile and removes them on the next good compile.
type ImportError struct {
	// ID is the stable identifier of the error record.
	ID string
	// Filename is the DAG source path that failed to import.
	Filename string
	// StackTrace is the human-readable parse/compile error (traceback).
	StackTrace string
	// BundleName is the originating bundle (empty when unknown).
	BundleName string
	// Timestamp is when the error was recorded.
	Timestamp time.Time
}
