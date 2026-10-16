package executor

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ServiceAccounts reads the ServiceAccounts of the namespace task pods run
// in, for the register dry run (#1889, ADR 0085): whether the account a task
// pod would run as exists, and how many pull secrets it carries. It reads
// the account only, never the Secrets it names, with the one `get` on
// serviceaccounts the chart's Role grants.
type ServiceAccounts struct {
	clientset kubernetes.Interface
	namespace string
}

// NewServiceAccounts returns a reader of the ServiceAccounts of namespace,
// "default" when empty, as NewKubernetesExecutor places task pods.
func NewServiceAccounts(clientset kubernetes.Interface, namespace string) *ServiceAccounts {
	if namespace == "" {
		namespace = "default"
	}
	return &ServiceAccounts{clientset: clientset, namespace: namespace}
}

// ImagePullSecrets returns how many imagePullSecrets the ServiceAccount name
// carries, and found false when the namespace has no such account. Any other
// failure (a refusal, an apiserver that does not answer) is an error.
func (s *ServiceAccounts) ImagePullSecrets(ctx context.Context, name string) (count int, found bool, err error) {
	sa, err := s.clientset.CoreV1().ServiceAccounts(s.namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("reading ServiceAccount %s/%s: %w", s.namespace, name, err)
	}
	return len(sa.ImagePullSecrets), true, nil
}
