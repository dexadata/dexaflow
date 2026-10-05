// Package executor runs task instances via Kubernetes, Docker, or a subprocess.
package executor

import (
	"context"

	"github.com/dexadata/dexaflow/internal/domain"
)

// PodSecurity holds the task-pod hardening knobs whose defaults are behavioral
// rather than free. Both zero values are the safe choice, so a Request that
// never touches this struct gets a pod that Pod Security Admission's
// `restricted` profile admits.
type PodSecurity struct {
	// RunAsNonRoot refuses to start a task container whose image resolves to
	// UID 0. It completes the `restricted` set and is on by default: the images
	// Dexaflow ships now satisfy it. runtime/Dockerfile runs as the numeric
	// non-root UID 65532 (`USER 65532:65532` — a name the kubelet cannot resolve
	// is what previously blocked this), and every examples/*/image inherits it.
	// When set, BuildPod also stamps a pod-level fsGroup (nonRootFSGroup) so the
	// per-run staging PVC (ADR 0022) stays writable by that non-root user.
	//
	// It stays a knob rather than a constant so an operator can turn it off for a
	// fleet whose task images legitimately run as root.
	RunAsNonRoot bool

	// ReadOnlyRootFilesystem mounts the container root read-only. Off by
	// default on purpose: `restricted` does not require it, and it breaks
	// ordinary Python tasks that write to /tmp, the pip cache or a matplotlib
	// config dir. Opt in for a task that is known not to write.
	ReadOnlyRootFilesystem bool
}

// Request bundles everything an executor needs to run a single task instance.
type Request struct {
	TaskInstanceID string
	TenantID       string
	DagID          string
	RunID          string
	TaskID         string
	TryNumber      int
	// AttemptEpoch is the execution of TryNumber the dispatcher claimed for this
	// request (ADR 0051 amendment). It is stamped into the pod's identity
	// annotation so the exchanged token names this execution.
	AttemptEpoch int

	Image           string
	ImagePullPolicy string
	Operator        string
	Entrypoint      string
	Env             map[string]string
	Resources       domain.Resources
	Execution       domain.Execution
	TimeoutSeconds  int
	// AttemptLifetimeCeilingSeconds is the operator's attempt credential ceiling
	// (auth.max_attempt_credential_lifetime) in whole seconds. The Kubernetes
	// executor floors the task pod's ActiveDeadlineSeconds with it when the task
	// declares no TimeoutSeconds, so no task pod is immortal: the agent's reports
	// retry for as long as the control plane is unreachable, and without a floor
	// a total control-plane outage would leave every such pod Running forever,
	// holding its requests and blocking node scale-down. Past the ceiling the pod
	// can do nothing useful — heartbeat renewal stops and its bearer lapses. A
	// user-declared TimeoutSeconds always wins, even when longer. Non-positive
	// means "no ceiling" (the same convention as the warm worker's attempt
	// watchdog for the same knob) and applies no floor. Ignored by the subprocess
	// executor, which has no pod.
	AttemptLifetimeCeilingSeconds int64

	// PodSecurity carries the two hardening choices that can change how a task
	// runs. Everything else BuildPod applies is unconditional, because dropping
	// capabilities, blocking privilege escalation and setting a seccomp profile
	// cost a normal task nothing. Zero value is the secure default.
	PodSecurity PodSecurity

	// Source is the dag.py text captured at compile time. The SubprocessExecutor
	// materializes it to a per-TI temp dir so `python -m leoflow_runtime
	// dag:<task>` can importlib it from there — this is how multi-DAG Lite setups
	// avoid the ModuleNotFoundError that hit Lima 2026-06-01 when the agent's
	// global workdir didn't carry the user's dag.py. Empty for Pro (the
	// container image already carries the source); ignored by the K8s executor.
	Source string

	// Agent connection details injected into the worker environment.
	ControlPlaneAddr string
	AgentToken       string

	// AgentTokenTransport selects how the agent's bearer credential reaches the
	// pod (ADR 0055 Fix #3): "" / "envvar" (the default) sets AgentToken as a
	// plaintext LEOFLOW_AGENT_TOKEN env var — today's behavior, byte-identical;
	// "exchange" keeps the plaintext token OFF the pod object and instead projects
	// a ServiceAccount token the agent exchanges for a task-scoped JWT. Ignored by
	// the subprocess executor (Lite has no pod/SA).
	AgentTokenTransport string
	// AgentTokenAudience is the audience of the projected ServiceAccount token
	// under the exchange transport (the control plane's audience). Empty falls back
	// to the default control-plane audience.
	AgentTokenAudience string
	// AgentTokenExpirationSeconds is the projected token's expiration under the
	// exchange transport. Floored to a safe minimum so a very short task's
	// bootstrap token has not already expired at exchange time.
	AgentTokenExpirationSeconds int64
	// AgentTokenSecretName / AgentTokenSecretKey select the SecretKeyRef fallback
	// for the exchange transport (a cluster that cannot project an SA token): when
	// AgentTokenSecretName is set, LEOFLOW_AGENT_TOKEN is sourced from that Secret
	// via SecretKeyRef rather than projected — still off the plaintext pod spec.
	AgentTokenSecretName string
	AgentTokenSecretKey  string

	// StagingClaim, when set, is the name of the per-DAG-run RWX PVC mounted at
	// /staging in the task pod for large intermediate data shared across the run
	// (ADR 0022). Empty means no staging volume. StagingSize/StagingStorageClass
	// are used to provision the claim on first use.
	StagingClaim        string
	StagingSize         string
	StagingStorageClass string
	// StagingAccessMode is the PVC access mode (default ReadWriteMany; single-node
	// dev uses ReadWriteOnce). Empty means ReadWriteMany.
	StagingAccessMode string

	// AgentTLSCAConfigMap, when set, is the name of a ConfigMap holding the CA
	// (key ca.crt) the agent uses to verify the control plane's gRPC TLS cert
	// (issue #58). It is mounted into the task pod and selects TLS for the agent.
	AgentTLSCAConfigMap string

	// TaskSecretName, when set, is a Kubernetes Secret mounted read-only into the
	// task pod at TaskSecretMountPath. It carries a credential a task references by
	// path (e.g. a GCP service-account key via the connection's key_path), keeping
	// the key in the cluster's secret store rather than in Dexaflow (ADR 0035).
	TaskSecretName      string
	TaskSecretMountPath string

	// SecretsBackend / SecretsBackendKwargs, when set, are the operator's external
	// secrets backend (ADR 0060): the provider class the in-pod resolver drives and
	// its raw kwargs JSON. Injected as LEOFLOW_SECRETS_BACKEND[_KWARGS] pod env, in
	// the leoflow-owned group (operator-sourced — an author's task env cannot set
	// LEOFLOW_ keys, #828). Empty = no external backend (chain stays vault-only).
	SecretsBackend       string
	SecretsBackendKwargs string
}

// Executor runs or dispatches a task. For asynchronous executors
// (Kubernetes/Docker/subprocess) the return reflects dispatch only, and the
// agent reports the final state over gRPC. The Disposition tells the scheduler
// WHY a dispatch failed — transient cluster Backpressure vs a permanent
// Rejected — so the orchestration layer never has to inspect runtime-specific
// error types itself (ADR 0051 Phase 4). A successful dispatch returns
// (Dispatched, nil); a failure returns the classified disposition alongside the
// non-nil cause (its text feeds the scheduler's note/log).
type Executor interface {
	Execute(ctx context.Context, req Request) (Disposition, error)
}
