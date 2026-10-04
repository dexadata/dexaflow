package executor

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// warmEnvMap projects the warm pod's single container env into a name->value map,
// for value assertions. Env vars sourced from elsewhere (SecretKeyRef) have an
// empty value here.
func warmEnvMap(pod *corev1.Pod) map[string]string {
	m := map[string]string{}
	for _, e := range pod.Spec.Containers[0].Env {
		m[e.Name] = e.Value
	}
	return m
}

// warmEnvVar returns the full EnvVar named name from the warm pod's container, so
// a test can inspect ValueFrom (downward-API sources carry no literal Value).
func warmEnvVar(pod *corev1.Pod, name string) (corev1.EnvVar, bool) {
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == name {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

// TestBuildWarmPodInjectsPodNameViaDownwardAPI locks the durable-binding key
// plumbing (ADR 0058 N1d-a1): the worker learns its OWN pod name through the
// Kubernetes downward API as LEOFLOW_POD_NAME (fieldRef metadata.name), since the
// name — carrying a random suffix — is unknown at build time. The agent forwards
// it in WorkerRegister.pod_name so the control plane can bind a started attempt
// to it as warm_worker_id.
func TestBuildWarmPodInjectsPodNameViaDownwardAPI(t *testing.T) {
	pod := BuildWarmPod(baseWarmSpec())

	ev, ok := warmEnvVar(pod, "LEOFLOW_POD_NAME")
	if !ok {
		t.Fatal("warm pod must carry LEOFLOW_POD_NAME env for the durable binding key")
	}
	if ev.Value != "" {
		t.Errorf("LEOFLOW_POD_NAME must be sourced from the downward API, not a literal value (got %q)", ev.Value)
	}
	if ev.ValueFrom == nil || ev.ValueFrom.FieldRef == nil {
		t.Fatalf("LEOFLOW_POD_NAME must use valueFrom.fieldRef, got %+v", ev.ValueFrom)
	}
	if got := ev.ValueFrom.FieldRef.FieldPath; got != "metadata.name" {
		t.Errorf("LEOFLOW_POD_NAME fieldRef.fieldPath = %q, want metadata.name", got)
	}
}

// baseWarmSpec is a minimal env-var-transport warm spec used across the cases.
func baseWarmSpec() WarmPodSpec {
	return WarmPodSpec{
		DagVersionID:     "dv-abc",
		Image:            "reg.example/dag:v3",
		Namespace:        "leoflow",
		ControlPlaneAddr: "cp:9000",
		BootstrapToken:   "boot-tok",
	}
}

// TestBuildWarmPodWarmEnvAndLabels locks the warm-worker pod shape (ADR 0058
// N1b2b): the agent is told to run in warm mode for a dag_version, the pod is
// labeled so the reconciler can list/count it, and it carries NO task-specific
// env (a warm worker has no task instance until an attempt arrives in-band).
func TestBuildWarmPodWarmEnvAndLabels(t *testing.T) {
	pod := BuildWarmPod(baseWarmSpec())

	env := warmEnvMap(pod)
	if env["LEOFLOW_WARM_WORKER"] != "1" {
		t.Errorf("LEOFLOW_WARM_WORKER = %q, want 1", env["LEOFLOW_WARM_WORKER"])
	}
	if env["LEOFLOW_DAG_VERSION_ID"] != "dv-abc" {
		t.Errorf("LEOFLOW_DAG_VERSION_ID = %q, want dv-abc", env["LEOFLOW_DAG_VERSION_ID"])
	}
	if env["LEOFLOW_CONTROL_PLANE_ADDR"] != "cp:9000" {
		t.Errorf("LEOFLOW_CONTROL_PLANE_ADDR = %q, want cp:9000", env["LEOFLOW_CONTROL_PLANE_ADDR"])
	}

	// No task-specific env: the warm worker has no task instance, try number, or
	// per-attempt outcome path — those arrive in-band per attempt (AwaitAssignment).
	for _, k := range []string{"LEOFLOW_TASK_INSTANCE_ID", "LEOFLOW_TERMINATION_LOG_PATH"} {
		if _, ok := env[k]; ok {
			t.Errorf("warm pod must not carry task env %s (got %q)", k, env[k])
		}
	}

	// Image and warm-worker labels the reconciler selects on.
	if got := pod.Spec.Containers[0].Image; got != "reg.example/dag:v3" {
		t.Errorf("image = %q, want reg.example/dag:v3", got)
	}
	if pod.Labels[warmWorkerLabelKey] != "true" {
		t.Errorf("label %s = %q, want true", warmWorkerLabelKey, pod.Labels[warmWorkerLabelKey])
	}
	if pod.Labels[warmDagVersionLabelKey] != "dv-abc" {
		t.Errorf("label %s = %q, want dv-abc", warmDagVersionLabelKey, pod.Labels[warmDagVersionLabelKey])
	}
	// No per-task identity labels leak onto a warm pod.
	for _, k := range []string{"leoflow.io/task-id", "leoflow.io/run-id", "leoflow.io/try-number"} {
		if _, ok := pod.Labels[k]; ok {
			t.Errorf("warm pod must not carry task label %s", k)
		}
	}

	// No tenant label when the spec carries none: an absent tenant label means
	// exactly "pre-label pod" to the reconciler (M4).
	if _, ok := pod.Labels[warmTenantLabelKey]; ok {
		t.Errorf("warm pod must not carry a blank tenant label when TenantID is unset (got %q)", pod.Labels[warmTenantLabelKey])
	}

	// A warm worker that exits on drain must be REPLACED by the reconciler, not
	// restarted in place (a fresh pod re-registers cleanly); RestartPolicy Never.
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy = %q, want Never", pod.Spec.RestartPolicy)
	}
	// The agent authenticates over gRPC and never calls the Kubernetes API.
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("AutomountServiceAccountToken must be explicitly false")
	}
}

