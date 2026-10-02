package executor

import (
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Warm-worker pod labels (ADR 0058 N1b2b). The warm-pool reconciler lists and
// counts warm pods per dag_version by selecting on these, so they are the stable
// contract between BuildWarmPod (writes them) and the reconciler (reads them).
const (
	// warmWorkerLabelKey marks a pod as a warm worker; its value is always "true".
	// It is the selector the reconciler lists the warm fleet by.
	warmWorkerLabelKey = "leoflow.io/warm-worker"
	warmWorkerLabelVal = "true"
	// warmDagVersionLabelKey names the dag_version pool a warm worker serves, so
	// the reconciler can group and reconcile counts per version.
	warmDagVersionLabelKey = "leoflow.io/dag-version-id"
	// warmTenantLabelKey names the tenant that owns a warm worker's dag_version, so
	// the reconciler can count a tenant's whole warm footprint — including pods of a
	// draining/inactive version that are absent from the active targets — and
	// enforce the per-tenant aggregate cap (M4). It is stamped from
	// WarmPodSpec.TenantID and read back into WarmPodInfo.TenantID by ListWarmPods.
	warmTenantLabelKey = "leoflow.io/tenant-id"
	// warmAnchorLabelKey marks a ConfigMap as a warm-pool GC anchor (ADR 0058 D11);
	// its value is always "true". Together with warmDagVersionLabelKey it makes an
	// anchor discoverable (which version it anchors), so an operator can see the
	// GC-owner ConfigMaps the reconciler creates alongside the warm fleet.
	warmAnchorLabelKey = "leoflow.io/warm-anchor"
	warmAnchorLabelVal = "true"
)

// Exported warm-worker label contract (ADR 0058 D1/D2). The token-exchange
// resolver reads these off a reviewed pod to decide whether it is a warm worker
// (and which pool/tenant it serves), so the SAME label BuildWarmPod stamps is the
// one the resolver keys on — single-sourced, no drift. They alias the package's
// stamping constants above.
const (
	// WarmWorkerLabel marks a pod as a warm worker; WarmWorkerLabelValue is its
	// value. A reviewed pod carrying this label is resolved to a warm-worker
	// (control-channel-only) identity rather than a task instance.
	WarmWorkerLabel      = warmWorkerLabelKey
	WarmWorkerLabelValue = warmWorkerLabelVal
	// WarmDagVersionLabel names the dag_version pool a warm worker serves.
	WarmDagVersionLabel = warmDagVersionLabelKey
	// WarmTenantLabel names the tenant that owns a warm worker's dag_version.
	WarmTenantLabel = warmTenantLabelKey
)

// WarmPodSpec is everything BuildWarmPod needs to build one long-lived warm-worker
// pod: which dag_version pool it serves, the image (the DAG's image — a warm
// worker runs the agent in warm mode and forks a child per attempt), the
// control-plane connection, and how its BOOTSTRAP credential reaches it (the same
// transport a task pod uses). It carries NO task instance: the per-attempt token
// and task identity arrive in-band over AwaitAssignment, never on this spec.
type WarmPodSpec struct {
	DagVersionID    string
	Image           string
	ImagePullPolicy string
	Namespace       string

	// TenantID is the tenant that owns this dag_version. It is stamped onto the pod
	// as the leoflow.io/tenant-id label so the reconciler can attribute the pod to
	// its tenant for the per-tenant aggregate warm-pod cap (M4), even after the
	// version stops being active (the label outlives the active target).
	TenantID string

	// ControlPlaneAddr / AgentTLSCAConfigMap mirror the task-pod connection knobs:
	// where the agent dials and (when set) the CA ConfigMap it verifies the server
	// cert against. Reused verbatim so a warm worker connects exactly as a task pod.
	ControlPlaneAddr    string
	AgentTLSCAConfigMap string

	// ServiceAccount is the ServiceAccount the warm pod runs as — the operator's
	// default task ServiceAccount (executor.task_service_account). A warm worker is
	// pre-created and generic, so it can only carry the operator-wide default, not a
	// per-DAG execution.service_account; empty leaves it on the namespace default SA.
	// Without this a task placed on a warm worker runs as the default SA and keyless
	// secret resolution breaks (#2, warm-pool path).
	ServiceAccount string

	// Bootstrap-credential transport (ADR 0055 Fix #3), mirroring Request's token
	// fields: BootstrapToken rides plaintext on the env-var default; the exchange
	// transport keeps it off the pod and projects a ServiceAccount token instead.
	// The credential authorizes only Register + AwaitAssignment (no secret access —
	// secrets resolve per-attempt against the in-band attempt token), so a warm
	// worker never stamps a task-instance identity annotation.
	BootstrapToken              string
	AgentTokenTransport         string
	AgentTokenAudience          string
	AgentTokenExpirationSeconds int64
	AgentTokenSecretName        string
	AgentTokenSecretKey         string

	// Self-lifecycle caps the warm agent enforces on ITSELF (ADR 0058 D9/D10/D6/H3),
	// carried in-band as env so a worker can drain, idle-recycle, and hard-bound a
	// wedged attempt without any control-plane round trip. Each is zero when the
	// operator disables that bound; the agent treats zero/unset as "no bound".
	//
	//   - MaxAttemptsPerWorker: drain after this many completed attempts (D9/D10).
	//   - MaxWorkerLifetimeSeconds: drain past this wall-clock age (D9/D10).
	//   - WorkerIdleTTLSeconds: idle-recycle after this long awaiting work (D6).
	//   - AttemptWatchdogSeconds: hard per-attempt ceiling, independent of the task's
	//     execution_timeout, so a no-timeout wedge cannot pin the slot (H3). main.go
	//     sets it to auth.max_attempt_credential_lifetime — an attempt can never
	//     validly outlive its credential ceiling.
	MaxAttemptsPerWorker     int
	MaxWorkerLifetimeSeconds int64
	WorkerIdleTTLSeconds     int64
	AttemptWatchdogSeconds   int64

	// PodSecurity carries the same container/pod hardening choices as a task pod.
	PodSecurity PodSecurity

	// ReadOnlyRootFilesystem is the warm isolation mode (X3.2,
	// execution.warm_read_only_root_filesystem). It forces a read-only root on the
	// warm container whatever PodSecurity says, mounts the writable /tmp emptyDir,
	// and tells the agent (warmAttemptHomeEnv) to give each attempt its own HOME
	// and XDG dirs inside the scratch it wipes, and to sweep /tmp between
	// attempts. On a writable root a file one attempt plants on the image would be
	// executed by the next attempt on this worker.
	ReadOnlyRootFilesystem bool

	// AnchorName / AnchorUID identify the per-dag-version GC-anchor ConfigMap this
	// warm pod is owned by (ADR 0058 D11). When BOTH are set, BuildWarmPod stamps an
	// ownerReference to the anchor, so on control-plane loss / namespace teardown the
	// pod is cascade-GC'd with the anchor — the one orphan class the reconciler (as
	// deleter) cannot cover. When either is empty (off-cluster/pre-anchor builds and
	// tests) the pod is built bare, exactly as before D11.
	AnchorName string
	AnchorUID  types.UID

	// Labels / Annotations are operator-declared metadata overlaid onto the pod;
	// Dexaflow's own warm-worker labels always win a collision (see mergeMetadata).
	Labels      map[string]string
	Annotations map[string]string
}

// BuildWarmPod constructs the pod spec for one warm worker. It reuses BuildPod's
// machinery — the token transport, the CA mount, the control-plane env, and the
// security contexts — but for a LONG-LIVED worker bound to a dag_version rather
// than a single task attempt. The differences from a task pod are deliberate:
//
//   - Env: LEOFLOW_WARM_WORKER=1 + LEOFLOW_DAG_VERSION_ID select the agent's warm
//     loop, and there is NO task env (no task-instance id, no per-attempt token, no
//     durable-outcome path) — a warm worker has no task until an attempt is pushed
//     to it in-band.
//   - Labels: a stable warm-worker label set (warmWorkerLabelKey +
//     warmDagVersionLabelKey) so the reconciler can list, count, and reap warm
//     pods per version. None of the task identity labels are set.
//   - RestartPolicy Never: a warm worker that exits (drain, idle recycle, or a
//     crash) is REPLACED by the reconciler with a fresh pod that re-registers
//     cleanly, rather than restarted in place with stale in-container state. This
//     is the clean model for the reconciler-owned lifecycle; the D9 lifetime/
//     attempt caps and the idle-TTL recycle that drive drains are deferred to N1d.
//
// It is pure (modulo the random name suffix) and unit-tested independently of any
// cluster. It bakes NO task token in. When the spec carries a GC anchor (D11) it
// stamps an ownerReference to that anchor ConfigMap so the pod is cascade-GC'd on
// external teardown; without an anchor it builds a bare pod, unchanged.
func BuildWarmPod(spec WarmPodSpec) *corev1.Pod {
	if spec.ReadOnlyRootFilesystem {
		spec.PodSecurity.ReadOnlyRootFilesystem = true
	}
	pullPolicy := corev1.PullIfNotPresent
	if spec.ImagePullPolicy != "" {
		pullPolicy = corev1.PullPolicy(spec.ImagePullPolicy)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            warmPodName(spec.DagVersionID),
			Labels:          warmPodLabels(spec),
			Annotations:     map[string]string{},
			OwnerReferences: warmAnchorOwnerRefs(spec),
		},
		Spec: corev1.PodSpec{
			RestartPolicy:   corev1.RestartPolicyNever,
			SecurityContext: buildPodSecurityContext(spec.PodSecurity),
			// The warm agent authenticates to the control plane with its bootstrap
			// token over gRPC and never calls the Kubernetes API, so a mounted
			// ServiceAccount token is a credential handed to code for no reason.
			AutomountServiceAccountToken: ptr(false),
			Containers: []corev1.Container{{
				Name:            warmContainerName,
				Image:           spec.Image,
				ImagePullPolicy: pullPolicy,
				Env:             warmPodEnv(spec),
				SecurityContext: buildSecurityContext(spec.PodSecurity),
			}},
		},
	}
	mergeMetadata(pod.Labels, spec.Labels)
	mergeMetadata(pod.Annotations, spec.Annotations)
	if spec.ServiceAccount != "" {
		// Run the warm worker as the operator's default task ServiceAccount so a task
		// placed on it resolves keyless secrets exactly as a dedicated pod does (#2).
		// AutomountServiceAccountToken stays false: keyless (IRSA / Workload Identity)
		// works via the cloud webhook's projected token bound to the SA, not the
		// Kubernetes API token — same as the dedicated task pod.
		pod.Spec.ServiceAccountName = spec.ServiceAccount
	}
	// A read-only rootfs otherwise leaves the warm agent's os.MkdirTemp scratch
	// (under /tmp) with nowhere to write, so it dies before registering and the
	// reconciler replaces it forever (issue #741). Give it a writable /tmp
	// emptyDir; warmPodEnv points TMPDIR at it so the scratch lands there.
	mountWritableTmp(pod, spec.PodSecurity)
	mountWarmAgentTLSCA(pod, spec)
	// Bootstrap-credential transport, shared with the task pod. identity is nil: a
	// warm worker registers under its bearer's own identity, so no task-instance
	// identity annotation is stamped even under the exchange transport.
	mountAgentToken(pod, spec.agentToken())
	return pod
}

