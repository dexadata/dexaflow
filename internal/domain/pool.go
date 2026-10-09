package domain

// Pool is a named, tenant-scoped, cross-DAG task-concurrency budget (Airflow's
// pool). Slots is the cap: a task in the pool is admitted to `queued` only while
// the pool has a free slot, counting queued+running task instances across every
// DAG (ADR 0053 Stage 3). IsDefault marks the implicit default_pool a task with
// no declared pool falls back to. Pools are a Pro-only concept.
type Pool struct {
	Name        string
	Slots       int
	Description string
	IsDefault   bool
}

// DefaultPoolName is the implicit pool a task with no declared pool draws from,
// so the admission gate is always well-defined. Matches Airflow's default_pool.
const DefaultPoolName = "default_pool"

// PoolUsage is a pool's per-state occupancy in slots, feeding the Airflow
// PoolResponse slot fields: each task instance counts its pool_slots, the size
// the admission gate charges it (ADR 0066), not 1. The slots admission actually
// spends are queued+running; scheduled and deferred are reported for the UI but
// do not hold a slot.
type PoolUsage struct {
	Running   int
	Queued    int
	Scheduled int
	Deferred  int
}
