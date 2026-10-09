package executor

import (
	"context"
	"reflect"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// TestKubernetesWarmPodsListSelectsWarmOnly proves the cluster-backed client lists
// ONLY warm-worker pods (by label) and reads the dag_version each serves, so a
// namespace full of ordinary task pods does not confuse the reconciler's counts.
func TestKubernetesWarmPodsListSelectsWarmOnly(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "warm-1", Namespace: "leoflow",
			Labels: map[string]string{warmWorkerLabelKey: warmWorkerLabelVal, warmDagVersionLabelKey: "dv1"},
		}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "task-1", Namespace: "leoflow",
			Labels: map[string]string{"leoflow.io/run-id": "r1"},
		}},
	)
	k := NewKubernetesWarmPods(cs, "leoflow", nil)
	got, err := k.ListWarmPods(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPods: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d warm pods, want 1 (task pods must be excluded): %+v", len(got), got)
	}
	if got[0].Name != "warm-1" || got[0].DagVersionID != "dv1" {
		t.Errorf("listed %+v, want {warm-1 dv1}", got[0])
	}
}

// TestKubernetesWarmPodsListReadsTenant proves ListWarmPods reads the tenant label
// into WarmPodInfo.TenantID (M4), and a pre-label pod (no tenant label) reads "" so
// the reconciler treats it as unattributable.
func TestKubernetesWarmPodsListReadsTenant(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "labeled", Namespace: "leoflow",
			Labels: map[string]string{warmWorkerLabelKey: warmWorkerLabelVal, warmDagVersionLabelKey: "dv1", warmTenantLabelKey: "tenant-a"},
		}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "prelabel", Namespace: "leoflow",
			Labels: map[string]string{warmWorkerLabelKey: warmWorkerLabelVal, warmDagVersionLabelKey: "dv1"},
		}},
	)
	k := NewKubernetesWarmPods(cs, "leoflow", nil)
	got, err := k.ListWarmPods(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPods: %v", err)
	}
	tenant := map[string]string{}
	for _, p := range got {
		tenant[p.Name] = p.TenantID
	}
	if tenant["labeled"] != "tenant-a" {
		t.Errorf("labeled pod TenantID = %q, want tenant-a", tenant["labeled"])
	}
	if tenant["prelabel"] != "" {
		t.Errorf("pre-label pod TenantID = %q, want \"\" (unattributable)", tenant["prelabel"])
	}
}

// TestKubernetesWarmPodsListFlagsTerminal proves ListWarmPods marks Succeeded
// and Failed warm pods Terminal (dead RestartPolicy:Never workers) while a
// Running/Pending pod stays live, so the reconciler can replace and reap them.
func TestKubernetesWarmPodsListFlagsTerminal(t *testing.T) {
	warm := func(name string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "leoflow",
				Labels: map[string]string{warmWorkerLabelKey: warmWorkerLabelVal, warmDagVersionLabelKey: "dv1"},
			},
			Status: corev1.PodStatus{Phase: phase},
		}
	}
	cs := fake.NewSimpleClientset(
		warm("running", corev1.PodRunning),
		warm("succeeded", corev1.PodSucceeded),
		warm("failed", corev1.PodFailed),
	)
	k := NewKubernetesWarmPods(cs, "leoflow", nil)
	got, err := k.ListWarmPods(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPods: %v", err)
	}
	terminal := map[string]bool{}
	for _, p := range got {
		terminal[p.Name] = p.Terminal
	}
	if terminal["running"] {
		t.Errorf("running pod flagged Terminal, want live")
	}
	if !terminal["succeeded"] || !terminal["failed"] {
		t.Errorf("Succeeded/Failed pods not flagged Terminal: %+v", got)
	}
}