// warmPodLabels builds the warm worker's stable label set: the warm-worker marker
// the reconciler lists by, the dag_version pool it serves, and — when the spec
// carries one — the owning tenant, so the reconciler can attribute the pod to its
// tenant for the per-tenant aggregate cap (M4) even once the version is inactive.
// TenantID is omitted when empty rather than stamped blank, so an "absent" tenant
// label means exactly "pre-label pod" to the reconciler.
func warmPodLabels(spec WarmPodSpec) map[string]string {
	labels := map[string]string{
		warmWorkerLabelKey:     warmWorkerLabelVal,
		warmDagVersionLabelKey: sanitizeLabel(spec.DagVersionID),
	}
	if spec.TenantID != "" {
		labels[warmTenantLabelKey] = sanitizeLabel(spec.TenantID)
	}
	return labels
}

// warmAnchorOwnerRefs builds the pod's ownerReference to its dag_version's GC
// anchor (ADR 0058 D11) — but ONLY when the spec carries BOTH the anchor name and
// UID. The controller/blockOwnerDeletion pair makes the anchor the pod's GC owner,
// so deleting the anchor cascade-deletes the pod (the teardown backstop). When
// either field is empty the pod is built bare (no ownerReference), exactly as
// pre-D11: off-cluster/pre-anchor builds and unit tests that do not set an anchor
// get today's ephemeral bare warm pod.
func warmAnchorOwnerRefs(spec WarmPodSpec) []metav1.OwnerReference {
	if spec.AnchorName == "" || spec.AnchorUID == "" {
		return nil
	}
	return []metav1.OwnerReference{{
		APIVersion:         "v1",
		Kind:               "ConfigMap",
		Name:               spec.AnchorName,
		UID:                spec.AnchorUID,
		Controller:         ptr(true),
		BlockOwnerDeletion: ptr(true),
	}}
}

