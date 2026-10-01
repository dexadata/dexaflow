// Package kubeexchange implements the Kubernetes-backed half of the agent
// token-exchange transport (ADR 0055 Fix #3): validating a pod's projected
// ServiceAccount token via the apiserver TokenReview API, and resolving the
// reviewed pod to the task-instance identity the control plane stamped on it.
// Both satisfy the mockable interfaces in internal/agentrpc, so the exchange
// handler is unit-tested without a cluster; this concrete code is the Pro/K8s
// implementation the owed real-cluster e2e exercises.
package kubeexchange

import (
	"context"
	"errors"
	"fmt"
	"strings"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/executor"
)

// boundTokenPodNameKey / boundTokenPodUIDKey are the apiserver "extra" keys a
// bound (pod-scoped) ServiceAccount token carries in the TokenReview response
// (Kubernetes 1.22+ for BoundServiceAccountTokenVolume). ADR 0055 D7 pins these
// as the resolution keys; they are validated end-to-end by the owed real-cluster
// e2e against the target apiserver version.
const (
	boundTokenPodNameKey = "authentication.kubernetes.io/pod-name"
	boundTokenPodUIDKey  = "authentication.kubernetes.io/pod-uid"
)

// TokenReviewer validates a projected SA token against a fixed audience via the
// apiserver TokenReview API. It implements agentrpc.TokenReviewer.
type TokenReviewer struct {
	client   kubernetes.Interface
	audience string
}

var _ agentrpc.TokenReviewer = (*TokenReviewer)(nil)

// NewTokenReviewer builds a reviewer that asks the apiserver to validate tokens
// for the given control-plane audience. A token not valid for that audience is
// rejected by the apiserver (authenticated=false), so audience separation is
// enforced server-side, not by string comparison here.
func NewTokenReviewer(client kubernetes.Interface, audience string) *TokenReviewer {
	return &TokenReviewer{client: client, audience: audience}
}

// ReviewProjectedToken submits the token to the apiserver's TokenReview API,
// scoped to the control-plane audience, and returns the pod it was bound to. This
// is the single apiserver call in the exchange, made once per pod at bootstrap.
func (r *TokenReviewer) ReviewProjectedToken(ctx context.Context, token string) (agentrpc.ReviewedPod, error) {
	review := &authv1.TokenReview{
		Spec: authv1.TokenReviewSpec{Token: token, Audiences: []string{r.audience}},
	}
	out, err := r.client.AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return agentrpc.ReviewedPod{}, fmt.Errorf("submitting token review: %w", err)
	}
	return reviewedPodFromStatus(out.Status)
}

// reviewedPodFromStatus extracts the pod reference from an authenticated bound
// token's review status. It is the pure parsing core (unit-tested): an
// unauthenticated review, or an authenticated-but-not-pod-bound token, is an
// error — never a resolvable caller.
func reviewedPodFromStatus(st authv1.TokenReviewStatus) (agentrpc.ReviewedPod, error) {
	if !st.Authenticated {
		if st.Error != "" {
			return agentrpc.ReviewedPod{}, fmt.Errorf("token review: not authenticated: %s", st.Error)
		}
		return agentrpc.ReviewedPod{}, errors.New("token review: not authenticated")
	}
	podName := firstExtra(st.User.Extra, boundTokenPodNameKey)
	if podName == "" {
		return agentrpc.ReviewedPod{}, errors.New("token review: token is not pod-bound (no pod-name in bound-token extra)")
	}
	return agentrpc.ReviewedPod{
		Namespace:      namespaceFromSAUsername(st.User.Username),
		PodName:        podName,
		PodUID:         firstExtra(st.User.Extra, boundTokenPodUIDKey),
		ServiceAccount: st.User.Username,
	}, nil
}

// firstExtra returns the first value of a TokenReview "extra" key, or "".
func firstExtra(extra map[string]authv1.ExtraValue, key string) string {
	if vals, ok := extra[key]; ok && len(vals) > 0 {
		return vals[0]
	}
	return ""
}

// namespaceFromSAUsername pulls the namespace out of a ServiceAccount username of
// the form "system:serviceaccount:<namespace>:<name>". Returns "" if it does not
// match, letting the resolver fall back to its configured task namespace.
func namespaceFromSAUsername(username string) string {
	const prefix = "system:serviceaccount:"
	if !strings.HasPrefix(username, prefix) {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(username, prefix), ":")
	if len(parts) != 2 {
		return ""
	}
	return parts[0]
}

// PodResolver maps a reviewed pod to the task-instance identity the control plane
// stamped on it at dispatch. It implements agentrpc.PodTaskResolver.
type PodResolver struct {
	client    kubernetes.Interface
	namespace string
}

var _ agentrpc.PodTaskResolver = (*PodResolver)(nil)

