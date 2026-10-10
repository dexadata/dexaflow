package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestPodInformer_BootSurvivesACacheThatCannotSync is #1083. The pod informer's
// cache warm-up is an optimization: PodInformer.WaitForCacheSync documents that a
// false return "is not fatal", and CachedPodActive gates on HasSynced so consumers
// fall back to live reads until the cache warms. That design is only reachable if
// boot can reach it.
//
// It could not. buildPodInformer passed the PROCESS-lifetime context, and
// cache.WaitForCacheSync returns only on sync or on that context's cancel, which
// happens at shutdown. So a cache that cannot sync held the boot goroutine before
// startAPISide returned, and the HTTP and metrics listeners never bound. On a real
// cluster that is a ServiceAccount without list/watch on pods: the control plane
// serves gRPC, never binds /readyz, fails its liveness probe on the same port, and
// is killed and restarted about every 70s with each cycle recorded as
// `Completed exit=0`: a dependency failure that reports itself as success.
//
// Forbidden is the faithful shape: the reflector retries it forever, so the cache
// never syncs while every call still returns promptly.
func TestPodInformer_BootSurvivesACacheThatCannotSync(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "pods"}, "", errors.New("RBAC: no list on pods"))
	})

	// Shorten the production budget so the suite does not pay it. The assertion is
	// that a deadline EXISTS and is honored. Without one the call never returns,
	// whatever this is set to.
	restore := podInformerSyncTimeout
	podInformerSyncTimeout = 150 * time.Millisecond
	t.Cleanup(func() { podInformerSyncTimeout = restore })

	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))

	cfg := &config.ServerConfig{}
	cfg.Server.Role = config.RoleAll
	cfg.Executor.Type = "kubernetes"
	cfg.Executor.TaskNamespace = "leoflow-tasks"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan struct{})
	go func() {
		defer close(done)
		buildPodInformer(ctx, cfg, cs, log)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("buildPodInformer did not return: boot is blocked behind a cache that cannot sync (#1083)")
	}

	// Diagnosability is the other half. A bounded wait that stays silent leaves the
	// operator with a probe failure and no cause, which is the state #1083 describes.
	got := logBuf.String()
	for _, want := range []string{"leoflow-tasks", "list"} {
		if !strings.Contains(got, want) {
			t.Errorf("the timeout log does not name %q, so it cannot point at the cause:\n%s", want, got)
		}
	}
}

// TestPodInformer_ColdCacheStillWarmsAfterTheBootDeadline is the other half of
// #1083, and the half a bounded wait can silently lose.
//
// The whole safety argument for continuing boot with a cold cache is that the
// cache is only cold for now: the deadline bounds the WAIT, not the informer.
// Start hands the reflector the process context, so it stays in its retry
// backoff, and HasSynced is read live by every consumer, so an operator who
// fixes the RBAC gets a warm read-path without restarting the control plane, and
// the reaper settling gate (which holds on informerSynced) closes on its own.
//
// Bounding the informer instead of the wait would satisfy the boot assertion
// exactly as well and leave the cache cold forever: reapers would fall back to
// the per-running-TI LIST storm the shared informer exists to remove, and the
// settling gate would hold every reaper until its liveness valve forced it open.
// That regression is invisible to a test that only asserts buildPodInformer
// returns, which is why this one asserts recovery.
func TestPodInformer_ColdCacheStillWarmsAfterTheBootDeadline(t *testing.T) {
	var denied atomic.Bool
	denied.Store(true)

	cs := fake.NewClientset()
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		if denied.Load() {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Resource: "pods"}, "", errors.New("RBAC: no list on pods"))
		}
		return false, nil, nil // the RBAC fix landed; the fake serves the LIST
	})

	restore := podInformerSyncTimeout
	podInformerSyncTimeout = 150 * time.Millisecond
	t.Cleanup(func() { podInformerSyncTimeout = restore })

	cfg := &config.ServerConfig{}
	cfg.Server.Role = config.RoleAll
	cfg.Executor.Type = "kubernetes"
	cfg.Executor.TaskNamespace = "leoflow-tasks"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	pi := buildPodInformer(ctx, cfg, cs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if pi == nil {
		t.Fatal("buildPodInformer returned nil on the kubernetes/all path")
	}
	if pi.HasSynced() {
		t.Fatal("the cache reports synced while every LIST is refused")
	}

	// The operator fixes the Role. Nothing restarts, nothing is re-wired.
	denied.Store(false)

	deadline := time.Now().Add(20 * time.Second)
	for !pi.HasSynced() {
		if time.Now().After(deadline) {
			t.Fatal("the cache never warmed after LIST started succeeding: the boot deadline " +
				"stopped the informer instead of only bounding the wait, so the reaper/reconciler " +
				"read-path is cold for the life of the process (#1083)")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
