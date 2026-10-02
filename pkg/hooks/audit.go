package hooks

import (
	"context"
)

// AuditEvent represents a single audit log entry.
type AuditEvent struct {
	TenantID   string
	UserID     string
	Action     string
	Resource   string
	ResourceID string
	Timestamp  int64
	Metadata   map[string]string
}

// AuditSink receives and persists audit events.
type AuditSink interface {
	RecordEvent(ctx context.Context, event AuditEvent) error
}

// NoOpSink discards all events.
type NoOpSink struct{}

func (NoOpSink) RecordEvent(ctx context.Context, event AuditEvent) error {
	return nil
}
