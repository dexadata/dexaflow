package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dexadata/dexaflow/internal/domain"
)

// KubernetesExecutor runs each task as an ephemeral pod (ADR 0002).
type KubernetesExecutor struct {
	clientset kubernetes.Interface
	namespace string
	staging   StagingStore
}

// SetStagingStore wires the metadatabase-backed staging-volume lifecycle store
// (ADR 0022). With no store set, provisioning is not recorded and GC is a no-op.
func (e *KubernetesExecutor) SetStagingStore(s StagingStore) { e.staging = s }

// NewKubernetesExecutor builds an executor creating pods in the given namespace.
func NewKubernetesExecutor(clientset kubernetes.Interface, namespace string) *KubernetesExecutor {
	if namespace == "" {
		namespace = "default"
	}
	return &KubernetesExecutor{clientset: clientset, namespace: namespace}
}

// Execute creates the task pod. The agent inside the pod reports state over gRPC.
// A dispatch failure is classified on this layer — where the apiserver's error
// types are known — into transient Backpressure (a ResourceQuota 403 or an APF
// 429) or a permanent Rejected, so the scheduler acts on the disposition without
// importing Kubernetes error types (ADR 0051 Phase 4). The cause is returned
// alongside so its text still feeds the scheduler's note/log.
func (e *KubernetesExecutor) Execute(ctx context.Context, req Request) (Disposition, error) {
	// Provision the run's shared staging PVC on first use (idempotent), before the
	// pod that mounts it (ADR 0022).
	if req.StagingClaim != "" {
		if err := e.ensureStagingClaim(ctx, req); err != nil {
			cause := fmt.Errorf("provisioning staging volume for task %s: %w", req.TaskID, err)
			return classifyDispatchError(cause), cause
		}
	}
	pod := BuildPod(req)
	if _, err := e.clientset.CoreV1().Pods(e.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		cause := fmt.Errorf("creating pod for task %s: %w", req.TaskID, err)
		return classifyDispatchError(cause), cause
	}
	return Dispatched, nil
}

// terminationMessagePath is where the task container surfaces its termination
// message. The agent writes its durable outcome record here and the reconciler
// reads it from pod status (ADR 0052); it is the Kubernetes default path, pinned
// explicitly so the contract does not rely on an implicit default.
const terminationMessagePath = "/dev/termination-log"

// BuildPod constructs the pod spec for a task instance. It is pure (modulo the
// random name suffix) and unit-tested independently of any cluster.
func BuildPod(req Request) *corev1.Pod {
	pullPolicy := corev1.PullIfNotPresent
	if req.ImagePullPolicy != "" {
		pullPolicy = corev1.PullPolicy(req.ImagePullPolicy)
	}
	// Inject gVisor and Spot configuration for Free tier
	runtimeClass := req.Execution.RuntimeClassName
	if req.Env["LEOFLOW_TENANT_PLAN"] == "free" {
		runtimeClass = ptrStr("gvisor")
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName(req),
			Labels: map[string]string{
				"leoflow.io/dag-id":     sanitizeLabel(req.DagID),
				"leoflow.io/task-id":    sanitizeLabel(req.TaskID),
				"leoflow.io/run-id":     sanitizeLabel(req.RunID),
				"leoflow.io/try-number": strconv.Itoa(req.TryNumber),
				"leoflow.io/tenant-id":  sanitizeLabel(req.TenantID),
			},
			Annotations: map[string]string{"leoflow.io/task-instance-id": req.TaskInstanceID},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			SecurityContext:               buildPodSecurityContext(req.PodSecurity),
			NodeSelector:                  req.Execution.NodeSelector,
			Tolerations:                   buildTolerationsForPlan(req.Execution.Tolerations, req.Env["LEOFLOW_TENANT_PLAN"]),
			PriorityClassName:             req.Execution.PriorityClassName,
			TerminationGracePeriodSeconds: req.Execution.TerminationGracePeriodSeconds,
			RuntimeClassName:              runtimeClass,
			TopologySpreadConstraints:     decodeStructuredSlice[corev1.TopologySpreadConstraint](req.Execution.TopologySpreadConstraints),
			Affinity:                      buildAffinity(req.Execution.Affinity),
			ResourceClaims:                decodeStructuredSlice[corev1.PodResourceClaim](req.Execution.ResourceClaims),
			// The task pod authenticates to the control plane with its own
			// per-task token over gRPC and never calls the Kubernetes API, so a
			// mounted ServiceAccount token is a credential handed to untrusted
			// code for no reason.
			AutomountServiceAccountToken: ptr(false),
			Containers: []corev1.Container{{
				Name:            "task",
				Image:           req.Image,
				ImagePullPolicy: pullPolicy,
				Env:             podEnv(req),
				Resources:       buildResources(req.Resources),
				SecurityContext: buildSecurityContext(req.PodSecurity),
				// Pin the termination-message contract explicitly rather than relying
				// on the Kubernetes default: the agent writes its durable outcome
				// record here before delivering the report, and the reconciler reads
				// it from pod status to recover a success whose report was lost (ADR
				// 0052). FallbackToLogsOnError is deliberately NOT set — it would
				// populate the message from the log tail, which is not a record.
				TerminationMessagePolicy: corev1.TerminationMessageReadFile,
				TerminationMessagePath:   terminationMessagePath,
			}},
		},
	}
	if deadline := podActiveDeadline(req); deadline > 0 {
		pod.Spec.ActiveDeadlineSeconds = &deadline
	}
	if req.Execution.ServiceAccount != "" {
		pod.Spec.ServiceAccountName = req.Execution.ServiceAccount
	}
	mergeMetadata(pod.Labels, req.Execution.Labels)
	mergeMetadata(pod.Annotations, req.Execution.Annotations)
	mountWritableTmp(pod, req.PodSecurity)
	mountStagingVolume(pod, req)
	mountAgentTLSCA(pod, req)
	mountTaskSecret(pod, req)
	mountAgentToken(pod, agentTokenOf(req))
	return pod
}