// TestBuildWarmPodStampsTenantLabel locks the M4 tenant attribution: when the spec
// carries a TenantID, BuildWarmPod stamps it as the leoflow.io/tenant-id label so
// the reconciler can attribute the pod to its tenant for the per-tenant aggregate
// cap — even after the version stops being active (the label outlives the target).
func TestBuildWarmPodStampsTenantLabel(t *testing.T) {
	spec := baseWarmSpec()
	spec.TenantID = "tenant-xyz"
	pod := BuildWarmPod(spec)
	if got := pod.Labels[warmTenantLabelKey]; got != "tenant-xyz" {
		t.Errorf("label %s = %q, want tenant-xyz", warmTenantLabelKey, got)
	}
}

// TestBuildWarmPodCarriesSelfLifecycleCaps locks the four self-lifecycle caps the
// warm agent enforces on itself (ADR 0058 D9/D10/D6/H3): the attempt count cap, the
// wall-clock lifetime cap (seconds), the idle-TTL (seconds), and the per-attempt
// watchdog (seconds). BuildWarmPod injects them as env so the worker can drain,
// idle-recycle, and hard-bound a wedged attempt without any control-plane round trip.
func TestBuildWarmPodCarriesSelfLifecycleCaps(t *testing.T) {
	spec := baseWarmSpec()
	spec.MaxAttemptsPerWorker = 50
	spec.MaxWorkerLifetimeSeconds = 3600
	spec.WorkerIdleTTLSeconds = 300
	spec.AttemptWatchdogSeconds = 86400

	env := warmEnvMap(BuildWarmPod(spec))
	for _, tc := range []struct{ key, want string }{
		{"LEOFLOW_MAX_ATTEMPTS_PER_WORKER", "50"},
		{"LEOFLOW_MAX_WORKER_LIFETIME_SECONDS", "3600"},
		{"LEOFLOW_WORKER_IDLE_TTL_SECONDS", "300"},
		{"LEOFLOW_ATTEMPT_WATCHDOG_SECONDS", "86400"},
	} {
		if env[tc.key] != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, env[tc.key], tc.want)
		}
	}
}

// TestBuildWarmPodEnvVarTransport locks the default (env-var) bootstrap-token
// transport: the minted worker JWT rides as the plaintext LEOFLOW_AGENT_TOKEN,
// exactly how a task pod carries its token on the env-var path.
func TestBuildWarmPodEnvVarTransport(t *testing.T) {
	pod := BuildWarmPod(baseWarmSpec())
	env := warmEnvMap(pod)
	if env["LEOFLOW_AGENT_TOKEN"] != "boot-tok" {
		t.Errorf("LEOFLOW_AGENT_TOKEN = %q, want boot-tok", env["LEOFLOW_AGENT_TOKEN"])
	}
	if _, ok := env["LEOFLOW_AGENT_TOKEN_PATH"]; ok {
		t.Error("env-var transport must not set LEOFLOW_AGENT_TOKEN_PATH")
	}
	if _, ok := pod.Annotations[AgentIdentityAnnotation]; ok {
		t.Error("env-var transport must not stamp the exchange identity annotation")
	}
}

