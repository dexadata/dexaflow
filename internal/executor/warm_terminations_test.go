package executor

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestWarmTerminationsExpire: a remembered terminal reason is served until
// warmTerminationMemory has passed, then forgotten and removed from the map, so
// the memory stays bounded by the churn of one window (#1835).
func TestWarmTerminationsExpire(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := newWarmTerminations()
	m.now = func() time.Time { return now }
	m.observe(oomKilledWarmPod("oom"))
	m.observe(warmInformerPod("live", "dv-1", corev1.PodRunning))
	if got := m.reason("oom"); got != "OOMKilled" {
		t.Fatalf("reason = %q, want OOMKilled", got)
	}
	if len(m.seen) != 1 {
		t.Fatalf("memory holds %d entries, want only the terminated pod", len(m.seen))
	}

	now = now.Add(warmTerminationMemory - time.Second)
	if got := m.reason("oom"); got != "OOMKilled" {
		t.Errorf("reason just inside the window = %q, want OOMKilled", got)
	}
	now = now.Add(2 * time.Second)
	m.observe(oomKilledWarmPod("newer"))
	if got := m.reason("oom"); got != "" {
		t.Errorf("reason after the window = %q, want empty", got)
	}
	if _, kept := m.seen["oom"]; kept || len(m.seen) != 1 {
		t.Errorf("memory = %v, want the expired entry removed and only newer kept", m.seen)
	}

	var nilMem *warmTerminations
	nilMem.observe(oomKilledWarmPod("x"))
	if got := nilMem.reason("x"); got != "" {
		t.Errorf("nil memory reason = %q, want empty", got)
	}
}

// TestWarmPodInformerRecordsTerminalUpdate: a warm worker turning terminal is
// recorded on the update that carries its final container status, before any
// delete (#1835).
func TestWarmPodInformerRecordsTerminalUpdate(t *testing.T) {
	cs := fake.NewClientset(warmInformerPod("warm-a", "dv-1", corev1.PodRunning))
	k := NewKubernetesWarmPods(cs, "leoflow", nil)
	wi, err := NewWarmPodInformer(cs, "leoflow")
	if err != nil {
		t.Fatalf("NewWarmPodInformer: %v", err)
	}
	wi.RecordTerminationsTo(k)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wi.Start(ctx)
	if !wi.WaitForCacheSync(ctx) {
		t.Fatal("warm pod cache did not sync")
	}
	if _, err := cs.CoreV1().Pods("leoflow").UpdateStatus(ctx, oomKilledWarmPod("warm-a"), metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !waitFor(t, func() bool { return k.LastTerminationReason("warm-a") == "OOMKilled" }) {
		t.Error("a warm worker turning OOMKilled was not recorded")
	}
}