// podStartupHeadroom is how much longer than the declared execution timeout the
// pod's deadline runs, to absorb everything that happens between the two clocks
// that bound a task.
//
// The two clocks start at different moments. The kubelet counts
// activeDeadlineSeconds from pod.Status.StartTime, which is stamped BEFORE the
// image pull. The agent starts its own deadline inside its execute step, so
// everything between those two moments is spent on the kubelet's clock and none
// of it on the agent's: the image pull, the volume attach and mount, container
// start, the agent's token bootstrap and exchange, the gRPC dial, Register,
// GetTaskSpec, buildEnv (XCom fan-in and secret resolution, possibly over RPCs
// to an external backend) and the RUNNING report, whose retry has no budget of
// its own. The image pull dominates on a cold node. With the deadline set to the
// timeout itself the kubelet wins by that entire startup cost —
// systematically, on every task, not as a race.
//
// That inversion matters because of the layering: the AGENT owns the semantic
// timeout. It is what interrupts the user process at the boundary the author
// declared and reports "execution_timeout: task exceeded Xs limit", the only
// description that tells an operator a task was killed for running too long.
// The kubelet's deadline is only the backstop for an agent that can no longer
// enforce anything (crashed, wedged, partitioned), so it must never fire first:
// when it does, the pod is gone and the reason degrades to what Kubernetes
// observed from outside (see podFailureReason), which cannot name a timeout.
//
// The value is the dispatch-lost threshold, which is the window in which the
// control plane still presumes healthy startup and defers reaping a queued task.
// It is deliberately NOT a bound on startup: the dispatch-lost reaper checks the
// pod before failing anything and DEFERS while it is Pending or Running (see
// stale_queued_reap.go, #461), so a pod pulling an image for twenty minutes is
// never reaped for being slow. The threshold is where the control plane stops
// assuming a dispatch landed and starts looking — a suspicion threshold. Below
// it, nothing in this system has yet formed a view that the task is unhealthy,
// so the kubelet should not already be spending the author's execution budget.
// That is what makes the number the right size for the headroom, not any
// guarantee that startup fits inside it.
//
// It binds the compile-time DEFAULT (defaultDispatchLostThreshold), not
// ReaperConfig.DispatchLostThreshold, which the reaper reads at runtime. The two
// are the same number today because that field is not operator-tunable — nothing
// outside tests sets it — so this is a reuse of the constant, not a live
// coupling to the reaper's configured value. If a knob for it ever lands, this
// headroom will NOT follow it, and whether it should is a decision to make then:
// a longer reaper threshold widens the window in which a slow-starting pod is
// left alone, which is an argument for widening the headroom with it, but the
// pod deadline is also the operator's protection against an immortal pod.
//
// The termination grace is added on top of this rather than folded into it: the
// grace covers the tail (the agent shutting the child down and delivering its
// report after its own deadline fired), the headroom covers the head. Either
// alone leaves the kubelet able to preempt.
//
// A fixed headroom is not airtight, deliberately: a pathological image pull
// (cold node, multi-gigabyte image, a throttling registry) or a control-plane
// outage spanning the RUNNING pre-flight (which retries without a budget, by
// design) exceeds any constant, and then the kubelet fires first again and the
// timeout diagnosis is lost. The airtight variant is to anchor the AGENT's
// clock at process start instead of at execute, which makes it strictly earlier
// than the kubelet's for any non-negative headroom. That is not done here
// because it would fold the whole startup — the image pull above all, plus the
// token exchange and env build — into the author's declared budget, changing
// what execution_timeout means (it covers task execution).
const podStartupHeadroom = defaultDispatchLostThreshold