// TestBuildWarmPodExchangeTransport locks the exchange bootstrap transport reuse:
// the plaintext token is kept OFF the pod, a ServiceAccount token is projected and
// pointed at by LEOFLOW_AGENT_TOKEN_PATH, and — crucially — NO task-instance
// identity annotation is stamped (a warm worker registers under its bearer's
// identity, not a task instance).
func TestBuildWarmPodExchangeTransport(t *testing.T) {
	spec := baseWarmSpec()
	spec.AgentTokenTransport = agentTransportExchange
	pod := BuildWarmPod(spec)

	env := warmEnvMap(pod)
	if _, ok := env["LEOFLOW_AGENT_TOKEN"]; ok {
		t.Error("exchange: plaintext LEOFLOW_AGENT_TOKEN must not be on the warm pod")
	}
	if env["LEOFLOW_AGENT_TOKEN_TRANSPORT"] != agentTransportExchange {
		t.Errorf("LEOFLOW_AGENT_TOKEN_TRANSPORT = %q, want exchange", env["LEOFLOW_AGENT_TOKEN_TRANSPORT"])
	}
	if env["LEOFLOW_AGENT_TOKEN_PATH"] != agentTokenMountDir+"/"+agentTokenFile {
		t.Errorf("LEOFLOW_AGENT_TOKEN_PATH = %q, want %q", env["LEOFLOW_AGENT_TOKEN_PATH"], agentTokenMountDir+"/"+agentTokenFile)
	}
	if _, ok := pod.Annotations[AgentIdentityAnnotation]; ok {
		t.Error("exchange: a warm worker must NOT carry the task-instance identity annotation")
	}
	// The projected token volume is mounted.
	var found bool
	for _, v := range pod.Spec.Volumes {
		if v.Name == agentTokenVolumeName {
			found = true
		}
	}
	if !found {
		t.Error("exchange: projected agent-token volume must be mounted")
	}
}

// TestBuildWarmPodStampsAnchorOwnerReference locks the D11 GC anchor: when the spec
// carries BOTH an anchor name and UID, BuildWarmPod stamps a single controlling
// ownerReference to the anchor ConfigMap (Controller + BlockOwnerDeletion true), so
// deleting the anchor cascade-GCs the pod on external teardown.
func TestBuildWarmPodStampsAnchorOwnerReference(t *testing.T) {
	spec := baseWarmSpec()
	spec.AnchorName = "leoflow-pool-dv-abc"
	spec.AnchorUID = types.UID("anchor-uid-xyz")
	pod := BuildWarmPod(spec)

	if len(pod.OwnerReferences) != 1 {
		t.Fatalf("ownerReferences = %d, want exactly 1 (the GC anchor)", len(pod.OwnerReferences))
	}
	o := pod.OwnerReferences[0]
	if o.APIVersion != "v1" || o.Kind != "ConfigMap" {
		t.Errorf("ownerReference APIVersion/Kind = %s/%s, want v1/ConfigMap", o.APIVersion, o.Kind)
	}
	if o.Name != "leoflow-pool-dv-abc" || o.UID != types.UID("anchor-uid-xyz") {
		t.Errorf("ownerReference Name/UID = %s/%s, want leoflow-pool-dv-abc/anchor-uid-xyz", o.Name, o.UID)
	}
	if o.Controller == nil || !*o.Controller {
		t.Error("ownerReference Controller must be true (the anchor is the pod's controlling owner)")
	}
	if o.BlockOwnerDeletion == nil || !*o.BlockOwnerDeletion {
		t.Error("ownerReference BlockOwnerDeletion must be true")
	}
}

// TestBuildWarmPodBarePodWhenAnchorUnset locks the do-no-harm default: with no
// anchor (or only one of name/UID) BuildWarmPod produces a BARE pod with no
// ownerReference — exactly today's ephemeral warm pod. This keeps off-cluster /
// pre-anchor builds and tests that don't set an anchor unchanged.
func TestBuildWarmPodBarePodWhenAnchorUnset(t *testing.T) {
	// Neither set.
	if refs := BuildWarmPod(baseWarmSpec()).OwnerReferences; len(refs) != 0 {
		t.Errorf("bare spec produced %d ownerReferences, want 0 (unchanged bare pod)", len(refs))
	}
	// Only name set (no UID): still bare — a half-set anchor must not stamp a
	// dangling owner with an empty UID.
	nameOnly := baseWarmSpec()
	nameOnly.AnchorName = "leoflow-pool-dv-abc"
	if refs := BuildWarmPod(nameOnly).OwnerReferences; len(refs) != 0 {
		t.Errorf("name-only spec produced %d ownerReferences, want 0", len(refs))
	}
	// Only UID set (no name): still bare.
	uidOnly := baseWarmSpec()
	uidOnly.AnchorUID = types.UID("anchor-uid-xyz")
	if refs := BuildWarmPod(uidOnly).OwnerReferences; len(refs) != 0 {
		t.Errorf("uid-only spec produced %d ownerReferences, want 0", len(refs))
	}
}

