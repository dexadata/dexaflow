package executor

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/domain"
)

func sampleReq() Request {
	return Request{
		TaskInstanceID: "ti-1", TenantID: "default", DagID: "etl", RunID: "r1", TaskID: "extract",
		TryNumber: 1, Image: "img:v1", ImagePullPolicy: "Always", Operator: "python",
		Resources:        domain.Resources{Requests: &domain.ResourceQuantity{CPU: "500m", Memory: "512Mi"}},
		Execution:        domain.Execution{NodeSelector: map[string]string{"disktype": "ssd"}, ServiceAccount: "sa"},
		TimeoutSeconds:   600,
		ControlPlaneAddr: "cp:9000",
		AgentToken:       "tok",
	}
}

func TestBuildPod(t *testing.T) {
	pod := BuildPod(sampleReq())

	if !strings.HasPrefix(pod.Name, "leoflow-etl-extract-1-") {
		t.Errorf("pod name = %q, want leoflow-etl-extract-1-* prefix", pod.Name)
	}
	if pod.Labels["leoflow.io/dag-id"] != "etl" || pod.Labels["leoflow.io/try-number"] != "1" {
		t.Errorf("labels = %v", pod.Labels)
	}
	if pod.Annotations["leoflow.io/task-instance-id"] != "ti-1" {
		t.Errorf("annotation = %v", pod.Annotations)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %v, want Never", pod.Spec.RestartPolicy)
	}
	// The declared 600s timeout plus the startup headroom and termination grace
	// that keep the agent — not the kubelet — the enforcer of the timeout.
	wantDeadline := int64(600 + 180 + corev1.DefaultTerminationGracePeriodSeconds)
	if pod.Spec.ActiveDeadlineSeconds == nil {
		t.Errorf("activeDeadlineSeconds = nil, want %d", wantDeadline)
	} else if *pod.Spec.ActiveDeadlineSeconds != wantDeadline {
		t.Errorf("activeDeadlineSeconds = %d, want %d", *pod.Spec.ActiveDeadlineSeconds, wantDeadline)
	}
	if pod.Spec.NodeSelector["disktype"] != "ssd" || pod.Spec.ServiceAccountName != "sa" {
		t.Errorf("placement: nodeSelector=%v sa=%q", pod.Spec.NodeSelector, pod.Spec.ServiceAccountName)
	}
	c := pod.Spec.Containers[0]
	if c.Image != "img:v1" || c.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("container image=%q pull=%v", c.Image, c.ImagePullPolicy)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["LEOFLOW_CONTROL_PLANE_ADDR"] != "cp:9000" || env["LEOFLOW_AGENT_TOKEN"] != "tok" || env["LEOFLOW_TASK_INSTANCE_ID"] != "ti-1" {
		t.Errorf("agent env not injected: %v", env)
	}
	if c.Resources.Requests.Cpu().String() != "500m" || c.Resources.Requests.Memory().String() != "512Mi" {
		t.Errorf("resources = %v", c.Resources.Requests)
	}
}

// TestBuildPodAppliesTolerations locks the fix for the dropped-tolerations contract
// bug: a task's declared tolerations (domain.Execution.Tolerations, an untyped
// []map[string]any) must land on pod.Spec.Tolerations. Without this a DAG that
// declares a toleration is accepted at push and silently mis-scheduled — a tainted
// dedicated node pool becomes unreachable.
func TestBuildPodAppliesTolerations(t *testing.T) {
	req := sampleReq()
	req.Execution.Tolerations = []map[string]any{
		{"key": "workload", "operator": "Equal", "value": "leoflow", "effect": "NoSchedule"},
		{"key": "dedicated", "operator": "Exists", "effect": "NoExecute"},
	}

	tols := BuildPod(req).Spec.Tolerations
	if len(tols) != 2 {
		t.Fatalf("tolerations = %v, want 2", tols)
	}
	if want := (corev1.Toleration{Key: "workload", Operator: corev1.TolerationOpEqual, Value: "leoflow", Effect: corev1.TaintEffectNoSchedule}); tols[0] != want {
		t.Errorf("tolerations[0] = %+v, want %+v", tols[0], want)
	}
	if want := (corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}); tols[1] != want {
		t.Errorf("tolerations[1] = %+v, want %+v", tols[1], want)
	}
}

