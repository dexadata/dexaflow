package kubeexchange

import (
	"context"
	"testing"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestReviewedPodFromStatus parses the bound-token TokenReview status into a
// ReviewedPod: an authenticated review yields the pod name/uid from the apiserver
// "extra" keys and the namespace from the ServiceAccount username (ADR 0055 D7).
func TestReviewedPodFromStatus(t *testing.T) {
	st := authv1.TokenReviewStatus{
		Authenticated: true,
		User: authv1.UserInfo{
			Username: "system:serviceaccount:leoflow:task",
			Extra: map[string]authv1.ExtraValue{
				boundTokenPodNameKey: {"leoflow-etl-extract-1-abcd"},
				boundTokenPodUIDKey:  {"uid-123"},
			},
		},
	}
	pod, err := reviewedPodFromStatus(st)
	if err != nil {
		t.Fatalf("reviewedPodFromStatus: %v", err)
	}
	if pod.PodName != "leoflow-etl-extract-1-abcd" || pod.PodUID != "uid-123" {
		t.Errorf("pod name/uid = %q/%q", pod.PodName, pod.PodUID)
	}
	if pod.Namespace != "leoflow" {
		t.Errorf("namespace = %q, want leoflow (from SA username)", pod.Namespace)
	}
}

// TestReviewedPodFromStatusRejectsUnauthenticated: authenticated=false (a bad,
// expired, or wrong-audience token — the apiserver sets this) is an error, never
// a ReviewedPod.
func TestReviewedPodFromStatusRejectsUnauthenticated(t *testing.T) {
	if _, err := reviewedPodFromStatus(authv1.TokenReviewStatus{Authenticated: false, Error: "bad audience"}); err == nil {
		t.Error("an unauthenticated review must be an error")
	}
}

// TestReviewedPodFromStatusRejectsMissingPod: an authenticated token that is not
// pod-bound (no pod-name extra) cannot resolve to a task instance — reject rather
// than mint a token for an ambiguous caller.
func TestReviewedPodFromStatusRejectsMissingPod(t *testing.T) {
	st := authv1.TokenReviewStatus{Authenticated: true, User: authv1.UserInfo{Username: "system:serviceaccount:leoflow:task"}}
	if _, err := reviewedPodFromStatus(st); err == nil {
		t.Error("a non-pod-bound token must be an error")
	}
}

// TestPodResolverReadsIdentityAnnotation: the resolver reads the exact identity
// the executor stamped on the pod (via the shared AgentIdentityAnnotation
// contract), so scoping/liveness key on the true attempt — not lossy labels.
func TestPodResolverReadsIdentityAnnotation(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "leoflow-etl-extract-1-abcd",
		Namespace: "leoflow",
		UID:       "uid-123",
		Annotations: map[string]string{
			executor.AgentIdentityAnnotation: `{"ti":"ti-1","tenant":"acme","dag":"ETL","run":"run-1","task":"Extract","try":2}`,
		},
	}}
	cs := fake.NewClientset(pod)
	r := NewPodResolver(cs, "leoflow")

	id, err := r.ResolveTaskInstance(context.Background(), agentrpc.ReviewedPod{Namespace: "leoflow", PodName: "leoflow-etl-extract-1-abcd", PodUID: "uid-123"})
	if err != nil {
		t.Fatalf("ResolveTaskInstance: %v", err)
	}
	if id.TaskInstanceID != "ti-1" || id.TenantID != "acme" || id.DagID != "ETL" || id.TaskID != "Extract" || id.TryNumber != 2 {
		t.Errorf("resolved identity = %+v", id)
	}
}

// TestPodResolverRejectsStaleUID: a name reused by a different pod (UID mismatch)
// must not resolve — the reviewed token was issued for a different pod object.
func TestPodResolverRejectsStaleUID(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "leoflow-etl-extract-1-abcd", Namespace: "leoflow", UID: "current-uid",
		Annotations: map[string]string{executor.AgentIdentityAnnotation: `{"ti":"ti-1"}`},
	}}
	cs := fake.NewClientset(pod)
	r := NewPodResolver(cs, "leoflow")
	if _, err := r.ResolveTaskInstance(context.Background(), agentrpc.ReviewedPod{PodName: "leoflow-etl-extract-1-abcd", PodUID: "stale-uid"}); err == nil {
		t.Error("a UID mismatch must be an error")
	}
}

// TestPodResolverRejectsMissingAnnotation: a pod without the identity annotation
// (e.g. an env-var-transport pod) cannot be resolved via the exchange path.
func TestPodResolverRejectsMissingAnnotation(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "leoflow", UID: "u"}}
	cs := fake.NewClientset(pod)
	r := NewPodResolver(cs, "leoflow")
	if _, err := r.ResolveTaskInstance(context.Background(), agentrpc.ReviewedPod{PodName: "p", PodUID: "u"}); err == nil {
		t.Error("a pod without the identity annotation must be an error")
	}
}

