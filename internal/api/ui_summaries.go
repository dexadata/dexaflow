package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TaskSummaryReader fetches task instances across a set of runs of a DAG, the
// source for the grid's per-cell state summaries.
type TaskSummaryReader interface {
	TaskInstancesForRuns(ctx context.Context, tenant, dagID string, runIDs []string) ([]domain.TaskInstance, error)
}

// lightTISummaryDTO is the Airflow 3.3 LightGridTaskInstanceSummary. child_states
// is null in the MVP (no dynamic task mapping); state is null for un-run tasks.
//
// dag_version_number is the version the cell's run is pinned to. The 3.3 grid
// links a cell to its task instance only when it is set: a null number sends
// the click to the run-less task page, which has no Logs tab. It also drives
// the version-change markers between runs.
type lightTISummaryDTO struct {
	TaskID           string     `json:"task_id"`
	TaskDisplayName  string     `json:"task_display_name"`
	State            *string    `json:"state"`
	ChildStates      *struct{}  `json:"child_states"`
	MinStartDate     *time.Time `json:"min_start_date"`
	MaxEndDate       *time.Time `json:"max_end_date"`
	DagVersionNumber *int       `json:"dag_version_number"`
}

// gridTISummariesDTO is one Airflow 3.3 GridTISummaries, the NDJSON record
// emitted per DAG run.
type gridTISummariesDTO struct {
	DagID         string              `json:"dag_id"`
	RunID         string              `json:"run_id"`
	TaskInstances []lightTISummaryDTO `json:"task_instances"`
}

// taskAgg accumulates a task's summary across its tries: latest-try state, the
// earliest start and the latest end.
type taskAgg struct {
	taskID   string
	tryMax   int
	state    domain.TaskState
	minStart *time.Time
	maxEnd   *time.Time
}