// TestBuildPodNoTolerations confirms omission stays unset (nil) rather than an
// empty non-nil slice, so a task that declares none is byte-identical to today.
func TestBuildPodNoTolerations(t *testing.T) {
	if tols := BuildPod(sampleReq()).Spec.Tolerations; tols != nil {
		t.Errorf("tolerations = %v, want nil when none declared", tols)
	}
}

// TestBuildPodPinsTerminationMessagePolicy locks the ADR 0052 prerequisite: the
// task container must explicitly pin TerminationMessagePolicy=File and the path,
// so the agent's durable outcome record is surfaced on pod status. Relying on the
// Kubernetes default is fragile — an admission webhook or PodSecurity policy could
// mutate it and silently revert outcome recovery to phase-based failure.
func TestBuildPodPinsTerminationMessagePolicy(t *testing.T) {
	c := BuildPod(sampleReq()).Spec.Containers[0]
	if c.TerminationMessagePolicy != corev1.TerminationMessageReadFile {
		t.Errorf("TerminationMessagePolicy = %q, want File (ADR 0052)", c.TerminationMessagePolicy)
	}
	if c.TerminationMessagePath != "/dev/termination-log" {
		t.Errorf("TerminationMessagePath = %q, want /dev/termination-log", c.TerminationMessagePath)
	}
	// The agent must be told to write its record to the same path the container
	// surfaces, so the reader and writer agree.
	var envPath string
	for _, e := range c.Env {
		if e.Name == "LEOFLOW_TERMINATION_LOG_PATH" {
			envPath = e.Value
		}
	}
	if envPath != c.TerminationMessagePath {
		t.Errorf("LEOFLOW_TERMINATION_LOG_PATH = %q, want it to match TerminationMessagePath %q", envPath, c.TerminationMessagePath)
	}
}

// TestBuildPodAppliesPriorityClassName locks the shared-cluster preemption knob
// (ADR 0054): a declared PriorityClassName must land on the PodSpec so the
// scheduler preempts Dexaflow's ETL, not production, under contention.
func TestBuildPodAppliesPriorityClassName(t *testing.T) {
	req := sampleReq()
	req.Execution.PriorityClassName = "leoflow-batch"
	if got := BuildPod(req).Spec.PriorityClassName; got != "leoflow-batch" {
		t.Errorf("PriorityClassName = %q, want leoflow-batch", got)
	}
	// Omission stays the empty zero value (byte-identical to today).
	if got := BuildPod(sampleReq()).Spec.PriorityClassName; got != "" {
		t.Errorf("PriorityClassName = %q, want empty when none declared", got)
	}
}

// TestBuildPodAppliesTerminationGracePeriod asserts a declared grace period lands
// on the PodSpec, and omission leaves it nil (cluster default).
func TestBuildPodAppliesTerminationGracePeriod(t *testing.T) {
	req := sampleReq()
	req.Execution.TerminationGracePeriodSeconds = ptr(int64(45))
	got := BuildPod(req).Spec.TerminationGracePeriodSeconds
	if got == nil || *got != 45 {
		t.Errorf("TerminationGracePeriodSeconds = %v, want 45", got)
	}
	if got := BuildPod(sampleReq()).Spec.TerminationGracePeriodSeconds; got != nil {
		t.Errorf("TerminationGracePeriodSeconds = %v, want nil when none declared", got)
	}
}

// TestBuildPodAppliesRuntimeClassName asserts a declared RuntimeClassName lands on
// the PodSpec (e.g. a sandboxed or GPU runtime), and omission leaves it nil.
func TestBuildPodAppliesRuntimeClassName(t *testing.T) {
	req := sampleReq()
	req.Execution.RuntimeClassName = ptr("gvisor")
	got := BuildPod(req).Spec.RuntimeClassName
	if got == nil || *got != "gvisor" {
		t.Errorf("RuntimeClassName = %v, want gvisor", got)
	}
	if got := BuildPod(sampleReq()).Spec.RuntimeClassName; got != nil {
		t.Errorf("RuntimeClassName = %v, want nil when none declared", got)
	}
}

