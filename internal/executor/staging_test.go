package executor

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/domain"
)

func TestStagingClaimName(t *testing.T) {
	n := StagingClaimName("ETL Vendas", "manual__2026-05-23T21:00:00Z")
	if n == "" || n[:16] != "leoflow-staging-" {
		t.Fatalf("name = %q, want leoflow-staging- prefix", n)
	}
	// Deterministic: same inputs -> same name (so a re-run re-attaches the PVC).
	if n != StagingClaimName("ETL Vendas", "manual__2026-05-23T21:00:00Z") {
		t.Error("claim name must be deterministic")
	}
	// DNS-safe: lowercase, no spaces/colons.
	for _, c := range n {
		if c == ' ' || c == ':' || (c >= 'A' && c <= 'Z') {
			t.Errorf("name %q has a non-DNS-safe char %q", n, c)
		}
	}
}

func TestEnsureStagingClaimIsIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	req := sampleReq()
	req.StagingClaim = "leoflow-staging-etl-r1"
	req.StagingSize = "5Gi"

	for i := 0; i < 2; i++ { // called per task; must not error on the second
		if err := e.ensureStagingClaim(context.Background(), req); err != nil {
			t.Fatalf("ensure #%d: %v", i, err)
		}
	}
	pvc, err := cs.CoreV1().PersistentVolumeClaims("leoflow").Get(context.Background(), req.StagingClaim, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("PVC not created: %v", err)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Errorf("accessModes = %v, want [ReadWriteMany]", pvc.Spec.AccessModes)
	}
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != "5Gi" {
		t.Errorf("size = %s, want 5Gi", got)
	}
	if pvc.Labels["leoflow.io/run-id"] == "" {
		t.Error("PVC must carry the run-id label for GC")
	}
}

// fakeStagingStore is an in-memory StagingStore for the GC tests.
type fakeStagingStore struct {
	active  []domain.StagingVolumeState
	deleted map[string]string // pvc name -> reason
}

func (f *fakeStagingStore) RecordStagingVolume(_ context.Context, _, _, _, pvcName, _ string) error {
	f.active = append(f.active, domain.StagingVolumeState{PVCName: pvcName, RunState: "running"})
	return nil
}

func (f *fakeStagingStore) MarkStagingDeleted(_ context.Context, pvcName, reason string) error {
	if f.deleted == nil {
		f.deleted = map[string]string{}
	}
	f.deleted[pvcName] = reason
	return nil
}

func (f *fakeStagingStore) ListActiveStagingVolumes(_ context.Context) ([]domain.StagingVolumeState, error) {
	return f.active, nil
}

// GC reclaims from the metadatabase-tracked run state (ADR 0022): a succeeded run
// frees its volume now, a failed run after the TTL from its terminal time, an
// orphan (run row gone) on sight; an active run stays. Each deletion is recorded
// with its reason (run_succeeded | ttl_expired | orphaned).
func TestGCStagingClaims(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)
	now := time.Now()
	store := &fakeStagingStore{active: []domain.StagingVolumeState{
		{PVCName: "s-success", RunState: "success", CreatedAt: now},
		{PVCName: "s-failed-old", RunState: "failed", RunEndedAt: &old, CreatedAt: old},
		{PVCName: "s-failed-new", RunState: "failed", RunEndedAt: &recent, CreatedAt: recent},
		{PVCName: "s-running", RunState: "running", CreatedAt: now},
		{PVCName: "s-orphan-old", RunState: "", CreatedAt: old},   // run gone + older than TTL -> reclaim
		{PVCName: "s-orphan-fresh", RunState: "", CreatedAt: now}, // unresolved but fresh -> KEEP (safety)
	}}
	e.SetStagingStore(store)
	for _, n := range []string{"s-success", "s-failed-old", "s-failed-new", "s-running", "s-orphan-old", "s-orphan-fresh"} {
		_, _ = cs.CoreV1().PersistentVolumeClaims("leoflow").Create(context.Background(),
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: n}}, metav1.CreateOptions{})
	}
	if err := e.GCStagingClaims(context.Background(), 24*time.Hour); err != nil {
		t.Fatalf("GC: %v", err)
	}
	exists := func(n string) bool {
		_, err := cs.CoreV1().PersistentVolumeClaims("leoflow").Get(context.Background(), n, metav1.GetOptions{})
		return err == nil
	}
	for pvc, want := range map[string]string{"s-success": "run_succeeded", "s-failed-old": "ttl_expired", "s-orphan-old": "orphaned"} {
		if exists(pvc) {
			t.Errorf("%s should be deleted", pvc)
		}
		if store.deleted[pvc] != want {
			t.Errorf("%s delete reason = %q, want %q", pvc, store.deleted[pvc], want)
		}
	}
	// Kept — crucially s-orphan-fresh: an unresolved run-state must NEVER delete a
	// freshly-created volume (an active run's pods would be stranded — prod safety).
	for _, pvc := range []string{"s-failed-new", "s-running", "s-orphan-fresh"} {
		if !exists(pvc) {
			t.Errorf("%s should be kept", pvc)
		}
		if _, ok := store.deleted[pvc]; ok {
			t.Errorf("%s should not be marked deleted", pvc)
		}
	}
}

// The staging PVC honors the configured access mode: ReadWriteOnce for single-node
// dev (k3d local-path rejects RWX), ReadWriteMany by default (ADR 0022).
func TestStagingAccessMode(t *testing.T) {
	cases := map[string]corev1.PersistentVolumeAccessMode{
		"":                 corev1.ReadWriteMany, // default = multi-node prod
		"ReadWriteMany":    corev1.ReadWriteMany,
		"ReadWriteOnce":    corev1.ReadWriteOnce, // single-node dev
		"ReadWriteOncePod": corev1.ReadWriteOncePod,
	}
	for mode, want := range cases {
		cs := fake.NewSimpleClientset()
		e := NewKubernetesExecutor(cs, "leoflow")
		req := sampleReq()
		req.StagingClaim = "leoflow-staging-etl-r1"
		req.StagingAccessMode = mode
		if err := e.ensureStagingClaim(context.Background(), req); err != nil {
			t.Fatalf("mode %q: ensure: %v", mode, err)
		}
		pvc, _ := cs.CoreV1().PersistentVolumeClaims("leoflow").Get(context.Background(), req.StagingClaim, metav1.GetOptions{})
		if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != want {
			t.Errorf("mode %q -> %v, want [%v]", mode, pvc.Spec.AccessModes, want)
		}
	}
}

// ensureStagingClaim records the volume as active when a store is wired (ADR 0022).
func TestEnsureStagingClaimRecords(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	store := &fakeStagingStore{}
	e.SetStagingStore(store)
	req := sampleReq()
	req.StagingClaim = "leoflow-staging-etl-r1"
	if err := e.ensureStagingClaim(context.Background(), req); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(store.active) != 1 || store.active[0].PVCName != "leoflow-staging-etl-r1" {
		t.Fatalf("expected the volume recorded as active, got %+v", store.active)
	}
}
