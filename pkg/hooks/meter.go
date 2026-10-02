package hooks

import (
	"context"
)

// UsageMeter records metered events for billing aggregation.
type UsageMeter interface {
	RecordDagRunCreated(ctx context.Context, tenantID, dagID, runID string) error
	RecordTaskRunCompleted(ctx context.Context, tenantID, dagID, runID, taskID string, durationMs int64) error
	RecordComputeMinutes(ctx context.Context, tenantID string, minutes float64) error
}

// NoOpMeter is the default implementation that does nothing.
type NoOpMeter struct{}

func (NoOpMeter) RecordDagRunCreated(ctx context.Context, tenantID, dagID, runID string) error {
	return nil
}

func (NoOpMeter) RecordTaskRunCompleted(ctx context.Context, tenantID, dagID, runID, taskID string, durationMs int64) error {
	return nil
}

func (NoOpMeter) RecordComputeMinutes(ctx context.Context, tenantID string, minutes float64) error {
	return nil
}