// TestKubernetesWarmPodsCreateBuildsAndCreates proves CreateWarmPod runs the
// injected spec builder, builds a warm pod, and Creates it in the namespace.
func TestKubernetesWarmPodsCreateBuildsAndCreates(t *testing.T) {
	cs := fake.NewSimpleClientset()
	newSpec := func(t WarmTarget) (WarmPodSpec, error) {
		return WarmPodSpec{DagVersionID: t.DagVersionID, Image: t.Image, BootstrapToken: "tok"}, nil
	}
	k := NewKubernetesWarmPods(cs, "leoflow", newSpec)
	if err := k.CreateWarmPod(context.Background(), WarmTarget{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 1}, "", ""); err != nil {
		t.Fatalf("CreateWarmPod: %v", err)
	}
	pods, err := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{
		LabelSelector: warmWorkerLabelKey + "=" + warmWorkerLabelVal,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("created %d warm pods, want 1", len(pods.Items))
	}
	if got := pods.Items[0].Spec.Containers[0].Image; got != "img" {
		t.Errorf("created pod image = %q, want img", got)
	}
}

// TestKubernetesWarmPodsCreateStampsAnchorOwner proves CreateWarmPod threads the
// anchor name+UID onto the spec so the created pod carries the D11 ownerReference
// to its GC anchor.
func TestKubernetesWarmPodsCreateStampsAnchorOwner(t *testing.T) {
	cs := fake.NewSimpleClientset()
	newSpec := func(t WarmTarget) (WarmPodSpec, error) {
		return WarmPodSpec{DagVersionID: t.DagVersionID, Image: t.Image}, nil
	}
	k := NewKubernetesWarmPods(cs, "leoflow", newSpec)
	if err := k.CreateWarmPod(context.Background(), WarmTarget{DagVersionID: "dv1", Image: "img"}, "leoflow-pool-dv1", "anchor-uid-123"); err != nil {
		t.Fatalf("CreateWarmPod: %v", err)
	}
	pods, err := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{
		LabelSelector: warmWorkerLabelKey + "=" + warmWorkerLabelVal,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("created %d warm pods, want 1", len(pods.Items))
	}
	owners := pods.Items[0].OwnerReferences
	if len(owners) != 1 {
		t.Fatalf("created pod has %d ownerReferences, want exactly 1 (the GC anchor)", len(owners))
	}
	o := owners[0]
	if o.Kind != "ConfigMap" || o.Name != "leoflow-pool-dv1" || o.UID != types.UID("anchor-uid-123") {
		t.Errorf("ownerReference = %+v, want ConfigMap/leoflow-pool-dv1/anchor-uid-123", o)
	}
}

// TestKubernetesEnsureWarmAnchorIdempotent proves EnsureWarmAnchor creates the
// anchor ConfigMap and returns its UID on the first call, and on a second call
// (AlreadyExists) is NOT an error and returns the SAME UID — so every warm pod
// created across ticks is stamped with the same owner UID. The apiserver-assigned
// UID is simulated by a create reactor (the fake tracker assigns none).
func TestKubernetesEnsureWarmAnchorIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		cm := action.(ktesting.CreateAction).GetObject().(*corev1.ConfigMap)
		if cm.UID == "" {
			cm.UID = types.UID("uid-" + cm.Name)
		}
		return false, nil, nil // fall through to the tracker so it is stored + AlreadyExists works
	})
	k := NewKubernetesWarmPods(cs, "leoflow", nil)

	uid1, err := k.EnsureWarmAnchor(context.Background(), "dv1")
	if err != nil {
		t.Fatalf("EnsureWarmAnchor (first): %v", err)
	}
	if uid1 == "" {
		t.Fatal("EnsureWarmAnchor returned an empty UID on create, want a non-empty UID")
	}

	// The anchor exists with the discoverability labels and no data.
	cm, err := cs.CoreV1().ConfigMaps("leoflow").Get(context.Background(), warmAnchorName("dv1"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("anchor not created: %v", err)
	}
	if cm.Labels[warmAnchorLabelKey] != warmAnchorLabelVal || cm.Labels[warmDagVersionLabelKey] != "dv1" {
		t.Errorf("anchor labels = %v, want warm-anchor=true + dag-version-id=dv1", cm.Labels)
	}
	if len(cm.Data) != 0 || len(cm.BinaryData) != 0 {
		t.Errorf("anchor carries data (%v/%v), want empty (it is a GC anchor)", cm.Data, cm.BinaryData)
	}

	uid2, err := k.EnsureWarmAnchor(context.Background(), "dv1")
	if err != nil {
		t.Fatalf("EnsureWarmAnchor (second, AlreadyExists) = %v, want nil (idempotent)", err)
	}
	if uid2 != uid1 {
		t.Errorf("second EnsureWarmAnchor UID = %q, want the same as the first %q", uid2, uid1)
	}
}