// podActiveDeadline returns the task pod's ActiveDeadlineSeconds: the user's
// declared timeout plus startup headroom and termination grace when set,
// otherwise the attempt credential ceiling as a floor, otherwise 0 (no
// deadline). The user's value is never shortened — a declared timeout is the
// author's bound to make, even one above the ceiling — and it is never merely
// matched either: the deadline outlasts it so the agent's own clock fires first
// and the operator sees the timeout named (podStartupHeadroom).
//
// The floor path takes no headroom. It is not a bound the agent races: nothing
// in the pod enforces the credential ceiling from inside, so there is no earlier
// clock to leave room for, and extending the pod past the ceiling only buys time
// in which its lapsed bearer can accomplish nothing.
//
// The floor exists because the agent's RUNNING and terminal reports retry for as
// long as the control plane is unreachable (by design: a report that gives up is
// how a succeeded task ends up marked failed), so a pod with no deadline of its
// own would survive a total control-plane outage indefinitely, pinned Running
// with its requests held and blocking node scale-down. Past the credential
// ceiling the pod can do nothing useful — its bearer stops renewing and lapses —
// so that is the natural bound; a non-positive ceiling is the operator's "no
// ceiling" and applies no floor. When the pod hits the floor the kubelet kills
// it and the reconciler recovers the durable outcome record the agent wrote
// before its first report attempt, so no result is lost, only a stuck pod.
func podActiveDeadline(req Request) int64 {
	if req.TimeoutSeconds > 0 {
		return min(int64(req.TimeoutSeconds)+int64(podStartupHeadroom/time.Second)+podDeadlineGraceTerm(req), maxPodActiveDeadline)
	}
	if req.AttemptLifetimeCeilingSeconds > 0 {
		return min(req.AttemptLifetimeCeilingSeconds, maxPodActiveDeadline)
	}
	return 0
}

// maxPodActiveDeadline is the largest activeDeadlineSeconds the apiserver will
// accept: it validates the field into [1, math.MaxInt32] at pod CREATE (#910).
//
// The clamp matters because the deadline is no longer the user's value but a
// SUM of it with the startup headroom and the grace term. A declared timeout
// near MaxInt32 is absurd on its face — 68 years, so a typo or a generated
// spec — but before the sum it still produced a valid pod, one whose deadline
// nothing would ever reach; with the sum it breaches the bound and the pod is
// rejected at CREATE, turning an absurd-but-harmless declaration into a
// permanently Rejected dispatch. Clamping keeps the harmless outcome. The
// credential-ceiling branch is clamped too: it is an operator-configured int64
// and can breach the bound without any sum at all.
const maxPodActiveDeadline = math.MaxInt32

// maxDeadlineGraceTerm caps how much of a declared terminationGracePeriodSeconds
// the pod deadline will budget (#910).
//
// The declaration is unvalidated — domain.Execution carries it as a bare *int64
// with no bound anywhere — so a DAG may ask for 3600. Added verbatim that puts
// the deadline an hour past the declared execution_timeout, and the kubelet then
// grants that same hour of SIGTERM grace again on top of the deadline, so the
// pod can outlive the timeout its author declared by about two hours. The
// backstop has to stay a backstop.
//
// The term is capped rather than dropped because the tail it budgets is real:
// once the agent's own clock fires it still has to stop the child, write the
// durable outcome record and land one report RPC, and the kubelet must not
// preempt that. But the tail is not proportional to the declared grace. The
// agent does not pass the declaration on to the child: internal/agent/exec.go
// SIGKILLs the child's whole process group immediately, whatever the DAG asked
// for (#943). What is left after that kill is bounded by the agent too — its
// execWaitDelay (10 s) caps the wait for an output pipe held open by a
// descendant that escaped the group — so the worst tail is that delay plus one
// record write and one RPC, and a minute covers it on any cluster.
const maxDeadlineGraceTerm = 60

