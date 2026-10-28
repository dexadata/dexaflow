package migrator

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// InstallationIDKey is the ConfigMap key that holds the installation id. The
// chart's Deployments read it into LEOFLOW_DATABASE_INSTALLATION_ID (database.installation_id).
const InstallationIDKey = "installationId"

// kubeStore is the installation ConfigMap in the release namespace. The
// wrapper creates and updates it itself rather than the chart rendering it,
// so no Helm lookup is involved and `helm template`, Argo CD and Flux installs
// behave like `helm install` (ADR 0090 decision 3); `helm uninstall` leaves it
// in place.
type kubeStore struct {
	client    kubernetes.Interface
	namespace string
	name      string
}

// newKubeStore reaches the Kubernetes API with the pod's ServiceAccount.
func newKubeStore(namespace, name string) (*kubeStore, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, &Failure{
			Kind: KindUsage, Code: ExitUsage, Cause: err,
			Summary: "-installation-configmap needs the Kubernetes API, and this process is not running in a pod " +
				"with a ServiceAccount token. Run it as the chart's migration Job, or leave out the -installation-* " +
				"flags to migrate without the installation id.",
		}
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building the Kubernetes client: %w", err)
	}
	return &kubeStore{client: cs, namespace: namespace, name: name}, nil
}

// String is namespace/name.
func (s *kubeStore) String() string { return s.namespace + "/" + s.name }

// Get reads the id. A ConfigMap without the key is an error rather than no
// ConfigMap: reading it as absent would adopt whatever database this is.
func (s *kubeStore) Get(ctx context.Context) (storedID, error) {
	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, s.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return storedID{}, nil
	}
	if err != nil {
		return storedID{}, err
	}
	id := cm.Data[InstallationIDKey]
	if id == "" {
		return storedID{}, fmt.Errorf("the ConfigMap has no %s key; restore it, or delete the ConfigMap to adopt the id the database carries", InstallationIDKey)
	}
	return storedID{found: true, id: id}, nil
}

// Create creates the ConfigMap. One that appeared since Get is a race with
// another run, retried.
func (s *kubeStore) Create(ctx context.Context, id string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.name,
			Namespace: s.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dexaflow-migrate",
				"app.kubernetes.io/part-of":    "dexaflow",
			},
			Annotations: map[string]string{
				"dexaflow.dexadata.ai/description": "The installation id of this release's database (ADR 0090). " +
					"Written by the migration Job; change it only through the chart's installation.id.",
			},
		},
		Data: map[string]string{InstallationIDKey: id},
	}
	_, err := s.client.CoreV1().ConfigMaps(s.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("%w: %w", errStoreRace, err)
	}
	return err
}

// Update sets the id on the ConfigMap Get read. A conflict is a race with
// another run, retried.
func (s *kubeStore) Update(ctx context.Context, id string) error {
	cms := s.client.CoreV1().ConfigMaps(s.namespace)
	cm, err := cms.Get(ctx, s.name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[InstallationIDKey] = id
	_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %w", errStoreRace, err)
	}
	return err
}
