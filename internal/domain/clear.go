package domain

// ClearOptions carries the two independent decisions a clear makes about the run
// it belongs to.
//
// ResetDagRun re-opens the run so the scheduler looks at it again. Without it a
// terminal run stays terminal, and the cleared task instance sits at `none` in a
// run nothing schedules.
//
// RunOnLatestVersion decides WHICH version the re-run executes: the DAG's current
// one, or the one the run was pinned to when it was created.
//
// These were a single boolean. Folding the version decision into the re-open
// meant there was no way to re-run a task against the image that produced it —
// "clear last week's task" always executed today's code — and no way to re-open a
// run without also moving it forward a version. Apache Airflow separates them the
// same way, calling the second `run_on_latest_version`.
type ClearOptions struct {
	ResetDagRun        bool
	RunOnLatestVersion bool
}

// ClearVersionKept names a run that a clear asked to move to the DAG's
// current version (RunOnLatestVersion) and that keeps the version it is bound
// to, with the reason (ADR 0086): only a finished run with no queued or
// running task instance moves.
type ClearVersionKept struct {
	RunID  string
	Reason string
}
