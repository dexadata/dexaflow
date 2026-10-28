package migrator

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestKubeStoreRoundTrip(t *testing.T) {
	cs := fake.NewClientset()
	s := &kubeStore{client: cs, namespace: "ns", name: "rel-installation"}
	got, err := s.Get(t.Context())
	if err != nil || got.found {
		t.Fatalf("Get on an empty namespace = %+v, %v; want not found", got, err)
	}
	if err = s.Create(t.Context(), "id-1"); err != nil {
		t.Fatal(err)
	}
	cm, err := cs.CoreV1().ConfigMaps("ns").Get(t.Context(), "rel-installation", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cm.Data[InstallationIDKey] != "id-1" || cm.Labels["app.kubernetes.io/managed-by"] != "dexaflow-migrate" {
		t.Fatalf("created ConfigMap = %+v", cm)
	}
	if err = s.Update(t.Context(), "id-2"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(t.Context())
	if err != nil || got != (storedID{found: true, id: "id-2"}) {
		t.Fatalf("Get after Update = %+v, %v; want id-2", got, err)
	}
	if err := s.Create(t.Context(), "id-3"); !errors.Is(err, errStoreRace) {
		t.Fatalf("Create over an existing ConfigMap = %v, want a race", err)
	}
}

// A ConfigMap that lost its key is not read as no ConfigMap, which would
// adopt whatever database this is.
func TestKubeStoreRefusesAConfigMapWithoutTheKey(t *testing.T) {
	cs := fake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}})
	s := &kubeStore{client: cs, namespace: "ns", name: "c"}
	if _, err := s.Get(t.Context()); err == nil {
		t.Fatal("Get of a ConfigMap without the key succeeded")
	}
}

func TestKubeStoreUpdateConflictIsARace(t *testing.T) {
	cs := fake.NewClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Data:       map[string]string{InstallationIDKey: "a"},
	})
	cs.PrependReactor("update", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(corev1.Resource("configmaps"), "c", errors.New("changed"))
	})
	s := &kubeStore{client: cs, namespace: "ns", name: "c"}
	err := s.Update(t.Context(), "b")
	if !errors.Is(err, errStoreRace) {
		t.Fatalf("Update on a conflict = %v, want a race", err)
	}
	if f := storeFailure("writing", s, err); f.Code != ExitTransient {
		t.Fatalf("a race exits %d, want %d (retried)", f.Code, ExitTransient)
	}
}