// warmAnchorName is the DNS-safe name of a dag_version's GC-anchor ConfigMap
// (ADR 0058 D11): leoflow-pool-<sanitized dag_version>, truncated to the 253-char
// object-name limit. It is deterministic per version so EnsureWarmAnchor is
// idempotent (a re-create hits AlreadyExists) and DeleteWarmAnchor can address the
// anchor by version alone.
func warmAnchorName(dagVersionID string) string {
	name := "leoflow-pool-" + sanitizeLabel(dagVersionID)
	if len(name) > 253 {
		name = strings.TrimRight(name[:253], "-")
	}
	return name
}

// warmContainerName is the warm worker's single container.
const warmContainerName = "warm-worker"

// agentToken projects a WarmPodSpec's token fields into the shared transport
// carrier. identity is left nil so mountAgentToken projects the token (under
// exchange) without stamping a task-instance identity annotation.
func (spec WarmPodSpec) agentToken() agentToken {
	return agentToken{
		transport:         spec.AgentTokenTransport,
		token:             spec.BootstrapToken,
		secretName:        spec.AgentTokenSecretName,
		secretKey:         spec.AgentTokenSecretKey,
		audience:          spec.AgentTokenAudience,
		expirationSeconds: spec.AgentTokenExpirationSeconds,
		identity:          nil,
	}
}