// podDeadlineGraceTerm is the shutdown tail the pod deadline budgets for the
// agent: what the DAG declared, capped at maxDeadlineGraceTerm, else the
// Kubernetes default the kubelet applies to a pod that declares none. It is the
// deadline's grace TERM, not the pod's grace — the pod spec carries the
// declaration verbatim (see BuildPod); only the arithmetic here is bounded.
//
// The default fallback covers a declared 0 as well as an absent declaration. A
// grace-0 pod is SIGKILLed the instant the kubelet decides to stop it, so it has
// no post-deadline tail to size from the declaration at all; falling back keeps
// one arithmetic for every pod whose declaration says nothing usable, and the
// extra seconds cost nothing on a bound that only fires when the agent is dead.
func podDeadlineGraceTerm(req Request) int64 {
	g := req.Execution.TerminationGracePeriodSeconds
	if g == nil || *g <= 0 {
		return corev1.DefaultTerminationGracePeriodSeconds
	}
	return min(*g, maxDeadlineGraceTerm)
}

// Agent-token transport (ADR 0055 Fix #3). The env-var transport keeps the
// plaintext token on the pod spec (today's behavior); the exchange transport
// keeps it OFF and projects a ServiceAccount token instead.
const (
	agentTransportExchange = "exchange"
	// agentTokenVolumeName / agentTokenMountDir / agentTokenFile place the
	// projected ServiceAccount token the agent exchanges for a task-scoped JWT.
	agentTokenVolumeName = "leoflow-agent-token"
	agentTokenMountDir   = "/var/run/leoflow"
	agentTokenFile       = "token"
	// DefaultAgentTokenAudience is the projected token's audience when the request
	// does not set one — the control plane's TokenReviewer validates the projected
	// token against this exact audience on exchange, so both sides share the const.
	DefaultAgentTokenAudience = "leoflow-control-plane"
	// minProjectedTokenExpirationSeconds floors the projected token's lifetime so a
	// very short task's bootstrap credential is not already expired at exchange time
	// (ADR 0055 "Verify at implementation": ~10 min floor). It is also the default.
	minProjectedTokenExpirationSeconds int64 = 600
	// AgentIdentityAnnotation carries the exact (unsanitized) task-instance identity
	// the control plane resolves a reviewed pod to on exchange. Pod labels are
	// sanitized and lossy, so the resolver reads this instead. Written only under
	// the exchange transport, so the env-var default pod spec is unchanged. It is
	// exported so the pod → task-instance resolver reads the SAME contract that
	// wrote it (single-sourced, no drift).
	AgentIdentityAnnotation = "leoflow.io/agent-identity"
)

// PodIdentity is the JSON payload of AgentIdentityAnnotation: the full
// task-instance identity the control plane mints the exchanged JWT for.
type PodIdentity struct {
	TaskInstanceID string `json:"ti"`
	TenantID       string `json:"tenant"`
	DagID          string `json:"dag"`
	RunID          string `json:"run"`
	TaskID         string `json:"task"`
	TryNumber      int    `json:"try"`
}

// ParseAgentIdentity decodes the AgentIdentityAnnotation payload. It is the read
// side of the identity contract mountAgentToken writes, used by the pod →
// task-instance resolver on the exchange path.
func ParseAgentIdentity(raw string) (PodIdentity, error) {
	var id PodIdentity
	if err := json.Unmarshal([]byte(raw), &id); err != nil {
		return PodIdentity{}, fmt.Errorf("decoding agent-identity annotation: %w", err)
	}
	return id, nil
}

// agentToken is the transport-independent description of how an agent's bootstrap
// bearer credential reaches a pod, shared by the task-pod (BuildPod) and warm-pod
// (BuildWarmPod) builders so the token transport is wired one way for both. A task
// pod fills identity so the exchange resolver can map the projected token back to
// its task instance; a warm worker leaves identity nil (it registers under its
// bearer's own identity, not a task instance), so no identity annotation is
// stamped for it.
type agentToken struct {
	transport         string
	token             string
	secretName        string
	secretKey         string
	audience          string
	expirationSeconds int64
	identity          *PodIdentity
}