// TestBuildPodAppliesTopologySpreadConstraints locks the JSON round-trip of the
// untyped spread constraints onto typed pod constraints. A malformed entry is
// skipped, and omission stays nil.
func TestBuildPodAppliesTopologySpreadConstraints(t *testing.T) {
	req := sampleReq()
	req.Execution.TopologySpreadConstraints = []map[string]any{
		{
			"maxSkew":           1,
			"topologyKey":       "topology.kubernetes.io/zone",
			"whenUnsatisfiable": "DoNotSchedule",
			"labelSelector":     map[string]any{"matchLabels": map[string]any{"leoflow.io/dag-id": "etl"}},
		},
	}
	tscs := BuildPod(req).Spec.TopologySpreadConstraints
	if len(tscs) != 1 {
		t.Fatalf("topologySpreadConstraints = %v, want 1", tscs)
	}
	if tscs[0].MaxSkew != 1 || tscs[0].TopologyKey != "topology.kubernetes.io/zone" ||
		tscs[0].WhenUnsatisfiable != corev1.DoNotSchedule {
		t.Errorf("topologySpreadConstraints[0] = %+v", tscs[0])
	}
	if tscs[0].LabelSelector == nil || tscs[0].LabelSelector.MatchLabels["leoflow.io/dag-id"] != "etl" {
		t.Errorf("labelSelector not round-tripped: %+v", tscs[0].LabelSelector)
	}
	// Omission stays nil.
	if tscs := BuildPod(sampleReq()).Spec.TopologySpreadConstraints; tscs != nil {
		t.Errorf("topologySpreadConstraints = %v, want nil when none declared", tscs)
	}
}

// TestBuildPodAppliesAffinity locks the JSON round-trip of the untyped affinity
// object onto *corev1.Affinity, and asserts omission stays nil.
func TestBuildPodAppliesAffinity(t *testing.T) {
	req := sampleReq()
	req.Execution.Affinity = map[string]any{
		"nodeAffinity": map[string]any{
			"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
				"nodeSelectorTerms": []any{
					map[string]any{
						"matchExpressions": []any{
							map[string]any{"key": "accelerator", "operator": "In", "values": []any{"gpu"}},
						},
					},
				},
			},
		},
	}
	aff := BuildPod(req).Spec.Affinity
	if aff == nil || aff.NodeAffinity == nil || aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatalf("affinity not round-tripped: %+v", aff)
	}
	terms := aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchExpressions) != 1 || terms[0].MatchExpressions[0].Key != "accelerator" {
		t.Errorf("nodeAffinity term = %+v", terms)
	}
	// Omission stays nil.
	if aff := BuildPod(sampleReq()).Spec.Affinity; aff != nil {
		t.Errorf("affinity = %v, want nil when none declared", aff)
	}
}

// TestBuildPodAppliesResourceClaims locks Dynamic Resource Allocation passthrough
// (DRA, GA in Kubernetes 1.34): pod-level ResourceClaims and the container's
// resources.claims that reference them, both round-tripped from the untyped spec.
func TestBuildPodAppliesResourceClaims(t *testing.T) {
	req := sampleReq()
	req.Execution.ResourceClaims = []map[string]any{
		{"name": "gpu", "resourceClaimTemplateName": "single-gpu"},
	}
	req.Resources.Claims = []map[string]any{
		{"name": "gpu", "request": "gpu-req"},
	}
	pod := BuildPod(req)
	claims := pod.Spec.ResourceClaims
	if len(claims) != 1 || claims[0].Name != "gpu" ||
		claims[0].ResourceClaimTemplateName == nil || *claims[0].ResourceClaimTemplateName != "single-gpu" {
		t.Fatalf("pod resourceClaims = %+v", claims)
	}
	cclaims := pod.Spec.Containers[0].Resources.Claims
	if len(cclaims) != 1 || cclaims[0].Name != "gpu" || cclaims[0].Request != "gpu-req" {
		t.Errorf("container resources.claims = %+v", cclaims)
	}
	// Omission stays nil on both.
	base := BuildPod(sampleReq())
	if base.Spec.ResourceClaims != nil {
		t.Errorf("pod resourceClaims = %v, want nil when none declared", base.Spec.ResourceClaims)
	}
	if base.Spec.Containers[0].Resources.Claims != nil {
		t.Errorf("container resources.claims = %v, want nil when none declared", base.Spec.Containers[0].Resources.Claims)
	}
}

