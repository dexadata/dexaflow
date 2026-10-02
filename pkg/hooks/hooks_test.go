package hooks_test

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/pkg/hooks"
)

func TestNoOpMeter(t *testing.T) {
	// Must implement UsageMeter interface
	var meter hooks.UsageMeter = hooks.NoOpMeter{}
	ctx := context.Background()

	// All methods on NoOpMeter should return nil error
	if err := meter.RecordDagRunCreated(ctx, "tenant", "dag", "run"); err != nil {
		t.Errorf("RecordDagRunCreated: expected nil error, got %v", err)
	}
	if err := meter.RecordTaskRunCompleted(ctx, "tenant", "dag", "run", "task", 100); err != nil {
		t.Errorf("RecordTaskRunCompleted: expected nil error, got %v", err)
	}
	if err := meter.RecordComputeMinutes(ctx, "tenant", 1.5); err != nil {
		t.Errorf("RecordComputeMinutes: expected nil error, got %v", err)
	}
}

func TestDefaultResolver(t *testing.T) {
	// Must implement TenantResolver interface
	var resolver hooks.TenantResolver = hooks.DefaultResolver{}
	
	// Default resolver always returns "default" in OSS installations
	slug, err := resolver.ResolveTenant(context.Background(), "localhost", "fake-jwt")
	if err != nil {
		t.Errorf("ResolveTenant: expected nil error, got %v", err)
	}
	if slug != "default" {
		t.Errorf("ResolveTenant: expected 'default', got %q", slug)
	}
}

func TestNoOpSink(t *testing.T) {
	// Must implement AuditSink interface
	var sink hooks.AuditSink = hooks.NoOpSink{}
	
	err := sink.RecordEvent(context.Background(), hooks.AuditEvent{
		TenantID: "default",
		Action:   "test.action",
	})
	
	if err != nil {
		t.Errorf("RecordEvent: expected nil error, got %v", err)
	}
}

func TestNoOpEnforcer(t *testing.T) {
	// Must implement QuotaEnforcer interface
	var enforcer hooks.QuotaEnforcer = hooks.NoOpEnforcer{}
	ctx := context.Background()

	if err := enforcer.CheckDagRunQuota(ctx, "default"); err != nil {
		t.Errorf("CheckDagRunQuota: expected nil error, got %v", err)
	}
	if err := enforcer.CheckActiveDagQuota(ctx, "default"); err != nil {
		t.Errorf("CheckActiveDagQuota: expected nil error, got %v", err)
	}
	if err := enforcer.CheckUserQuota(ctx, "default"); err != nil {
		t.Errorf("CheckUserQuota: expected nil error, got %v", err)
	}
}