// agentTokenOf projects a Request's token fields into the shared carrier, stamping
// the task-instance identity so the exchange path is byte-identical to before.
func agentTokenOf(req Request) agentToken {
	return agentToken{
		transport:         req.AgentTokenTransport,
		token:             req.AgentToken,
		secretName:        req.AgentTokenSecretName,
		secretKey:         req.AgentTokenSecretKey,
		audience:          req.AgentTokenAudience,
		expirationSeconds: req.AgentTokenExpirationSeconds,
		identity: &PodIdentity{
			TaskInstanceID: req.TaskInstanceID, TenantID: req.TenantID, DagID: req.DagID,
			RunID: req.RunID, TaskID: req.TaskID, TryNumber: req.TryNumber,
		},
	}
}

// usesExchange reports whether the token opted into the projected-token exchange
// transport. Empty / "envvar" is the default (plaintext env var).
func (t agentToken) usesExchange() bool { return t.transport == agentTransportExchange }

// env returns the env var(s) carrying (or pointing at) the agent's bearer
// credential, per the selected transport:
//   - env-var (default): a plaintext LEOFLOW_AGENT_TOKEN — today's behavior.
//   - exchange + SecretKeyRef fallback: LEOFLOW_AGENT_TOKEN sourced from a Secret.
//   - exchange (projected, primary): NO token env var at all — the agent reads the
//     projected token from LEOFLOW_AGENT_TOKEN_PATH and exchanges it. The path and
//     transport marker are added by pathEnv.
func (t agentToken) env() []corev1.EnvVar {
	if !t.usesExchange() {
		return []corev1.EnvVar{{Name: "LEOFLOW_AGENT_TOKEN", Value: t.token}}
	}
	if t.secretName != "" {
		key := t.secretKey
		if key == "" {
			key = agentTokenFile
		}
		return []corev1.EnvVar{{
			Name: "LEOFLOW_AGENT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: t.secretName},
				Key:                  key,
			}},
		}}
	}
	return nil // projected primary path: no token env var, only the path (pathEnv).
}

// pathEnv returns the transport marker + token-file path the agent reads under the
// projected exchange path, or nil for the env-var default and the SecretKeyRef
// fallback (whose token is already in LEOFLOW_AGENT_TOKEN), keeping those pod specs
// unchanged.
func (t agentToken) pathEnv() []corev1.EnvVar {
	if !t.usesExchange() || t.secretName != "" {
		return nil
	}
	return []corev1.EnvVar{
		{Name: "LEOFLOW_AGENT_TOKEN_TRANSPORT", Value: agentTransportExchange},
		{Name: "LEOFLOW_AGENT_TOKEN_PATH", Value: agentTokenMountDir + "/" + agentTokenFile},
	}
}

// mountAgentToken projects the ServiceAccount token the agent exchanges (ADR 0055
// Fix #3), and — for a task pod — stamps the identity annotation the control plane
// resolves the pod to on exchange. It is a no-op for the env-var default and for
// the SecretKeyRef fallback (neither projects a token), keeping those pod specs
// unchanged. A warm worker (identity nil) projects the token but stamps no
// task-instance identity: it authenticates its stream under its bootstrap bearer
// and serves any task of its dag_version, so a task identity would be wrong.
func mountAgentToken(pod *corev1.Pod, t agentToken) {
	if !t.usesExchange() || t.secretName != "" {
		return
	}
	audience := t.audience
	if audience == "" {
		audience = DefaultAgentTokenAudience
	}
	exp := t.expirationSeconds
	if exp < minProjectedTokenExpirationSeconds {
		exp = minProjectedTokenExpirationSeconds
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: agentTokenVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{
					ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
						Audience:          audience,
						ExpirationSeconds: ptr(exp),
						Path:              agentTokenFile,
					},
				}},
			},
		},
	})
	c := &pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name: agentTokenVolumeName, MountPath: agentTokenMountDir, ReadOnly: true,
	})
	if t.identity == nil {
		return
	}
	if raw, err := json.Marshal(*t.identity); err == nil {
		pod.Annotations[AgentIdentityAnnotation] = string(raw)
	}
}