// parseRunIDs reads run_ids from repeated query params and/or comma-separated
// values, preserving order and dropping blanks/duplicates.
func parseRunIDs(c *gin.Context) []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, raw := range c.QueryArray("run_ids") {
		for _, id := range strings.Split(raw, ",") {
			id = strings.TrimSpace(id)
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// aggregateSummaries groups task instances by run then task, keeping the
// latest-try state and the min start / max end across tries. It returns the
// per-run map and the freshness key (latest timestamp, total instance count).
func aggregateSummaries(tis []domain.TaskInstance) (byRun map[string]map[string]*taskAgg, latest time.Time, count int) {
	byRun = make(map[string]map[string]*taskAgg)
	for _, ti := range tis {
		tasks := byRun[ti.RunID]
		if tasks == nil {
			tasks = make(map[string]*taskAgg)
			byRun[ti.RunID] = tasks
		}
		a := tasks[ti.TaskID]
		if a == nil {
			a = &taskAgg{taskID: ti.TaskID, tryMax: -1}
			tasks[ti.TaskID] = a
		}
		if ti.TryNumber >= a.tryMax {
			a.tryMax = ti.TryNumber
			a.state = ti.State
		}
		a.minStart = earliest(a.minStart, ti.StartedAt)
		a.maxEnd = latestOf(a.maxEnd, ti.EndedAt)
		latest = maxTime(latest, ti.StartedAt, ti.EndedAt)
	}
	return byRun, latest, len(tis)
}

func earliest(cur, candidate *time.Time) *time.Time {
	if candidate == nil {
		return cur
	}
	if cur == nil || candidate.Before(*cur) {
		return candidate
	}
	return cur
}

func latestOf(cur, candidate *time.Time) *time.Time {
	if candidate == nil {
		return cur
	}
	if cur == nil || candidate.After(*cur) {
		return candidate
	}
	return cur
}

func maxTime(cur time.Time, candidates ...*time.Time) time.Time {
	for _, c := range candidates {
		if c != nil && c.After(cur) {
			cur = *c
		}
	}
	return cur
}

// stateOrNil renders a task state as a pointer, with the "none" sentinel as JSON
// null (Airflow has no "none" member in TaskInstanceState).
func stateOrNil(s domain.TaskState) *string {
	if s == "" || s == domain.TaskStateNone {
		return nil
	}
	v := string(s)
	return &v
}

// tiSummariesHandler implements GET /ui/grid/ti_summaries/{dag_id}: an NDJSON
// stream (application/x-ndjson), one GridTISummaries object per requested run.
// One DB query backs it; results are grouped in Go. A weak ETag over the latest
// timestamp, the instance count and a fingerprint of every row enables
// conditional GETs. With revalidate (ui.etag_revalidation) the response is
// marked private, no-cache instead of the surface-wide no-store, so a browser
// can actually send that ETag back.
func tiSummariesHandler(reader TaskSummaryReader, runs DagRunRepository, versions DagVersionLister, revalidate bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		runIDs := parseRunIDs(c)
		tis, err := reader.TaskInstancesForRuns(c.Request.Context(), tenantOf(c), dagID, runIDs)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		byRun, latest, count := aggregateSummaries(tis)
		runVersions := runVersionNumbers(c, runs, versions, runIDs, byRun)

		// #289: mark-state PATCH changes a TI's state without moving its
		// started/ended timestamps, so the original `count + latest` ETag
		// was identical before and after the mutation and the SPA's
		// TanStack Query kept serving the cached body. Fold a fingerprint
		// of every row into the ETag so any change invalidates it.
		// The run's version number is folded in too: a clear that re-pins a run
		// to the latest version changes it without touching any row.
		etag := fmt.Sprintf(`W/"%d-%d-%x-%x"`, count, latest.UnixNano(), gridFingerprint(tis), versionFingerprint(runIDs, runVersions))
		c.Header("ETag", etag)
		if revalidate {
			// The browser may keep a private copy but must revalidate it on
			// every use. Revalidation runs the full auth chain, so a revoked
			// credential gets 401 rather than 304, and Vary keys the copy to
			// the credential that fetched it.
			c.Header("Cache-Control", "private, no-cache")
			c.Header("Vary", "Authorization, Cookie")
		}
		c.Header("Content-Type", "application/x-ndjson")
		if match := c.GetHeader("If-None-Match"); match == etag {
			c.Status(http.StatusNotModified)
			return
		}

		enc := json.NewEncoder(c.Writer)
		for _, runID := range runIDs {
			rec := gridTISummariesDTO{DagID: dagID, RunID: runID, TaskInstances: []lightTISummaryDTO{}}
			number := runVersions[runID]
			for _, a := range byRun[runID] {
				rec.TaskInstances = append(rec.TaskInstances, lightTISummaryDTO{
					TaskID:           a.taskID,
					TaskDisplayName:  a.taskID,
					State:            stateOrNil(a.state),
					MinStartDate:     a.minStart,
					MaxEndDate:       a.maxEnd,
					DagVersionNumber: number,
				})
			}
			if err := enc.Encode(rec); err != nil {
				return // client disconnected mid-stream.
			}
		}
	}
}

// RunVersionLabeler reads the version label of many runs of one DAG in one
// query. The storage repository implements it; a DagRunRepository without it
// falls back to one GetDagRun per run.
type RunVersionLabeler interface {
	DagRunVersionLabels(ctx context.Context, tenant, dagID string, runIDs []string) (map[string]string, error)
}

// runVersionNumbers resolves the version number each requested run that has
// task instances is pinned to, by label, falling back to the DAG's newest
// version when the label no longer resolves (as runDagVersion does). It lists
// the DAG's versions once and reads the runs' labels in one query when the
// repository can (the grid polls this stream). A run that cannot be read, or a
// DAG with no versions, maps to nil, which keeps the old null.
func runVersionNumbers(c *gin.Context, runs DagRunRepository, versions DagVersionLister, runIDs []string, byRun map[string]map[string]*taskAgg) map[string]*int {
	out := make(map[string]*int, len(runIDs))
	if runs == nil || versions == nil || len(byRun) == 0 {
		return out
	}
	p := pinnedDagVersions(c, versions)
	if p.newest == nil {
		return out
	}
	wanted := make([]string, 0, len(runIDs))
	for _, runID := range runIDs {
		if len(byRun[runID]) > 0 {
			wanted = append(wanted, runID)
		}
	}
	labels := runLabels(c, runs, p.dagID, wanted)
	for _, runID := range wanted {
		label, found := labels[runID]
		if !found {
			continue
		}
		n := p.newest.VersionNumber
		if v, hit := p.byLabel[label]; hit && label != "" {
			n = v.VersionNumber
		}
		out[runID] = &n
	}
	return out
}

// runLabels returns the version label of each run in runIDs that exists. A
// failed batch read returns none, so every cell keeps the null.
func runLabels(c *gin.Context, runs DagRunRepository, dagID string, runIDs []string) map[string]string {
	if l, isBatch := runs.(RunVersionLabeler); isBatch {
		labels, err := l.DagRunVersionLabels(c.Request.Context(), tenantOf(c), dagID, runIDs)
		if err != nil {
			return nil
		}
		return labels
	}
	labels := make(map[string]string, len(runIDs))
	for _, runID := range runIDs {
		if run, err := runs.GetDagRun(c.Request.Context(), tenantOf(c), dagID, runID); err == nil {
			labels[runID] = run.Version
		}
	}
	return labels
}

// versionFingerprint hashes the version number served for each run, in request
// order, so the ETag changes when a run is re-pinned.
func versionFingerprint(runIDs []string, numbers map[string]*int) uint64 {
	h := uint64(fnvOffset64)
	for _, runID := range runIDs {
		h = fnvString(h, runID)
		if n := numbers[runID]; n != nil {
			h = fnvInt(fnvUint(h, 1), int64(*n))
		} else {
			h = fnvUint(h, 0)
		}
	}
	return h
}

// registerUISummaries mounts the grid ti-summaries stream when a reader is set.
// runs and versions may be nil; the cells then carry no dag_version_number.
func registerUISummaries(r gin.IRouter, reader TaskSummaryReader, runs DagRunRepository, versions DagVersionLister, revalidate bool) {
	if reader == nil {
		return
	}
	r.GET("/ui/grid/ti_summaries/:dag_id", RequirePermission("read", "task_instance"), tiSummariesHandler(reader, runs, versions, revalidate))
}

// FNV-1a 64-bit parameters, inlined so the fingerprint hashes without
// allocating a hash.Hash per row.
const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// gridFingerprint is an order-independent hash of the rows that feed the
// ti_summaries body: the sum of a per-row FNV-1a over run, task, map index, try,
// state and the start and end times. It is O(n) and allocation-free; the sum
// makes it a property of the set of rows, so the database's row order never
// changes it.
func gridFingerprint(tis []domain.TaskInstance) uint64 {
	var sum uint64
	for i := range tis {
		ti := &tis[i]
		h := uint64(fnvOffset64)
		h = fnvString(h, ti.RunID)
		h = fnvString(h, ti.TaskID)
		h = fnvInt(h, int64(ti.MapIndex))
		h = fnvInt(h, int64(ti.TryNumber))
		h = fnvString(h, string(ti.State))
		h = fnvTime(h, ti.StartedAt)
		h = fnvTime(h, ti.EndedAt)
		sum += h
	}
	return sum
}

// fnvString folds s and a terminator into h, so adjacent fields cannot run into
// each other ("ab"+"c" differs from "a"+"bc").
func fnvString(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	h ^= 0xff
	h *= fnvPrime64
	return h
}

// fnvInt folds the two's-complement bytes of v into h.
func fnvInt(h uint64, v int64) uint64 {
	return fnvUint(h, uint64(v)) //nolint:gosec // only the bit pattern is hashed; the sign is irrelevant.
}

// fnvUint folds the eight bytes of v into h.
func fnvUint(h, v uint64) uint64 {
	for range 8 {
		h ^= v & 0xff
		h *= fnvPrime64
		v >>= 8
	}
	return h
}

// fnvTime folds a nullable timestamp into h, keeping nil distinct from the
// zero time.
func fnvTime(h uint64, t *time.Time) uint64 {
	if t == nil {
		return fnvUint(h, 0)
	}
	return fnvInt(fnvUint(h, 1), t.UnixNano())
}
