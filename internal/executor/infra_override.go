package executor

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/dexadata/dexaflow/internal/logs"
)

// InfraOverride describes a durable SUCCESS settled over a reaper's infra
// mark: the mark it overrode (agent_lost, pod_lost or dispatch_lost) and the
// attempt's log location, for the system line that records it.
type InfraOverride struct {
	Mark     string
	TenantID string
	DagID    string
	DagRunID string
	TaskID   string
}

// InfraOverrideReporter is implemented by an OutcomeReporter that can tell a
// SUCCESS settle that changed no row apart, and settle a durable SUCCESS over
// a provisional infra mark of the same attempt (ADR 0052 amendment, part 1).
// The reconciler uses it when the reporter implements it.
type InfraOverrideReporter interface {
	SucceedTaskIfActive(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
	SucceedTaskOverInfraMark(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (InfraOverride, bool, error)
}

// InfraOverrideRecorder counts overrides by the mark they overrode.
type InfraOverrideRecorder interface {
	RecordInfraOverride(mark string)
}

// SetInfraOverrideObservers wires what makes an override visible: a counter
// by mark and the sink the system line is appended to. Either may be nil.
func (r *Reconciler) SetInfraOverrideObservers(rec InfraOverrideRecorder, sink logSink) {
	r.overrideRecorder, r.overrideSink = rec, sink
}

// succeedFromRecord settles a SUCCESS verdict. With a reporter that can
// override, a settle that found nothing active falls back to the override,
// but only for a durable record: a pod that succeeded by phase alone carries
// no statement from the task, and a FAILED record never reaches here.
func (r *Reconciler) succeedFromRecord(ctx context.Context, tiID string, tryNumber, epoch int, fromRecord bool) error {
	o, ok := r.reporter.(InfraOverrideReporter)
	if !ok {
		return r.reporter.SucceedTask(ctx, tiID, tryNumber, epoch)
	}
	settled, err := o.SucceedTaskIfActive(ctx, tiID, tryNumber, epoch)
	if err != nil || settled || !fromRecord {
		return err
	}
	ov, overridden, err := o.SucceedTaskOverInfraMark(ctx, tiID, tryNumber, epoch)
	if err != nil || !overridden {
		return err
	}
	slog.WarnContext(ctx, "outcome recovered from the durable record over an infra mark",
		"task_instance", tiID, "try_number", tryNumber, "attempt_epoch", epoch, "mark", ov.Mark)
	if r.overrideRecorder != nil {
		r.overrideRecorder.RecordInfraOverride(ov.Mark)
	}
	r.writeOverrideMarker(ov, tryNumber, epoch)
	return nil
}

// writeOverrideMarker appends the system line that records an override to the
// attempt's own log. Best effort: the database row and the server log are the
// source of truth.
func (r *Reconciler) writeOverrideMarker(ov InfraOverride, tryNumber, epoch int) {
	if r.overrideSink == nil {
		return
	}
	ref := logs.Ref{
		TenantID: ov.TenantID, DagID: ov.DagID, RunID: ov.DagRunID, TaskID: ov.TaskID,
		TryNumber: tryNumber, AttemptEpoch: epoch,
	}
	ev := logs.Event{
		Time:    r.now(),
		Level:   "info",
		Stream:  "system",
		Message: fmt.Sprintf("outcome recovered from the durable record over %s", ov.Mark),
	}
	if err := r.overrideSink.AppendEvent(ref, ev); err != nil {
		slog.Warn("appending the infra override marker", "task_id", ov.TaskID, "error", err)
	}
}