// TestPodResolverResolvesWarmWorkerByLabel: a pod carrying the warm-worker label
// resolves (via ResolveAgent, DP1) to a WORKER-scoped identity — its dag_version
// pool + tenant from labels and its worker id from the pod name — with NO task
// claims, even though it has no identity annotation.
func TestPodResolverResolvesWarmWorkerByLabel(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "leoflow-warm-dagver-9-abcd",
		Namespace: "leoflow",
		UID:       "uid-w",
		Labels: map[string]string{
			executor.WarmWorkerLabel:     executor.WarmWorkerLabelValue,
			executor.WarmDagVersionLabel: "dagver-9",
			executor.WarmTenantLabel:     "acme",
		},
	}}
	cs := fake.NewClientset(pod)
	r := NewPodResolver(cs, "leoflow")

	id, err := r.ResolveAgent(context.Background(), agentrpc.ReviewedPod{Namespace: "leoflow", PodName: "leoflow-warm-dagver-9-abcd", PodUID: "uid-w"})
	if err != nil {
		t.Fatalf("ResolveAgent (warm): %v", err)
	}
	if id.Scope != auth.ScopeWarmWorker {
		t.Errorf("scope = %q, want %q", id.Scope, auth.ScopeWarmWorker)
	}
	if id.DagVersionID != "dagver-9" || id.TenantID != "acme" {
		t.Errorf("warm identity dag_version/tenant = %q/%q", id.DagVersionID, id.TenantID)
	}
	if id.WorkerID != "leoflow-warm-dagver-9-abcd" {
		t.Errorf("worker id = %q, want the pod name", id.WorkerID)
	}
	if id.TaskInstanceID != "" || id.RunID != "" || id.TaskID != "" || id.TryNumber != 0 {
		t.Errorf("warm identity must carry no task claims, got %+v", id)
	}
}

// TestPodResolverResolveAgentTaskPath: a non-warm pod resolves (via ResolveAgent)
// to its task instance exactly as ResolveTaskInstance does — the DP1 default branch.
func TestPodResolverResolveAgentTaskPath(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "leoflow-etl-extract-1-abcd",
		Namespace: "leoflow",
		UID:       "uid-123",
		Annotations: map[string]string{
			executor.AgentIdentityAnnotation: `{"ti":"ti-1","tenant":"acme","dag":"ETL","run":"run-1","task":"Extract","try":2}`,
		},
	}}
	cs := fake.NewClientset(pod)
	r := NewPodResolver(cs, "leoflow")

	id, err := r.ResolveAgent(context.Background(), agentrpc.ReviewedPod{Namespace: "leoflow", PodName: "leoflow-etl-extract-1-abcd", PodUID: "uid-123"})
	if err != nil {
		t.Fatalf("ResolveAgent (task): %v", err)
	}
	if id.Scope != "" || id.TaskInstanceID != "ti-1" {
		t.Errorf("task path identity = %+v, want scope empty + ti-1", id)
	}
}

// TestPodResolverCarriesAttemptEpoch: the exchange mints the JWT for the exact
// execution the executor stamped, epoch included (ADR 0051 amendment). A pod
// annotated before the epoch existed resolves with the presence bit unset, so
// the exchanged token is a legacy token, never one upgraded to the row's
// current epoch.
func TestPodResolverCarriesAttemptEpoch(t *testing.T) {
	cases := []struct {
		name       string
		annotation string
		wantHas    bool
		wantEpoch  int
	}{
		{"with epoch", `{"ti":"ti-1","tenant":"acme","dag":"ETL","run":"run-1","task":"Extract","try":2,"epoch":3}`, true, 3},
		{"epoch zero", `{"ti":"ti-1","tenant":"acme","dag":"ETL","run":"run-1","task":"Extract","try":2,"epoch":0}`, true, 0},
		{"legacy pod", `{"ti":"ti-1","tenant":"acme","dag":"ETL","run":"run-1","task":"Extract","try":2}`, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "p", Namespace: "leoflow", UID: "uid-1",
				Annotations: map[string]string{executor.AgentIdentityAnnotation: tc.annotation},
			}}
			r := NewPodResolver(fake.NewClientset(pod), "leoflow")
			for name, resolve := range map[string]func(context.Context, agentrpc.ReviewedPod) (auth.AgentIdentity, error){
				"ResolveTaskInstance": r.ResolveTaskInstance,
				"ResolveAgent":        r.ResolveAgent,
			} {
				id, err := resolve(context.Background(), agentrpc.ReviewedPod{Namespace: "leoflow", PodName: "p", PodUID: "uid-1"})
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if id.HasAttemptEpoch != tc.wantHas || id.AttemptEpoch != tc.wantEpoch {
					t.Errorf("%s: epoch has=%v n=%d, want has=%v n=%d", name, id.HasAttemptEpoch, id.AttemptEpoch, tc.wantHas, tc.wantEpoch)
				}
			}
		})
	}
}