// TestKubernetesDeleteWarmAnchor proves DeleteWarmAnchor removes the anchor and
// tolerates NotFound (a second delete, or a version whose anchor was never created).
func TestKubernetesDeleteWarmAnchor(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: warmAnchorName("dv1"), Namespace: "leoflow",
		Labels: map[string]string{warmAnchorLabelKey: warmAnchorLabelVal, warmDagVersionLabelKey: "dv1"},
	}})
	k := NewKubernetesWarmPods(cs, "leoflow", nil)

	if err := k.DeleteWarmAnchor(context.Background(), "dv1"); err != nil {
		t.Fatalf("DeleteWarmAnchor: %v", err)
	}
	if _, err := cs.CoreV1().ConfigMaps("leoflow").Get(context.Background(), warmAnchorName("dv1"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("anchor still present after delete (err=%v), want NotFound", err)
	}
	// NotFound is tolerated: deleting an already-gone anchor is not an error.
	if err := k.DeleteWarmAnchor(context.Background(), "dv1"); err != nil {
		t.Errorf("second DeleteWarmAnchor = %v, want nil (NotFound tolerated)", err)
	}
}

// TestKubernetesWarmPodsDelete proves DeleteWarmPod removes the named pod.
func TestKubernetesWarmPodsDelete(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "warm-1", Namespace: "leoflow",
		Labels: map[string]string{warmWorkerLabelKey: warmWorkerLabelVal, warmDagVersionLabelKey: "dv1"},
	}})
	k := NewKubernetesWarmPods(cs, "leoflow", nil)
	if err := k.DeleteWarmPod(context.Background(), "warm-1"); err != nil {
		t.Fatalf("DeleteWarmPod: %v", err)
	}
	got, err := k.ListWarmPods(context.Background())
	if err != nil {
		t.Fatalf("ListWarmPods: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("after delete listed %d, want 0", len(got))
	}
}

// TestKubernetesWarmPodsListWarmAnchors (#1500): it lists exactly the warm-pool GC
// anchors (label leoflow.io/warm-anchor=true) in its namespace, returning each
// one's dag_version label; other ConfigMaps and other namespaces are ignored.
func TestKubernetesWarmPodsListWarmAnchors(t *testing.T) {
	anchor := func(name, ns, version string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			Labels: map[string]string{warmAnchorLabelKey: warmAnchorLabelVal, warmDagVersionLabelKey: version}}}
	}
	cs := fake.NewSimpleClientset(
		anchor(warmAnchorName("dv-a"), "leoflow", "dv-a"),
		anchor(warmAnchorName("dv-b"), "leoflow", "dv-b"),
		anchor(warmAnchorName("dv-other-ns"), "elsewhere", "dv-other-ns"),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "leoflow"}},
	)
	k := NewKubernetesWarmPods(cs, "leoflow", nil)

	got, err := k.ListWarmAnchors(context.Background())

	if err != nil {
		t.Fatalf("ListWarmAnchors: %v", err)
	}
	sort.Strings(got)
	if want := []string{"dv-a", "dv-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ListWarmAnchors = %v, want %v (only this namespace's anchors)", got, want)
	}
}
