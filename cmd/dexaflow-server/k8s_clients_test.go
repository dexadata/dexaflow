package main

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

func limiterOf(t *testing.T, cs kubernetes.Interface) flowcontrol.RateLimiter {
	t.Helper()
	rl := cs.CoreV1().RESTClient().GetRateLimiter()
	if rl == nil {
		t.Fatal("client has no rate limiter")
	}
	return rl
}

func testBaseConfig() *rest.Config {
	return &rest.Config{Host: "https://127.0.0.1:6443", Timeout: k8sClientTimeout}
}

// With no maintenance limits set, dispatch and maintenance share ONE client and
// therefore one rate limiter, exactly as before the split: the effective
// apiserver budget of an unconfigured install does not change.
func TestNewK8sClients_DefaultSharesOneLimiter(t *testing.T) {
	clients, err := newK8sClients(testBaseConfig(), config.KubeClientSection{QPS: 5, Burst: 10})
	if err != nil {
		t.Fatalf("newK8sClients: %v", err)
	}
	if clients.dispatch != clients.maintenance {
		t.Fatal("with maintenance limits unset the maintenance client must be the dispatch client")
	}
	if got := limiterOf(t, clients.dispatch).QPS(); got != 5 {
		t.Errorf("dispatch QPS = %v, want 5", got)
	}
}

// With maintenance limits set, GC, reconciler and reapers get their own client
// and their own token bucket, so a maintenance burst cannot drain the budget pod
// creation depends on.
func TestNewK8sClients_SplitGivesSeparateLimiters(t *testing.T) {
	clients, err := newK8sClients(testBaseConfig(), config.KubeClientSection{
		QPS: 50, Burst: 100, MaintenanceQPS: 20, MaintenanceBurst: 40,
	})
	if err != nil {
		t.Fatalf("newK8sClients: %v", err)
	}
	if clients.dispatch == clients.maintenance {
		t.Fatal("maintenance limits set but the maintenance client is the dispatch client")
	}
	d, m := limiterOf(t, clients.dispatch), limiterOf(t, clients.maintenance)
	if d == m {
		t.Fatal("dispatch and maintenance clients share a rate limiter")
	}
	if d.QPS() != 50 {
		t.Errorf("dispatch QPS = %v, want 50", d.QPS())
	}
	if m.QPS() != 20 {
		t.Errorf("maintenance QPS = %v, want 20", m.QPS())
	}
}

// A non-positive dispatch QPS or burst falls back to client-go's defaults rather
// than building an unthrottled client or one that can never send a request.
func TestK8sClientConfig_NonPositiveFallsBackToClientGoDefaults(t *testing.T) {
	base := testBaseConfig()
	got := k8sClientConfig(base, 0, -1)
	if got.QPS != rest.DefaultQPS || got.Burst != rest.DefaultBurst {
		t.Errorf("fallback limits = qps %v burst %d, want %v/%d", got.QPS, got.Burst, rest.DefaultQPS, rest.DefaultBurst)
	}
	if got.Timeout != 10*time.Second {
		t.Errorf("per-call timeout lost: %v", got.Timeout)
	}
	if base.QPS != 0 {
		t.Error("k8sClientConfig must not mutate the base config")
	}
}

// The informers watch through a client without the per-call timeout, which
// would cut every watch after 10s (#1621), on the maintenance limits.
func TestNewK8sClients_InformerClientHasNoCallTimeout(t *testing.T) {
	for _, limits := range []config.KubeClientSection{
		{QPS: 5, Burst: 10},
		{QPS: 50, Burst: 100, MaintenanceQPS: 20, MaintenanceBurst: 40},
	} {
		clients, err := newK8sClients(testBaseConfig(), limits)
		if err != nil {
			t.Fatalf("newK8sClients: %v", err)
		}
		if clients.informers == clients.maintenance || clients.informers == clients.dispatch {
			t.Fatal("the informers share a client with a per-call timeout")
		}
		want := float32(limits.QPS)
		if limits.MaintenanceQPS > 0 {
			want = float32(limits.MaintenanceQPS)
		}
		if got := limiterOf(t, clients.informers).QPS(); got != want {
			t.Errorf("informer QPS = %v, want the maintenance QPS %v", got, want)
		}
	}
	base := testBaseConfig()
	if got := k8sWatchConfig(base, 5, 10); got.Timeout != 0 {
		t.Errorf("watch config timeout = %v, want 0", got.Timeout)
	}
	if base.Timeout != k8sClientTimeout {
		t.Error("k8sWatchConfig must not mutate the base config")
	}
}
