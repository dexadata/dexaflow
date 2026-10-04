package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// LogReader resolves a task attempt's log location from API-facing identifiers
// and reads it from the log sink, and tails its live lines.
type LogReader struct {
	q      *queries.Queries
	sink   logs.Sink
	tailer logs.Tailer
}

// NewLogReader builds a LogReader over the given Postgres connection, sink, and
// live-tail tailer (tailer may be nil to disable following).
func NewLogReader(pg *Postgres, sink logs.Sink, tailer logs.Tailer) *LogReader {
	return &LogReader{q: pg.Queries, sink: sink, tailer: tailer}
}

// Tail subscribes to the task attempt's live log lines, returning a line channel
// and a cancel function. It resolves the run reference the same way ReadLogs
// does so the channel matches what the agent publishes.
func (r *LogReader) Tail(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (lines <-chan string, cancel func(), err error) {
	if r.tailer == nil {
		return nil, nil, fmt.Errorf("%w: live tailing not configured", domain.ErrNotFound)
	}
	ref, rerr := r.q.ResolveRunRef(ctx, queries.ResolveRunRefParams{Name: tenant, DagID: dagID, RunID: runID})
	if rerr != nil {
		return nil, nil, mapNotFound(rerr)
	}
	lines, cancel = r.tailer.Subscribe(ctx, logs.Ref{
		TenantID:  uuidToString(ref.TenantID),
		DagID:     dagID,
		RunID:     uuidToString(ref.DagRunID),
		TaskID:    taskID,
		TryNumber: tryNumber,
	})
	return lines, cancel, nil
}

// maxTryLogStreams bounds how many executions' streams one try's log read
// probes and serves. Every execution of a try has its own attempt epoch (ADR
// 0051 amendment), and a reschedule-mode sensor starts a new execution on
// every poke, so a long-running sensor's try can span many epochs. The read
// keeps the epoch-0 stream and the most recent executions up to this bound.
const maxTryLogStreams = 256

// ReadLogs resolves the run reference (tenant name -> id, run_id -> dag_run id),
// then opens the stored log for the task attempt. It returns domain.ErrNotFound
// when the run or its log file is absent. See issue #21 for the resolution cost.
//
// The API addresses a try, and one try can have several executions (an infra
// re-place, a reschedule poke, a repeated dispatch), each storing its stream
// under its own attempt epoch (#863). The epochs the try can span come from
// the database (TryAttemptEpochBounds), and logs.ReadAttempts serves every
// stored one in order. A try that ran once reads exactly as before.
func (r *LogReader) ReadLogs(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (io.ReadCloser, error) {
	ref, err := r.q.ResolveRunRef(ctx, queries.ResolveRunRefParams{Name: tenant, DagID: dagID, RunID: runID})
	if err != nil {
		return nil, mapNotFound(err)
	}
	bounds, err := r.q.TryAttemptEpochBounds(ctx, queries.TryAttemptEpochBoundsParams{
		DagRunID: ref.DagRunID, TaskID: taskID, TryNumber: toInt32(tryNumber),
	})
	if err != nil {
		return nil, fmt.Errorf("resolving the try's attempt epochs: %w", err)
	}
	rc, err := logs.ReadAttempts(r.sink, logs.Ref{
		TenantID:  uuidToString(ref.TenantID),
		DagID:     dagID,
		RunID:     uuidToString(ref.DagRunID),
		TaskID:    taskID,
		TryNumber: tryNumber,
	}, tryEpochs(int(bounds.Low), int(bounds.High)))
	if err != nil {
		return nil, classifyLogReadError(err)
	}
	return rc, nil
}

// tryEpochs is the epochs a try's log read probes: 0, where every log written
// before the epoch existed lives, then (low, high], capped to the most recent
// maxTryLogStreams.
func tryEpochs(low, high int) []int {
	if high-low > maxTryLogStreams {
		low = high - maxTryLogStreams
	}
	out := make([]int, 0, high-low+1)
	out = append(out, 0)
	for e := max(low+1, 1); e <= high; e++ {
		out = append(out, e)
	}
	return out
}

// classifyLogReadError maps a sink read error to the API-facing error. Only a
// genuine absence becomes domain.ErrNotFound (which the API renders as 404 / "no
// logs available"); every other failure — throttling, 5xx, denied or expired
// keyless creds, wrong region, missing bucket — is propagated so the API returns
// 5xx rather than a misleading 200. The disk sink signals absence with
// os.ErrNotExist; the object sink with logs.ErrObjectNotFound.
func classifyLogReadError(err error) error {
	if errors.Is(err, logs.ErrObjectNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, err.Error())
	}
	return fmt.Errorf("reading task log: %w", err)
}
