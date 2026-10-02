package executor

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dexadata/dexaflow/internal/domain"
)

// stagingLabel marks PVCs Dexaflow manages for per-run staging, so GC can find
// them without touching anything else in the namespace.
const stagingLabel = "leoflow.io/staging"

// stagingRunIDAnnotation holds the raw (unsanitized) run_id, which GC uses to
// check run state — the run-id label is sanitized and may be lossy.
const stagingRunIDAnnotation = "leoflow.io/run-id"

// defaultStagingSize is used when a DAG enables staging without a size.
const defaultStagingSize = "1Gi"

// stagingAccessMode maps the configured access mode to the PVC enum, defaulting
// to ReadWriteMany (multi-node). Single-node dev uses ReadWriteOnce because the
// k3d local-path provisioner rejects RWX, and a run's sequential same-node pods
// share an RWO volume fine.
func stagingAccessMode(mode string) corev1.PersistentVolumeAccessMode {
	switch mode {
	case "ReadWriteOnce":
		return corev1.ReadWriteOnce
	case "ReadWriteOncePod":
		return corev1.ReadWriteOncePod
	default:
		return corev1.ReadWriteMany
	}
}

// StagingClaimName is the deterministic PVC name for a run's staging volume. It
// must be stable across retries and clear+re-run so the same PVC is re-attached
// (ADR 0022), and DNS-safe.
func StagingClaimName(dagID, runID string) string {
	return fmt.Sprintf("leoflow-staging-%s-%s", sanitizeLabel(dagID), sanitizeLabel(runID))
}

// ensureStagingClaim creates the run's RWX staging PVC if it does not already
// exist (idempotent — called per task). An existing claim is reused, which is
// what makes a clear+re-run keep the upstream tasks' data.
func (e *KubernetesExecutor) ensureStagingClaim(ctx context.Context, req Request) error {
	size := req.StagingSize
	if size == "" {
		size = defaultStagingSize
	}
	qty, err := resource.ParseQuantity(size)
	if err != nil {
		return fmt.Errorf("invalid staging size %q: %w", size, err)
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: req.StagingClaim,
			Labels: map[string]string{
				stagingLabel:           "true",
				"leoflow.io/run-id":    sanitizeLabel(req.RunID),
				"leoflow.io/dag-id":    sanitizeLabel(req.DagID),
				"leoflow.io/tenant-id": sanitizeLabel(req.TenantID),
			},
			// The label is sanitized (run IDs contain label-illegal chars); GC needs
			// the raw run_id to check run state, so keep it as an annotation.
			Annotations: map[string]string{stagingRunIDAnnotation: req.RunID},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{stagingAccessMode(req.StagingAccessMode)},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: qty}},
		},
	}
	if req.StagingStorageClass != "" {
		sc := req.StagingStorageClass
		pvc.Spec.StorageClassName = &sc
	}
	_, err = e.clientset.CoreV1().PersistentVolumeClaims(e.namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating staging PVC %s: %w", req.StagingClaim, err)
	}
	// Track the volume in the metadatabase (idempotent by PVC name) so GC is
	// deterministic and the lifecycle is auditable (ADR 0022). AlreadyExists just
	// means another task of the run provisioned it first; still record.
	if e.staging != nil {
		if rerr := e.staging.RecordStagingVolume(ctx, req.TenantID, req.DagID, req.RunID, req.StagingClaim, size); rerr != nil {
			return fmt.Errorf("recording staging volume %s: %w", req.StagingClaim, rerr)
		}
	}
	return nil
}

// StagingStore persists the per-run staging-volume lifecycle in the metadatabase
// (ADR 0022): provisioning records an active row, GC marks it deleted with a
// reason, and GC reads the active set joined with each run's state. Identified by
// the deterministic PVC name (unique per namespace).
type StagingStore interface {
	RecordStagingVolume(ctx context.Context, tenantID, dagID, runID, pvcName, size string) error
	MarkStagingDeleted(ctx context.Context, pvcName, reason string) error
	ListActiveStagingVolumes(ctx context.Context) ([]domain.StagingVolumeState, error)
}

// GCStagingClaims reclaims per-run staging PVCs from the metadatabase-tracked
// lifecycle (ADR 0022): a successful run frees its volume immediately; a failed
// run keeps it until ttl elapses after the run's terminal time (clear+re-run
// safety); an orphaned volume (run row gone) is reclaimed. Each deletion is
// recorded with its reason. A no-op when no StagingStore is wired.
func (e *KubernetesExecutor) GCStagingClaims(ctx context.Context, ttl time.Duration) error {
	if e.staging == nil {
		return nil
	}
	vols, err := e.staging.ListActiveStagingVolumes(ctx)
	if err != nil {
		return fmt.Errorf("listing active staging volumes: %w", err)
	}
	now := time.Now()
	for _, v := range vols {
		reason, drop := stagingDeleteDecision(v, now, ttl)
		if !drop {
			continue
		}
		if derr := e.clientset.CoreV1().PersistentVolumeClaims(e.namespace).Delete(ctx, v.PVCName, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			return fmt.Errorf("deleting staging PVC %s: %w", v.PVCName, derr)
		}
		if merr := e.staging.MarkStagingDeleted(ctx, v.PVCName, reason); merr != nil {
			return fmt.Errorf("recording staging deletion %s: %w", v.PVCName, merr)
		}
	}
	return nil
}

// stagingDeleteDecision returns the delete reason and whether to reclaim a tracked
// staging volume given its run's state (ADR 0022).
func stagingDeleteDecision(v domain.StagingVolumeState, now time.Time, ttl time.Duration) (reason string, drop bool) {
	switch domain.DagRunState(v.RunState) {
	case domain.DagRunStateSuccess:
		return "run_succeeded", true // success: nothing to re-run, free it now
	case domain.DagRunStateFailed:
		// Keep until ttl after the run's terminal time so a fix-and-clear+re-run
		// still finds the upstream data.
		if v.RunEndedAt == nil || now.Sub(*v.RunEndedAt) > ttl {
			return "ttl_expired", true
		}
		return "", false
	case "":
		// The run did not resolve — usually history was cleared, but it could also
		// be a transient lookup miss for a still-active run. Only reclaim once the
		// volume is older than the TTL, so a miss can never delete an active run's
		// fresh volume (which would strand its pods). This is the prod safety net.
		if now.Sub(v.CreatedAt) > ttl {
			return "orphaned", true
		}
		return "", false
	default:
		return "", false // queued / running / scheduled: still active — keep
	}
}
