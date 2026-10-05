package config_test

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// The Kubernetes client limits default to client-go's own (QPS 5, burst 10) on
// one shared client, so an install that sets nothing keeps today's effective
// apiserver budget exactly. A zero maintenance QPS means "share the dispatch
// client".
func TestKubeClientDefaultsKeepClientGoLimits(t *testing.T) {
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	k := c.Executor.KubeClient
	if k.QPS != 5 || k.Burst != 10 {
		t.Errorf("dispatch client limits = qps %v burst %d, want client-go's 5/10", k.QPS, k.Burst)
	}
	if k.MaintenanceQPS != 0 || k.MaintenanceBurst != 0 {
		t.Errorf("maintenance limits = qps %v burst %d, want 0/0 (share the dispatch client)", k.MaintenanceQPS, k.MaintenanceBurst)
	}
}

// Every limit is reachable from the environment under both names, the current
// DEXAFLOW_* and the pre-rename LEOFLOW_*.
func TestKubeClientLimitsBindFromEnv(t *testing.T) {
	t.Setenv("DEXAFLOW_EXECUTOR_KUBE_CLIENT_QPS", "50")
	t.Setenv("DEXAFLOW_EXECUTOR_KUBE_CLIENT_BURST", "100")
	t.Setenv("LEOFLOW_EXECUTOR_KUBE_CLIENT_MAINTENANCE_QPS", "20.5")
	t.Setenv("LEOFLOW_EXECUTOR_KUBE_CLIENT_MAINTENANCE_BURST", "40")
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	k := c.Executor.KubeClient
	if k.QPS != 50 || k.Burst != 100 {
		t.Errorf("DEXAFLOW_EXECUTOR_KUBE_CLIENT_QPS/_BURST did not bind: got qps %v burst %d", k.QPS, k.Burst)
	}
	if k.MaintenanceQPS != 20.5 || k.MaintenanceBurst != 40 {
		t.Errorf("LEOFLOW_EXECUTOR_KUBE_CLIENT_MAINTENANCE_QPS/_BURST did not bind: got qps %v burst %d", k.MaintenanceQPS, k.MaintenanceBurst)
	}
}