// taskSecretVolumeName is the pod volume name for the operator-supplied task
// credential Secret.
const taskSecretVolumeName = "leoflow-task-secret"

// mountTaskSecret mounts an operator-configured Kubernetes Secret (when set)
// read-only into the task pod at req.TaskSecretMountPath. This is how a task
// reads a credential that lives in the cluster's secret store — e.g. a GCP
// service-account key a connection references by key_path — so Leoflow never
// stores the key itself (ADR 0035).
func mountTaskSecret(pod *corev1.Pod, req Request) {
	if req.TaskSecretName == "" || req.TaskSecretMountPath == "" {
		return
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: taskSecretVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: req.TaskSecretName},
		},
	})
	c := &pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name: taskSecretVolumeName, MountPath: req.TaskSecretMountPath, ReadOnly: true,
	})
}

// agentCAVolumeName / agentCADir / agentCAFile place the CA the agent uses to
// verify the control plane's gRPC server certificate (issue #58).
const (
	agentCAVolumeName = "leoflow-agent-ca"
	agentCADir        = "/etc/leoflow/tls"
	agentCAFile       = "ca.crt"
)

// mountAgentTLSCA mounts the CA ConfigMap (when configured) into the task pod so
// the agent can verify the control plane's TLS cert. The matching env vars are
// set in podEnv.
func mountAgentTLSCA(pod *corev1.Pod, req Request) {
	if req.AgentTLSCAConfigMap == "" {
		return
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: agentCAVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: req.AgentTLSCAConfigMap},
			},
		},
	})
	c := &pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: agentCAVolumeName, MountPath: agentCADir, ReadOnly: true})
}

// writableTmpVolumeName / writableTmpMountPath give a read-only-rootfs pod a
// writable scratch directory at /tmp (issue #741). Pod Security Admission's
// `restricted` profile is happy with a read-only root filesystem, but nothing on
// that filesystem is writable — so an ordinary task (pip cache, matplotlib
// config, temp files) and, worse, the warm agent itself (os.MkdirTemp under /tmp,
// before it can even register) have nowhere to write. The idiomatic pairing is a
// read-only rootfs plus a writable emptyDir mounted at /tmp; emptyDir usage
// counts against the container's ephemeral-storage limit, so a task that sets one
// bounds this too.
const (
	writableTmpVolumeName = "leoflow-tmp"
	writableTmpMountPath  = "/tmp"
)

// mountWritableTmp gives a read-only-rootfs pod somewhere writable at /tmp by
// mounting an emptyDir there. It is a no-op unless ReadOnlyRootFilesystem is set:
// a pod with a writable rootfs already has a writable /tmp, so mounting an
// emptyDir would only change the spec for no reason. This makes
// read_only_task_root_filesystem usable on both task and warm pods.
func mountWritableTmp(pod *corev1.Pod, ps PodSecurity) {
	if !ps.ReadOnlyRootFilesystem {
		return
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         writableTmpVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	c := &pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name: writableTmpVolumeName, MountPath: writableTmpMountPath,
	})
}

// stagingVolumeName is the pod volume name for the per-run staging PVC.
const stagingVolumeName = "leoflow-staging"

// stagingMountPath is where the per-run staging volume is mounted in task pods,
// exposed to user code as LEOFLOW_STAGING_DIR. See ADR 0022.
const stagingMountPath = "/staging"

// mountStagingVolume attaches the run's shared staging PVC (when set) to the task
// container at stagingMountPath, so a run's tasks share large intermediate data.
func mountStagingVolume(pod *corev1.Pod, req Request) {
	if req.StagingClaim == "" {
		return
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: stagingVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: req.StagingClaim},
		},
	})
	c := &pod.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: stagingVolumeName, MountPath: stagingMountPath})
}