// TestBuildPodAppliesEphemeralStorage asserts ephemeral-storage requests/limits
// reach the container ResourceList, so a runaway task cannot evict its neighbors
// under disk pressure (ADR 0054). Omission leaves the key absent.
func TestBuildPodAppliesEphemeralStorage(t *testing.T) {
	req := sampleReq()
	req.Resources = domain.Resources{
		Requests: &domain.ResourceQuantity{CPU: "500m", Memory: "512Mi", EphemeralStorage: "1Gi"},
		Limits:   &domain.ResourceQuantity{EphemeralStorage: "2Gi"},
	}
	c := BuildPod(req).Spec.Containers[0]
	if got := c.Resources.Requests[corev1.ResourceEphemeralStorage]; got.String() != "1Gi" {
		t.Errorf("requests ephemeral-storage = %q, want 1Gi", got.String())
	}
	if got := c.Resources.Limits[corev1.ResourceEphemeralStorage]; got.String() != "2Gi" {
		t.Errorf("limits ephemeral-storage = %q, want 2Gi", got.String())
	}
	// Omission: sampleReq declares no ephemeral-storage, so the key is absent.
	base := BuildPod(sampleReq()).Spec.Containers[0]
	if _, ok := base.Resources.Requests[corev1.ResourceEphemeralStorage]; ok {
		t.Error("ephemeral-storage present when none declared")
	}
}

// TestBuildPodMergesLabelsAndAnnotations asserts operator-declared labels and
// annotations are merged onto the task pod, but Dexaflow's own leoflow.io/* labels
// and the task-instance-id annotation win any key collision — a DAG must not be
// able to shadow the identity the reconciler and terminate path select on.
func TestBuildPodMergesLabelsAndAnnotations(t *testing.T) {
	req := sampleReq()
	req.Execution.Labels = map[string]string{
		"team":                     "data-eng",
		"leoflow.io/dag-id":        "hijacked", // collision: Dexaflow must win
		"leoflow.io/attempt-epoch": "99",       // the settle fence reads it: Dexaflow must win
	}
	req.Execution.Annotations = map[string]string{
		"cost-center":                 "1234",
		"leoflow.io/task-instance-id": "hijacked", // collision: Dexaflow must win
	}
	pod := BuildPod(req)
	if pod.Labels["team"] != "data-eng" {
		t.Errorf("declared label not merged: %v", pod.Labels)
	}
	if pod.Labels["leoflow.io/dag-id"] != "etl" {
		t.Errorf("Dexaflow label overridden by DAG: %q, want etl", pod.Labels["leoflow.io/dag-id"])
	}
	if pod.Labels["leoflow.io/attempt-epoch"] != "0" {
		t.Errorf("attempt-epoch label overridden by DAG: %q, want 0", pod.Labels["leoflow.io/attempt-epoch"])
	}
	if pod.Annotations["cost-center"] != "1234" {
		t.Errorf("declared annotation not merged: %v", pod.Annotations)
	}
	if pod.Annotations["leoflow.io/task-instance-id"] != "ti-1" {
		t.Errorf("Dexaflow annotation overridden by DAG: %q, want ti-1", pod.Annotations["leoflow.io/task-instance-id"])
	}
	// Omission leaves only Dexaflow's own metadata (6 labels, 1 annotation).
	base := BuildPod(sampleReq())
	if len(base.Labels) != 6 || len(base.Annotations) != 1 {
		t.Errorf("unexpected base metadata: labels=%v annotations=%v", base.Labels, base.Annotations)
	}
}

