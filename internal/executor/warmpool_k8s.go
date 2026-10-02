package executor

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// WarmPodSpecFunc mints the per-pod bootstrap credential and fills the transport /
// connection / security fields for a warm worker of the target, returning a spec
// ready for BuildWarmPod. It is the auth- and config-aware half of warm-pod
// creation, injected from main.go so the executor package imports neither auth nor
// config. It is called once per warm worker the reconciler needs to create.
type WarmPodSpecFunc func(t WarmTarget) (WarmPodSpec, error)

// KubernetesWarmPods is the production WarmPodClient: it lists warm pods by the
// warm-worker label, builds new ones via the injected spec func + BuildWarmPod,
// and deletes them, all in one namespace. It owns the label selector so the
// executor's warm-worker label contract stays private to this package.
type KubernetesWarmPods struct {
	clientset kubernetes.Interface
	namespace string
	newSpec   WarmPodSpecFunc
}

// NewKubernetesWarmPods builds the cluster-backed warm-pod client. newSpec is the
// auth/config-aware builder invoked per create; List and Delete do not need it.
func NewKubernetesWarmPods(cs kubernetes.Interface, namespace string, newSpec WarmPodSpecFunc) *KubernetesWarmPods {
	if namespace == "" {
		namespace = "default"
	}
	return &KubernetesWarmPods{clientset: cs, namespace: namespace, newSpec: newSpec}
}

// warmAnchorSelector selects exactly the warm-pool GC anchors (ADR 0058 D11),
// the label EnsureWarmAnchor stamps.
const warmAnchorSelector = warmAnchorLabelKey + "=" + warmAnchorLabelVal

// ListWarmAnchors returns the dag_version (its label) of every warm-pool GC
// anchor in the namespace (#1500). An anchor without the label reads "" and is
// skipped by the reconciler, never deleted on a guess.
func (k *KubernetesWarmPods) ListWarmAnchors(ctx context.Context) ([]string, error) {
	list, err := k.clientset.CoreV1().ConfigMaps(k.namespace).List(ctx, metav1.ListOptions{LabelSelector: warmAnchorSelector})
	if err != nil {
		return nil, fmt.Errorf("listing warm anchors: %w", err)
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Labels[warmDagVersionLabelKey])
	}
	return out, nil
}

// warmPodSelector selects exactly the warm-worker pods (excludes ordinary task
// pods), the label BuildWarmPod stamps.
const warmPodSelector = warmWorkerLabelKey + "=" + warmWorkerLabelVal

// ListWarmPods returns every warm-worker pod in the namespace, tagged with the
// dag_version it serves (from its label).
func (k *KubernetesWarmPods) ListWarmPods(ctx context.Context) ([]WarmPodInfo, error) {
	list, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{LabelSelector: warmPodSelector})
	if err != nil {
		return nil, fmt.Errorf("listing warm pods: %w", err)
	}
	out := make([]WarmPodInfo, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, warmPodInfoOf(&list.Items[i]))
	}
	return out, nil
}

// warmPodInfoOf maps a warm pod to the reconciler's view of it, shared by the
// live LIST and the WarmPodInformer cache so both read the fleet identically.
func warmPodInfoOf(p *corev1.Pod) WarmPodInfo {
	return WarmPodInfo{
		Name:         p.Name,
		DagVersionID: p.Labels[warmDagVersionLabelKey],
		// Warm pods are RestartPolicy:Never; a Succeeded/Failed pod is a dead
		// worker that can never serve again. Flag it so the reconciler neither
		// counts it toward the target nor leaves it to leak.
		Terminal: podTerminal(p),
		// Tenant attribution for the per-tenant aggregate cap (M4). A pre-label
		// pod (rolling upgrade) has no tenant label and reads "" here; the
		// reconciler attributes it via its version when resolvable and never
		// deletes it for the cap.
		TenantID: p.Labels[warmTenantLabelKey],
	}
}

// CreateWarmPod mints the target's warm-pod spec, builds the pod, and creates it.
// anchorName/anchorUID identify the version's GC-anchor ConfigMap (ADR 0058 D11);
// when non-empty they are threaded onto the spec so BuildWarmPod stamps the pod's
// ownerReference to the anchor. The reconciler ensures the anchor and reads its
// UID before any create, so both are populated on the live path; a caller that
// passes them empty gets a bare pod, unchanged.
func (k *KubernetesWarmPods) CreateWarmPod(ctx context.Context, t WarmTarget, anchorName, anchorUID string) (string, error) {
	if k.newSpec == nil {
		return "", fmt.Errorf("warm pod creation requires a spec builder")
	}
	spec, err := k.newSpec(t)
	if err != nil {
		return "", fmt.Errorf("building warm pod spec for dag_version %s: %w", t.DagVersionID, err)
	}
	spec.Namespace = k.namespace
	spec.AnchorName = anchorName
	spec.AnchorUID = types.UID(anchorUID)
	pod := BuildWarmPod(spec)
	created, err := k.clientset.CoreV1().Pods(k.namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating warm pod for dag_version %s: %w", t.DagVersionID, err)
	}
	return created.Name, nil
}

// DeleteWarmPod removes one warm worker by name.
func (k *KubernetesWarmPods) DeleteWarmPod(ctx context.Context, name string) error {
	if err := k.clientset.CoreV1().Pods(k.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("deleting warm pod %s: %w", name, err)
	}
	return nil
}

// EnsureWarmAnchor ensures the per-dag-version GC-anchor ConfigMap exists and
// returns its UID (ADR 0058 D11). The anchor owns the version's warm pods via an
// ownerReference, so on control-plane loss / namespace teardown the pods are
// cascade-GC'd — the orphan class the reconciler-as-deleter cannot cover. It is
// create-then-read and idempotent: an AlreadyExists (a prior tick, or another
// leader) is success, and the UID is read back with a GET so every pod created
// this tick is stamped with the SAME owner UID. The anchor carries no data (empty
// ConfigMap); the labels only make it discoverable.
func (k *KubernetesWarmPods) EnsureWarmAnchor(ctx context.Context, dagVersionID string) (string, error) {
	name := warmAnchorName(dagVersionID)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: k.namespace,
			Labels: map[string]string{
				warmAnchorLabelKey:     warmAnchorLabelVal,
				warmDagVersionLabelKey: sanitizeLabel(dagVersionID),
			},
		},
	}
	created, err := k.clientset.CoreV1().ConfigMaps(k.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err == nil {
		return string(created.UID), nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating warm anchor for dag_version %s: %w", dagVersionID, err)
	}
	// Already exists (idempotent): read the live anchor to recover its UID.
	got, gerr := k.clientset.CoreV1().ConfigMaps(k.namespace).Get(ctx, name, metav1.GetOptions{})
	if gerr != nil {
		return "", fmt.Errorf("reading existing warm anchor for dag_version %s: %w", dagVersionID, gerr)
	}
	return string(got.UID), nil
}

// DeleteWarmAnchor deletes the per-dag-version GC-anchor ConfigMap (ADR 0058 D11),
// tolerating NotFound (already gone / never created). The reconciler calls this
// ONLY for a fully-drained inactive version (zero live pods), so the cascade the
// ownerReference sets up is a no-op — it can never kill a live warm attempt.
func (k *KubernetesWarmPods) DeleteWarmAnchor(ctx context.Context, dagVersionID string) error {
	name := warmAnchorName(dagVersionID)
	if err := k.clientset.CoreV1().ConfigMaps(k.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting warm anchor for dag_version %s: %w", dagVersionID, err)
	}
	return nil
}

var _ WarmPodClient = (*KubernetesWarmPods)(nil)