func podEnv(req Request) []corev1.EnvVar {
	env := make([]corev1.EnvVar, 0, 4+len(req.Env))
	env = append(env, corev1.EnvVar{Name: "LEOFLOW_CONTROL_PLANE_ADDR", Value: req.ControlPlaneAddr})
	tok := agentTokenOf(req)
	// The bearer credential: a plaintext env var (env-var default), a SecretKeyRef
	// (exchange fallback), or nothing on the pod object (exchange projected path).
	env = append(env, tok.env()...)
	env = append(env,
		corev1.EnvVar{Name: "LEOFLOW_TASK_INSTANCE_ID", Value: req.TaskInstanceID},
		// Tell the pod agent where to write its durable outcome record; it matches
		// the container's TerminationMessagePath. Only the pod path sets this, so
		// Lite (subprocess, no pod) leaves the agent's record writing disabled.
		corev1.EnvVar{Name: "LEOFLOW_TERMINATION_LOG_PATH", Value: terminationMessagePath},
	)
	// Under the exchange transport's projected path, tell the agent to read its
	// bootstrap token from the mounted file and exchange it for a task-scoped JWT.
	// The SecretKeyRef fallback and the env-var default leave these unset (the
	// token is already in LEOFLOW_AGENT_TOKEN), so those pod specs are unchanged.
	env = append(env, tok.pathEnv()...)
	if req.StagingClaim != "" {
		env = append(env, corev1.EnvVar{Name: "LEOFLOW_STAGING_DIR", Value: stagingMountPath})
	}
	if req.AgentTLSCAConfigMap != "" {
		env = append(env,
			corev1.EnvVar{Name: "LEOFLOW_AGENT_INSECURE", Value: "false"},
			corev1.EnvVar{Name: "LEOFLOW_AGENT_TLS_CA", Value: agentCADir + "/" + agentCAFile},
		)
	}
	// External secrets backend (ADR 0060): operator-sourced, injected in the
	// leoflow-owned group BEFORE author req.Env below. An author's task env cannot
	// carry LEOFLOW_ keys (stripped at dispatch, #828), so this is never overridable.
	if req.SecretsBackend != "" {
		env = append(env,
			corev1.EnvVar{Name: "LEOFLOW_SECRETS_BACKEND", Value: req.SecretsBackend},
			corev1.EnvVar{Name: "LEOFLOW_SECRETS_BACKEND_KWARGS", Value: req.SecretsBackendKwargs},
		)
	}
	for k, v := range req.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	return env
}

// ptr returns a pointer to v. The Kubernetes API models optional booleans as
// *bool, where nil means "cluster default" and is not the same as false.
func ptr[T any](v T) *T { return &v }

// decodeStructured converts one untyped map — carried verbatim from the DAG spec
// — into a typed Kubernetes object via a JSON round-trip (the map keys match the
// target type's JSON field names). ok is false when the entry cannot be
// marshaled or decoded, so callers skip it rather than failing the whole pod
// build, matching how BuildPod treats other optional placement hints.
func decodeStructured[T any](m map[string]any) (out T, ok bool) {
	b, err := json.Marshal(m)
	if err != nil {
		return out, false
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, false
	}
	return out, true
}