// TestBuildWarmPodMountsCA locks CA-mount reuse: when a CA ConfigMap is set the
// warm pod mounts it and selects TLS via the same env the task pod uses.
func TestBuildWarmPodMountsCA(t *testing.T) {
	spec := baseWarmSpec()
	spec.AgentTLSCAConfigMap = "leoflow-ca"
	pod := BuildWarmPod(spec)

	env := warmEnvMap(pod)
	if env["LEOFLOW_AGENT_INSECURE"] != "false" {
		t.Errorf("LEOFLOW_AGENT_INSECURE = %q, want false", env["LEOFLOW_AGENT_INSECURE"])
	}
	if env["LEOFLOW_AGENT_TLS_CA"] != agentCADir+"/"+agentCAFile {
		t.Errorf("LEOFLOW_AGENT_TLS_CA = %q, want %q", env["LEOFLOW_AGENT_TLS_CA"], agentCADir+"/"+agentCAFile)
	}
	var found bool
	for _, v := range pod.Spec.Volumes {
		if v.Name == agentCAVolumeName {
			found = true
		}
	}
	if !found {
		t.Error("CA ConfigMap volume must be mounted")
	}
}

// TestBuildWarmPodMountsWritableTmpForReadOnlyRoot locks the fix for issue #741:
// a warm worker built with a read-only root filesystem must still have a writable
// /tmp, or the warm agent dies at os.MkdirTemp before it can register (RestartPolicy
// Never turns that into a replacement loop that takes the whole pool down). The
// restricted-profile pattern is ro rootfs + a writable emptyDir at /tmp, and the
// agent's scratch (TMPDIR) must resolve onto that emptyDir.
func TestBuildWarmPodMountsWritableTmpForReadOnlyRoot(t *testing.T) {
	spec := baseWarmSpec()
	spec.PodSecurity.ReadOnlyRootFilesystem = true
	pod := BuildWarmPod(spec)

	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == writableTmpVolumeName {
			vol = &pod.Spec.Volumes[i]
		}
	}
	if vol == nil {
		t.Fatalf("a writable /tmp emptyDir volume must be mounted for a read-only rootfs warm worker: %+v", pod.Spec.Volumes)
	}
	if vol.EmptyDir == nil {
		t.Errorf("the /tmp volume must be an emptyDir, got %+v", vol.VolumeSource)
	}

	c := pod.Spec.Containers[0]
	var mount *corev1.VolumeMount
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == writableTmpVolumeName {
			mount = &c.VolumeMounts[i]
		}
	}
	if mount == nil {
		t.Fatalf("the /tmp emptyDir must be mounted into the container: %+v", c.VolumeMounts)
	}
	if mount.MountPath != writableTmpMountPath {
		t.Errorf("/tmp mount path = %q, want %q", mount.MountPath, writableTmpMountPath)
	}
	if mount.ReadOnly {
		t.Error("the /tmp mount must be writable, not read-only")
	}

	// The warm agent's scratch is created with os.MkdirTemp(""), which honors TMPDIR
	// (falling back to /tmp). Point TMPDIR at the writable emptyDir so the scratch
	// resolves onto it rather than onto the read-only rootfs.
	if got := warmEnvMap(pod)["TMPDIR"]; got != writableTmpMountPath {
		t.Errorf("TMPDIR = %q, want %q so the warm scratch resolves onto the writable emptyDir", got, writableTmpMountPath)
	}
}

// TestBuildWarmPodNoWritableTmpWhenRootWritable keeps the default (writable
// rootfs) warm pod byte-identical: the /tmp emptyDir and TMPDIR are the ro-rootfs
// escape hatch only, so a default warm pod must not sprout either.
func TestBuildWarmPodNoWritableTmpWhenRootWritable(t *testing.T) {
	pod := BuildWarmPod(baseWarmSpec())

	for _, v := range pod.Spec.Volumes {
		if v.Name == writableTmpVolumeName {
			t.Errorf("no /tmp emptyDir must be mounted when the rootfs is writable: %+v", pod.Spec.Volumes)
		}
	}
	if _, ok := warmEnvVar(pod, "TMPDIR"); ok {
		t.Error("TMPDIR must not be set when the rootfs is writable")
	}
}