// NewPodResolver builds a resolver that reads pods from the task namespace
// (falling back to it when the reviewed pod carries no namespace).
func NewPodResolver(client kubernetes.Interface, namespace string) *PodResolver {
	return &PodResolver{client: client, namespace: namespace}
}

// ResolveAgent fetches the reviewed pod once and resolves it to an agent identity,
// branching on the warm-worker LABEL (ADR 0058 D1): a pod carrying
// executor.WarmWorkerLabel resolves to a WORKER-scoped identity (its dag_version
// pool + tenant from labels, its worker id from the pod name, no task claims); any
// other pod resolves to the task instance the executor stamped on it, exactly as
// ResolveTaskInstance does. A UID mismatch (a name reused by a newer pod) fails
// closed for both flavors.
func (r *PodResolver) ResolveAgent(ctx context.Context, pod agentrpc.ReviewedPod) (auth.AgentIdentity, error) {
	ns, got, err := r.getReviewedPod(ctx, pod)
	if err != nil {
		return auth.AgentIdentity{}, err
	}
	if got.Labels[executor.WarmWorkerLabel] == executor.WarmWorkerLabelValue {
		dagVersion := got.Labels[executor.WarmDagVersionLabel]
		if dagVersion == "" {
			return auth.AgentIdentity{}, fmt.Errorf("warm pod %s/%s has no %s label", ns, pod.PodName, executor.WarmDagVersionLabel)
		}
		workerID := pod.PodName
		if workerID == "" {
			workerID = pod.PodUID
		}
		return auth.AgentIdentity{
			Scope:        auth.ScopeWarmWorker,
			DagVersionID: dagVersion,
			TenantID:     got.Labels[executor.WarmTenantLabel],
			WorkerID:     workerID,
		}, nil
	}
	return taskIdentityFromPod(ns, pod.PodName, got)
}

// ResolveTaskInstance resolves the reviewed pod to the task instance the executor
// stamped on it (the shared executor.AgentIdentityAnnotation contract), so the
// minted JWT is scoped to the true attempt rather than a lossy label read. It is
// the pure task path — a warm pod has no task instance and is an error here; the
// exchange takes the warm branch via ResolveAgent instead. A UID mismatch or a
// missing annotation fails closed.
func (r *PodResolver) ResolveTaskInstance(ctx context.Context, pod agentrpc.ReviewedPod) (auth.AgentIdentity, error) {
	ns, got, err := r.getReviewedPod(ctx, pod)
	if err != nil {
		return auth.AgentIdentity{}, err
	}
	return taskIdentityFromPod(ns, pod.PodName, got)
}

// getReviewedPod fetches the reviewed pod from its namespace (falling back to the
// resolver's configured task namespace) and guards against a stale pod whose name
// was reused by a newer UID.
func (r *PodResolver) getReviewedPod(ctx context.Context, pod agentrpc.ReviewedPod) (string, *corev1.Pod, error) {
	ns := pod.Namespace
	if ns == "" {
		ns = r.namespace
	}
	got, err := r.client.CoreV1().Pods(ns).Get(ctx, pod.PodName, metav1.GetOptions{})
	if err != nil {
		return ns, nil, fmt.Errorf("getting pod %s/%s: %w", ns, pod.PodName, err)
	}
	if pod.PodUID != "" && string(got.UID) != pod.PodUID {
		return ns, nil, fmt.Errorf("pod %s/%s uid mismatch: reviewed %q, current %q (stale pod)", ns, pod.PodName, pod.PodUID, got.UID)
	}
	return ns, got, nil
}

// taskIdentityFromPod reads the executor-stamped identity annotation off a fetched
// pod into a task-scoped AgentIdentity, failing closed on a missing/empty
// annotation or a missing task instance id.
func taskIdentityFromPod(ns, podName string, got *corev1.Pod) (auth.AgentIdentity, error) {
	raw, ok := got.Annotations[executor.AgentIdentityAnnotation]
	if !ok || raw == "" {
		return auth.AgentIdentity{}, fmt.Errorf("pod %s/%s has no %s annotation", ns, podName, executor.AgentIdentityAnnotation)
	}
	id, err := executor.ParseAgentIdentity(raw)
	if err != nil {
		return auth.AgentIdentity{}, err
	}
	if id.TaskInstanceID == "" {
		return auth.AgentIdentity{}, fmt.Errorf("pod %s/%s identity annotation carries no task instance id", ns, podName)
	}
	return auth.AgentIdentity{
		TaskInstanceID: id.TaskInstanceID,
		TenantID:       id.TenantID,
		DagID:          id.DagID,
		RunID:          id.RunID,
		TaskID:         id.TaskID,
		TryNumber:      id.TryNumber,
	}, nil
}