// decodeStructuredSlice maps decodeStructured over a slice of untyped entries,
// skipping malformed ones. It returns nil when none are declared or none survive,
// so the field stays unset for a DAG that declares none.
func decodeStructuredSlice[T any](raw []map[string]any) []T {
	if len(raw) == 0 {
		return nil
	}
	out := make([]T, 0, len(raw))
	for _, m := range raw {
		if v, ok := decodeStructured[T](m); ok {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildTolerations converts a task's declared tolerations — an untyped
// []map[string]any carried verbatim from the DAG spec — into typed pod
// tolerations via a JSON round-trip. A malformed entry is skipped and omission
// stays unset (nil).
func buildTolerations(raw []map[string]any) []corev1.Toleration {
	return decodeStructuredSlice[corev1.Toleration](raw)
}

// buildAffinity converts a task's declared affinity — an untyped map[string]any
// carried verbatim from the DAG spec — into a typed *corev1.Affinity via a JSON
// round-trip. Returns nil when none is declared or the entry is malformed, so the
// field stays unset for a DAG that declares none.
func buildAffinity(m map[string]any) *corev1.Affinity {
	if len(m) == 0 {
		return nil
	}
	if v, ok := decodeStructured[corev1.Affinity](m); ok {
		return &v
	}
	return nil
}

// mergeMetadata overlays operator-declared labels or annotations onto Leoflow's
// own pod metadata, but Leoflow's keys always win a collision: the leoflow.io/*
// identity labels and the task-instance-id annotation are load-bearing (the
// reconciler and terminate path select on them), so a DAG cannot shadow them. The
// own map is mutated in place; a nil declared map is a no-op.
func mergeMetadata(own, declared map[string]string) {
	for k, v := range declared {
		if _, taken := own[k]; !taken {
			own[k] = v
		}
	}
}

// nonRootFSGroup is the GID the task base image runs as (runtime/Dockerfile:
// USER 65532:65532), the de-facto "nonroot" GID from Google's distroless images
// and the same GID the Helm chart pins for the control-plane and migration pods.
// A non-root task carries it as the pod's fsGroup so the kubelet chowns mounted
// volumes to this group and adds it to the container's supplementary groups —
// without it the per-run staging PVC (ADR 0022) lands root-owned and the
// non-root user cannot write the intermediate data it is meant to share.
const nonRootFSGroup int64 = 65532

// buildPodSecurityContext returns the pod-level security context, or nil when no
// pod-wide setting is needed. It carries fsGroup — a pod-level knob with no
// container-level equivalent — only for a non-root task: a task that may run as
// root writes its volumes as root already, so there is nothing to fix, and
// leaving the context unset skips the kubelet's recursive volume chown and keeps
// the spec byte-identical to before this feature.
func buildPodSecurityContext(ps PodSecurity) *corev1.PodSecurityContext {
	if !ps.RunAsNonRoot {
		return nil
	}
	return &corev1.PodSecurityContext{FSGroup: ptr(nonRootFSGroup)}
}

// buildSecurityContext hardens the task container toward Pod Security Admission's
// `restricted` profile, which requires four things: no privilege escalation,
// every capability dropped, a seccomp profile, and a non-root user.
//
// The first three are unconditional because they cost an ordinary task nothing:
// a task process does not escalate privileges, needs no Linux capability, and
// runs fine under the runtime's default seccomp filter. None of them depend on
// the UID, so they apply whatever the image runs as.
//
// The fourth, runAsNonRoot, follows req.PodSecurity: on by default now that the
// shipped task images carry numeric non-root UIDs, and paired at the pod level
// with an fsGroup (buildPodSecurityContext) so the non-root user can write its
// mounted volumes. An operator can still turn it off for a fleet whose images
// legitimately run as root.
//
// readOnlyRootFilesystem stays opt-in: `restricted` never asks for it, and
// turning it on by default breaks any task that writes to /tmp or its home dir.
func buildSecurityContext(ps PodSecurity) *corev1.SecurityContext {
	sc := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if ps.RunAsNonRoot {
		sc.RunAsNonRoot = ptr(true)
	}
	if ps.ReadOnlyRootFilesystem {
		sc.ReadOnlyRootFilesystem = ptr(true)
	}
	return sc
}

func buildResources(r domain.Resources) corev1.ResourceRequirements {
	out := corev1.ResourceRequirements{}
	if r.Requests != nil {
		out.Requests = quantities(*r.Requests)
	}
	if r.Limits != nil {
		out.Limits = quantities(*r.Limits)
	}
	// Container-side Dynamic Resource Allocation: the claims (declared pod-level in
	// Execution.ResourceClaims) this container consumes (ADR 0054).
	out.Claims = decodeStructuredSlice[corev1.ResourceClaim](r.Claims)
	return out
}

func quantities(q domain.ResourceQuantity) corev1.ResourceList {
	list := corev1.ResourceList{}
	for name, value := range map[corev1.ResourceName]string{
		corev1.ResourceCPU:              q.CPU,
		corev1.ResourceMemory:           q.Memory,
		corev1.ResourceEphemeralStorage: q.EphemeralStorage,
	} {
		if value == "" {
			continue
		}
		if parsed, err := resource.ParseQuantity(value); err == nil {
			list[name] = parsed
		}
	}
	if len(list) == 0 {
		return nil
	}
	return list
}

func podName(req Request) string {
	suffix := randSuffix()
	base := fmt.Sprintf("leoflow-%s-%s-%d", sanitizeLabel(req.DagID), sanitizeLabel(req.TaskID), req.TryNumber)
	maxBase := 63 - len(suffix) - 1
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-")
	}
	return base + "-" + suffix
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func randSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b)
}

func ptrStr(s string) *string {
	return &s
}

func buildTolerationsForPlan(declared []map[string]any, plan string) []corev1.Toleration {
	tols := buildTolerations(declared)
	if plan == "free" {
		tols = append(tols, corev1.Toleration{
			Key:      "cloud.google.com/gke-spot",
			Operator: corev1.TolerationOpEqual,
			Value:    "true",
			Effect:   corev1.TaintEffectNoSchedule,
		})
	}
	return tols
}