// A warm pod runs as the operator's default task ServiceAccount when set, so a
// task placed on a warm worker resolves keyless secrets like a dedicated pod (#2).
// Empty leaves it on the namespace default SA (today's behavior).
func TestBuildWarmPodServiceAccount(t *testing.T) {
	spec := baseWarmSpec()
	spec.ServiceAccount = "leoflow-task"
	if got := BuildWarmPod(spec).Spec.ServiceAccountName; got != "leoflow-task" {
		t.Errorf("warm pod ServiceAccountName = %q, want leoflow-task", got)
	}
	if got := BuildWarmPod(baseWarmSpec()).Spec.ServiceAccountName; got != "" {
		t.Errorf("no default SA → empty ServiceAccountName, got %q", got)
	}
}

// TestBuildWarmPodSetsResources is X4: a warm worker is sized like the dedicated
// pod a task without its own resources would get, request and limit, so a task
// placed on it is neither BestEffort where it would be Guaranteed nor unbounded
// where it would be limited.
func TestBuildWarmPodSetsResources(t *testing.T) {
	spec := baseWarmSpec()
	spec.Resources = &domain.Resources{
		Requests: &domain.ResourceQuantity{CPU: "500m", Memory: "512Mi"},
		Limits:   &domain.ResourceQuantity{CPU: "500m", Memory: "512Mi"},
	}
	res := BuildWarmPod(spec).Spec.Containers[0].Resources
	for _, list := range []corev1.ResourceList{res.Requests, res.Limits} {
		if got := list[corev1.ResourceCPU]; got.String() != "500m" {
			t.Errorf("cpu = %s, want 500m", got.String())
		}
		if got := list[corev1.ResourceMemory]; got.String() != "512Mi" {
			t.Errorf("memory = %s, want 512Mi", got.String())
		}
	}

	if res := BuildWarmPod(baseWarmSpec()).Spec.Containers[0].Resources; len(res.Requests) != 0 || len(res.Limits) != 0 {
		t.Errorf("a warm pod with no resources configured must stay unsized, got %+v", res)
	}
}

// TestBuildWarmPodReadOnlyRootFilesystemIsolation locks the warm isolation mode
// (X3.2): with execution.warm_read_only_root_filesystem on, the warm container's
// root filesystem is read only even when the task-pod default leaves it writable,
// so a file one attempt plants on the image (a module on the working directory's
// sys.path, a ~/.local site-packages entry) cannot be executed by the next one.
// The writable paths left are the /tmp emptyDir and /dev/shm, and the agent is told to give each
// attempt its own HOME and XDG dirs inside the scratch it wipes between attempts.
func TestBuildWarmPodReadOnlyRootFilesystemIsolation(t *testing.T) {
	spec := baseWarmSpec()
	spec.ReadOnlyRootFilesystem = true
	pod := BuildWarmPod(spec)

	sc := pod.Spec.Containers[0].SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Fatalf("warm container must have a read-only root filesystem, got %+v", sc)
	}
	var tmpMounted bool
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.Name == writableTmpVolumeName && m.MountPath == writableTmpMountPath && !m.ReadOnly {
			tmpMounted = true
		}
	}
	if !tmpMounted {
		t.Errorf("a writable /tmp emptyDir must be mounted: %+v", pod.Spec.Containers[0].VolumeMounts)
	}
	env := warmEnvMap(pod)
	if env["TMPDIR"] != writableTmpMountPath {
		t.Errorf("TMPDIR = %q, want %q", env["TMPDIR"], writableTmpMountPath)
	}
	if env[warmAttemptHomeEnv] != "1" {
		t.Errorf("%s = %q, want 1 so the agent gives each attempt its own HOME", warmAttemptHomeEnv, env[warmAttemptHomeEnv])
	}
}

// TestBuildWarmPodWritableRootByDefault keeps the default warm pod unchanged: no
// read-only root, no per-attempt HOME, so a task that writes to its image works
// exactly as it does today.
func TestBuildWarmPodWritableRootByDefault(t *testing.T) {
	pod := BuildWarmPod(baseWarmSpec())
	if sc := pod.Spec.Containers[0].SecurityContext; sc != nil && sc.ReadOnlyRootFilesystem != nil {
		t.Errorf("default warm pod must leave readOnlyRootFilesystem unset, got %v", *sc.ReadOnlyRootFilesystem)
	}
	if _, ok := warmEnvVar(pod, warmAttemptHomeEnv); ok {
		t.Errorf("%s must not be set on a default warm pod", warmAttemptHomeEnv)
	}
}
