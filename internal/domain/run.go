package domain

import (
	"encoding/json"
	"time"
)

// DAG is a registered DAG with its scheduling metadata (distinct from DAGSpec,
// which is the compiled artifact).
type DAG struct {
	DagID          string
	Description    string
	Owner          string
	Tags           []string
	Schedule       *string
	ScheduleTZ     string
	StartDate      *time.Time
	IsPaused       bool
	IsActive       bool
	MaxActiveRuns  int
	Catchup        bool
	LastParsedTime *time.Time
	// CurrentVersion is the LABEL of the DAG's current registered version, empty
	// when it cannot be resolved. Paired with DagRun.Version by the UI's clear
	// dialog, which offers "Run with latest bundle version" only when the two
	// differ — see that field.
	CurrentVersion string
}

// DagVersion is a registered version of a DAG. VersionNumber is the 1-based
// ordinal the UI uses (the stored version label is free-form).
type DagVersion struct {
	ID            string
	VersionNumber int
	CreatedAt     time.Time
	// Version is the deployment label that produced this snapshot: by default a
	// git describe (tag/SHA) plus a content hash in production, or
	// "dev-<timestamp>" in Lite. It is the stable
	// per-deployment identifier under a stable dag_id.
	Version string
	// ImageReference is the image the version runs, pinned by digest when the
	// deploy resolved one.
	ImageReference string
	// SpecHash is the canonical hash of the version's dag.json.
	SpecHash string
	// IsCurrent reports whether this is the DAG's current version: the one new
	// runs are created with.
	IsCurrent bool
	// CreatedBy is the email of the user who registered the version, or ""
	// when the register had no user (Lite's dev principal, a direct call).
	CreatedBy string
}

// DagVersionSelector names one registered version of a DAG, by its label
// (Version) or by its version_number (Number). Exactly one is set.
type DagVersionSelector struct {
	Version string
	Number  int
}

// CurrentVersionChange is the outcome of making a version current: the
// version that was current before (From), the one that is current now (To),
// and whether anything changed (false when To was already current).
type CurrentVersionChange struct {
	From    DagVersion
	To      DagVersion
	Changed bool
}

// DagRun is an execution of a DAG, identified by dag_id + run_id.
type DagRun struct {
	DagID       string
	RunID       string
	LogicalDate time.Time
	State       DagRunState
	RunType     string
	QueuedAt    time.Time
	StartedAt   *time.Time
	EndedAt     *time.Time
	Note        string
	// Conf is the run's configuration as a JSON object, supplied at trigger time
	// and exposed to tasks as params. Empty means no configuration; storage
	// persists the empty-object default so downstream readers never see NULL.
	Conf json.RawMessage
	// Version is the LABEL of the dag_version this run is pinned to (the
	// `dag_version` from its dag.json), empty when it cannot be resolved.
	//
	// It exists for the UI: the SPA's clear dialog renders its "Run with latest
	// bundle version" control only when the run's bundle_version differs from the
	// DAG's, and hides it when either is null. With no label the operator cannot
	// ask for the current version at all, whatever the API accepts.
	Version string
}

// TaskInstance is an execution of a task within a DagRun.
type TaskInstance struct {
	DagID     string
	RunID     string
	TaskID    string
	MapIndex  int
	TryNumber int
	MaxTries  int
	State     TaskState
	Operator  string
	// ScheduledAt and QueuedAt record when the instance first entered the
	// scheduled and queued states (Airflow's scheduled_when / queued_when).
	ScheduledAt *time.Time
	QueuedAt    *time.Time
	StartedAt   *time.Time
	EndedAt     *time.Time
	Duration    *float64
	Hostname    string
	// Note is operational context shown in the UI's task panel — e.g. why a task
	// is queued but not running (no executor available).
	Note string
	// Pool is the pool the instance is charged to, "default_pool" when the task
	// declares none, and PoolSlots the slots the admission gate charges it (ADR
	// 0066). Both are the instance's own: a clear re-binds them from the version
	// the re-run executes (#1589).
	Pool      string
	PoolSlots int
	// FailureReason is a short, human-readable cause for a terminal failure,
	// recorded by whichever component observed it: the agent's own report, the
	// reconciler reading the pod (image pull, OOM, exit code), a reaper declaring
	// the pod or agent lost, or the agent's pre-registration classification. It is
	// the answer to "why did this fail?" for an attempt that streamed no logs
	// because its agent never started — the case where the only remaining source
	// of truth used to be `kubectl logs` against the cluster.
	//
	// It is best-effort and often empty: a healthy instance has none, and a cause
	// nobody observed cannot be invented. It carries a classification, never a
	// credential or a raw internal error.
	FailureReason string
}
