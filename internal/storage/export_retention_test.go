package storage

// SetAfterRunLockHook runs fn inside DeleteFinishedRuns right after the expired
// runs are locked, so a test can commit a concurrent clear in that window.
func (s *RetentionStore) SetAfterRunLockHook(fn func()) { s.afterRunLock = fn }

// SetDryRunCap sets how many eligible runs (and audit rows) the dry run counts
// per tenant before it stops and reports a lower bound.
func (s *RetentionStore) SetDryRunCap(n int) { s.dryRunCap = n }