// warmPodEnv builds the warm worker's container env: the control-plane address,
// the bootstrap-token transport env (reused from the task path), the warm-mode
// selectors, and — when a CA is configured — the TLS env. It deliberately omits
// every task-specific var.
func warmPodEnv(spec WarmPodSpec) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, 12)
	env = append(env, corev1.EnvVar{Name: "LEOFLOW_CONTROL_PLANE_ADDR", Value: spec.ControlPlaneAddr})
	tok := spec.agentToken()
	env = append(env, tok.env()...)
	env = append(env, tok.pathEnv()...)
	// Select the agent's warm-worker loop for this dag_version pool, and inject the
	// worker's OWN pod name via the Kubernetes downward API (ADR 0058 N1d-a1). The
	// agent forwards LEOFLOW_POD_NAME in WorkerRegister.pod_name; the control plane
	// binds a started attempt to it (warm_worker_id) so a later failover reaper can
	// match the attempt against the live warm-pod set. The pod name is not known at
	// build time (it carries a random suffix), so it must come from the downward
	// API's metadata.name rather than a literal value.
	// Warm-mode selectors + the pod's own name via the downward API, then the
	// self-lifecycle caps (ADR 0058 D9/D10/D6/H3): the worker reads these and bounds
	// its own life — draining after N attempts or a lifetime, idle-recycling, and
	// hard-killing a wedged attempt. The caps are emitted verbatim (zero means "no
	// bound" to the agent), so the contract is explicit on every warm pod.
	env = append(env,
		corev1.EnvVar{Name: "LEOFLOW_WARM_WORKER", Value: "1"},
		corev1.EnvVar{Name: "LEOFLOW_DAG_VERSION_ID", Value: spec.DagVersionID},
		corev1.EnvVar{
			Name: "LEOFLOW_POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			},
		},
		corev1.EnvVar{Name: "LEOFLOW_MAX_ATTEMPTS_PER_WORKER", Value: strconv.Itoa(spec.MaxAttemptsPerWorker)},
		corev1.EnvVar{Name: "LEOFLOW_MAX_WORKER_LIFETIME_SECONDS", Value: strconv.FormatInt(spec.MaxWorkerLifetimeSeconds, 10)},
		corev1.EnvVar{Name: "LEOFLOW_WORKER_IDLE_TTL_SECONDS", Value: strconv.FormatInt(spec.WorkerIdleTTLSeconds, 10)},
		corev1.EnvVar{Name: "LEOFLOW_ATTEMPT_WATCHDOG_SECONDS", Value: strconv.FormatInt(spec.AttemptWatchdogSeconds, 10)},
	)
	if spec.AgentTLSCAConfigMap != "" {
		env = append(env,
			corev1.EnvVar{Name: "LEOFLOW_AGENT_INSECURE", Value: "false"},
			corev1.EnvVar{Name: "LEOFLOW_AGENT_TLS_CA", Value: agentCADir + "/" + agentCAFile},
		)
	}
	// On a read-only rootfs the warm agent's scratch (os.MkdirTemp("", ...)) would
	// resolve onto the read-only filesystem and fail before the worker registers
	// (issue #741). BuildWarmPod mounts a writable emptyDir at /tmp; point TMPDIR at
	// it so os.MkdirTemp — which honors $TMPDIR — lands the scratch on the emptyDir.
	if spec.PodSecurity.ReadOnlyRootFilesystem {
		env = append(env, corev1.EnvVar{Name: "TMPDIR", Value: writableTmpMountPath})
	}
	if spec.ReadOnlyRootFilesystem {
		env = append(env, corev1.EnvVar{Name: warmAttemptHomeEnv, Value: "1"})
	}
	return env
}

