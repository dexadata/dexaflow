package executor

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestServiceAccountsImagePullSecrets: the reader reports, for a
// ServiceAccount of its namespace, how many imagePullSecrets it carries,
// found false for one that does not exist (also in another namespace), and
// any other failure as an error (#1889).
func TestServiceAccountsImagePullSecrets(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "dexaflow-task", Namespace: "tasks"},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "regcred"}, {Name: "ecr"}}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "tasks"}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: "other"}},
	)
	r := NewServiceAccounts(cs, "tasks")
	for _, tc := range []struct {
		name  string
		count int
		found bool
	}{
		{"dexaflow-task", 2, true},
		{"default", 0, true},
		{"elsewhere", 0, false},
		{"gone", 0, false},
	} {
		n, found, err := r.ImagePullSecrets(context.Background(), tc.name)
		if err != nil || n != tc.count || found != tc.found {
			t.Errorf("%s: ImagePullSecrets = %d, %v, %v; want %d, %v, nil", tc.name, n, found, err, tc.count, tc.found)
		}
	}

	denied := fake.NewClientset()
	denied.PrependReactor("get", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("serviceaccounts is forbidden")
	})
	if _, _, err := NewServiceAccounts(denied, "tasks").ImagePullSecrets(context.Background(), "x"); err == nil {
		t.Error("a refused lookup returned no error")
	}
}