func TestBuildPodMountsStagingVolume(t *testing.T) {
	// Without a staging claim, no extra volume is added.
	if vols := BuildPod(sampleReq()).Spec.Volumes; len(vols) != 0 {
		t.Errorf("no staging claim should add no volumes, got %v", vols)
	}
	// With a claim, the run's PVC is mounted at /staging and exposed via env.
	req := sampleReq()
	req.StagingClaim = "leoflow-staging-etl-r1"
	pod := BuildPod(req)
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].PersistentVolumeClaim == nil ||
		pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "leoflow-staging-etl-r1" {
		t.Fatalf("staging volume not wired to the PVC: %+v", pod.Spec.Volumes)
	}
	c := pod.Spec.Containers[0]
	mounted := false
	for _, m := range c.VolumeMounts {
		if m.MountPath == stagingMountPath && m.Name == pod.Spec.Volumes[0].Name {
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("staging volume not mounted at %s: %+v", stagingMountPath, c.VolumeMounts)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["LEOFLOW_STAGING_DIR"] != stagingMountPath {
		t.Errorf("LEOFLOW_STAGING_DIR = %q, want %s", env["LEOFLOW_STAGING_DIR"], stagingMountPath)
	}
}

func TestBuildPodMountsAgentTLSCA(t *testing.T) {
	// No CA configmap -> agent stays insecure (no TLS env, no extra volume).
	base := BuildPod(sampleReq())
	baseEnv := map[string]string{}
	for _, e := range base.Spec.Containers[0].Env {
		baseEnv[e.Name] = e.Value
	}
	if _, ok := baseEnv["LEOFLOW_AGENT_TLS_CA"]; ok {
		t.Error("no CA configmap should not set LEOFLOW_AGENT_TLS_CA")
	}
	// With a CA configmap, mount it and tell the agent to use TLS.
	req := sampleReq()
	req.AgentTLSCAConfigMap = "leoflow-agent-ca"
	pod := BuildPod(req)
	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].ConfigMap != nil && pod.Spec.Volumes[i].ConfigMap.Name == "leoflow-agent-ca" {
			vol = &pod.Spec.Volumes[i]
		}
	}
	if vol == nil {
		t.Fatalf("CA configmap not mounted as a volume: %+v", pod.Spec.Volumes)
	}
	c := pod.Spec.Containers[0]
	mounted := false
	for _, m := range c.VolumeMounts {
		if m.Name == vol.Name {
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("CA volume not mounted: %+v", c.VolumeMounts)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["LEOFLOW_AGENT_INSECURE"] != "false" {
		t.Errorf("LEOFLOW_AGENT_INSECURE = %q, want false", env["LEOFLOW_AGENT_INSECURE"])
	}
	if env["LEOFLOW_AGENT_TLS_CA"] == "" {
		t.Error("LEOFLOW_AGENT_TLS_CA must point to the mounted CA")
	}
}

func TestBuildPodMountsTaskSecret(t *testing.T) {
	// No task secret -> no extra volume/mount.
	base := BuildPod(sampleReq())
	for _, v := range base.Spec.Volumes {
		if v.Name == taskSecretVolumeName {
			t.Fatalf("no task secret configured should not mount a volume: %+v", base.Spec.Volumes)
		}
	}
	// With a task secret, mount it read-only at the configured path so a task can
	// read a credential by key_path (ADR 0035 — Dexaflow does not store the key).
	req := sampleReq()
	req.TaskSecretName = "gcp-sa-key"
	req.TaskSecretMountPath = "/var/secrets/gcp"
	pod := BuildPod(req)
	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Secret != nil && pod.Spec.Volumes[i].Secret.SecretName == "gcp-sa-key" {
			vol = &pod.Spec.Volumes[i]
		}
	}
	if vol == nil {
		t.Fatalf("task secret not mounted as a volume: %+v", pod.Spec.Volumes)
	}
	c := pod.Spec.Containers[0]
	mounted := false
	for _, m := range c.VolumeMounts {
		if m.Name == vol.Name {
			if m.MountPath != "/var/secrets/gcp" {
				t.Errorf("mount path = %q, want /var/secrets/gcp", m.MountPath)
			}
			if !m.ReadOnly {
				t.Error("task secret must be mounted read-only")
			}
			mounted = true
		}
	}
	if !mounted {
		t.Errorf("task secret volume not mounted: %+v", c.VolumeMounts)
	}
	// A name without a mount path mounts nothing (both are required).
	req2 := sampleReq()
	req2.TaskSecretName = "gcp-sa-key"
	for _, v := range BuildPod(req2).Spec.Volumes {
		if v.Name == taskSecretVolumeName {
			t.Error("a task secret without a mount path should not mount")
		}
	}
}

func TestBuildPodSanitizesName(t *testing.T) {
	req := sampleReq()
	req.DagID = "ETL Vendas"
	req.TaskID = "Extract_Data"
	if name := BuildPod(req).Name; !strings.HasPrefix(name, "leoflow-etl-vendas-extract-data-1-") {
		t.Errorf("sanitized name = %q", name)
	}
}

func TestKubernetesExecutorCreatesPod(t *testing.T) {
	cs := fake.NewClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	if _, err := e.Execute(context.Background(), sampleReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	pods, err := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("want 1 pod created, got %d", len(pods.Items))
	}
}