// warmAttemptHomeEnv tells the warm agent its root filesystem is read only by
// design (X3.2), so it must give each attempt a HOME and XDG dirs inside its
// wiped scratch and sweep the shared /tmp between attempts.
const warmAttemptHomeEnv = "LEOFLOW_WARM_ATTEMPT_HOME"

// mountWarmAgentTLSCA mounts the CA ConfigMap (when configured) into the warm pod
// so the agent can verify the control plane's TLS cert, mirroring the task pod's
// mountAgentTLSCA (the matching env is set in warmPodEnv).
func mountWarmAgentTLSCA(pod *corev1.Pod, spec WarmPodSpec) {
	if spec.AgentTLSCAConfigMap == "" {
		return
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: agentCAVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: spec.AgentTLSCAConfigMap},
			},
		},
	})
	c := &pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: agentCAVolumeName, MountPath: agentCADir, ReadOnly: true})
}

// warmPodName builds a DNS-safe, collision-resistant name for a warm pod:
// leoflow-warm-<dag-version>-<rand>, truncated to the 63-char label limit like
// podName. The random suffix lets the reconciler create many workers for one
// version without name clashes.
func warmPodName(dagVersionID string) string {
	suffix := randSuffix()
	base := "leoflow-warm-" + sanitizeLabel(dagVersionID)
	maxBase := 63 - len(suffix) - 1
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	return base + "-" + suffix
}
