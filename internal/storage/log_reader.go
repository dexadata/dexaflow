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

// ReadLogs resolves the run reference (tenant name -> id, run_id -> dag_run id),
// then opens the stored log for the task attempt. It returns domain.ErrNotFound
// when the run or its log file is absent. See issue #21 for the resolution cost.
func (r *LogReader) ReadLogs(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (io.ReadCloser, error) {
	ref, err := r.q.ResolveRunRef(ctx, queries.ResolveRunRefParams{Name: tenant, DagID: dagID, RunID: runID})
	if err != nil {
		return nil, mapNotFound(err)
	}
	rc, err := r.sink.Read(logs.Ref{
		TenantID:  uuidToString(ref.TenantID),
		DagID:     dagID,
		RunID:     uuidToString(ref.DagRunID),
		TaskID:    taskID,
		TryNumber: tryNumber,
	})
	if err != nil {
		return nil, classifyLogReadError(err)
	}
	return rc, nil
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
