// Package dispatch launches pod-path task instances: it resolves a task's
// execution context, mints the agent's identity token, and routes the request
// to the executor. It implements scheduler.Dispatcher.
package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"github.com/google/uuid"
)

// reservedEnvPrefix marks env vars owned by leoflow's control plane / agent. An
// author's task env (dexaflow.yaml `env:`) must never set these: they configure
// the in-pod agent's control-plane address, token transport, and (ADR 0060) the
// external-secrets backend. An author override reaches the agent's own container
// (task env is appended last in the pod spec), so it could redirect the agent's
// credential exchange or downgrade its transport (#828).
const reservedEnvPrefix = "LEOFLOW_"

// reservedEnvPrefixNew is the same reservation under the name since the rename;
// the agent mirrors it onto reservedEnvPrefix at startup, so it is just as
// reserved.
const reservedEnvPrefixNew = "DEXAFLOW_"

// stripReservedEnv returns a copy of env without any leoflow-reserved key, so an
// author's dexaflow.yaml env: cannot override the agent's own configuration. The
// prefix match is case-insensitive (env keys are case-sensitive on Linux, but the
// agent only ever reads the canonical uppercase form; drop any case an author
// tries). A nil map stays nil.
func stripReservedEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		upper := strings.ToUpper(k)
		if strings.HasPrefix(upper, reservedEnvPrefix) || strings.HasPrefix(upper, reservedEnvPrefixNew) {
			continue
		}
		out[k] = v
	}
	return out
}

// warmLeaseSeconds is the ack deadline placed on a warm-worker assignment: the
// worker must ack it as started within this window or the registry reclaims the
// attempt (ADR 0058 N1b, H1). It is a fixed, sensible constant here — the pool
// does not yet tune it per DAG.
const warmLeaseSeconds = 30

// WarmPlacer hands a per-attempt WorkAssignment to a free warm worker of a
// dag_version, returning false when none is free (ADR 0058 N1b1-place). It is a
// narrow structural view of the agentrpc worker registry: the executor package
// must not import agentrpc, so the seam lives here and main.go passes the
// registry, which satisfies it. A nil WarmPlacer on the Dispatcher means warm
// pools are off — every task takes the dedicated pod path, today's behavior.
type WarmPlacer interface {
	Assign(dagVersionID string, a *agentv1.WorkAssignment) bool
}

// Resolved is the execution context the dispatcher needs to launch a task.
type Resolved struct {
	TaskInstanceID  string
	TenantID        string
	Image           string
	ImagePullPolicy string
	TryNumber       int
	// AttemptEpoch identifies this execution attempt of the row (ADR 0051
	// amendment). The resolver claims a fresh value on every dispatch, so two
	// dispatches of one try never share it, even with no reset rail between
	// them. The agent token (A2) and the pod's attempt-epoch label (A4) are
	// minted from it.
	AttemptEpoch int
	// Staging carries the DAG's opt-in staging-volume config (ADR 0022); nil or
	// disabled means no per-run volume.
	Staging *domain.StagingConfig
	// Source is the dag.py text captured at compile time, threaded to the
	// SubprocessExecutor so Lite tasks can importlib their DAG without relying
	// on a globally-correct workdir. Empty for Pro (the container image carries
	// the source) and ignored by the KubernetesExecutor.
	Source string
}

// Resolver loads a task instance's execution context from storage.
type Resolver interface {
	ResolveTask(ctx context.Context, runID, taskID string) (Resolved, error)
}

// TokenIssuer mints a per-task-instance agent token.
type TokenIssuer interface {
	IssueAgentToken(id auth.AgentIdentity, ttl time.Duration) (string, error)
}

// PlatformDefaults are per-cluster task defaults applied at dispatch to fill
// gaps the DAG artifact left empty (ADR 0023, layer L0). They are the lowest
// precedence (task override > DAG default > platform default) and never replace
// a value baked into dag.json, so the artifact stays portable across clusters.
type PlatformDefaults struct {
	// StagingSize/StagingStorageClass default the per-run staging volume when the
	// DAG enabled staging but did not pin them (e.g. the cluster's RWX class).
	StagingSize         string
	StagingStorageClass string
	// StagingAccessMode is the PVC access mode for the staging volume (default
	// ReadWriteMany; single-node dev uses ReadWriteOnce).
	StagingAccessMode string
	// Resources defaults a task's requests/limits when neither the task override
	// nor the DAG set any.
	Resources *domain.Resources
	// Unit is the operator resource unit (executor.unit, ADR 0066). When set it
	// replaces Resources: every task is sized from its pool_slots, and a task
	// that declares more than pool_slots x unit is refused (or, under
	// enforce: warn, run and counted). Nil: no unit.
	Unit *domain.ResourceUnit
	// PodSecurity carries the task-pod hardening choices. It lives here, not in
	// the DAG spec, on purpose: whether untrusted task code may run as root is a
	// cluster-operator decision. Exposing it per-DAG would let an author elevate
	// their own task, which is the same self-service escalation as picking an
	// arbitrary service_account. It is populated from the cluster's configured
	// defaults (runAsNonRoot on), which an operator can override.
	PodSecurity executor.PodSecurity
}

// Dispatcher builds executor requests for queued pod-path tasks and runs them.
type Dispatcher struct {
	exec           executor.Executor
	resolver       Resolver
	issuer         TokenIssuer
	controlAddr    string
	tokenTTL       time.Duration
	tlsCAConfigMap string
	taskSecret     string
	taskSecretPath string
	defaults       PlatformDefaults
	// Agent-token transport (ADR 0055 Fix #3). Empty tokenTransport = the env-var
	// default (plaintext token env var), so a deployment that does not opt in is
	// byte-identical to today. "exchange" threads the projected-token config onto
	// the request; the audience/expiration are read only under exchange.
	tokenTransport         string
	tokenAudience          string
	tokenExpirationSeconds int64
	// placer, when non-nil, is the warm-worker placement seam (ADR 0058
	// N1b1-place). Dispatch tries to place an admitted attempt on a free warm
	// worker of its dag_version before falling back to a dedicated pod. nil means
	// warm pools are off — the dedicated pod path is byte-for-byte today's behavior.
	placer WarmPlacer
	// secretsBackend / secretsBackendKwargs are the operator's external secrets
	// backend (ADR 0060): the provider class + raw kwargs JSON, injected as
	// LEOFLOW_SECRETS_* pod env. Empty = no external backend (chain vault-only).
	secretsBackend       string
	secretsBackendKwargs string
	// defaultTaskServiceAccount is the operator-configured ServiceAccount task pods
	// run as when a DAG's task does not set execution.service_account. Empty keeps
	// today's behavior (pods run as the namespace default SA). Wiring the chart's
	// taskServiceAccount here makes keyless work without every DAG having to opt in
	// (the execution.service_account trap).
	defaultTaskServiceAccount string
	// attemptLifetimeCeiling is the operator's auth.max_attempt_credential_lifetime,
	// handed to the executor so a task pod that declares no timeout still gets a
	// deadline floor. Zero (unset / disabled) applies no floor.
	attemptLifetimeCeiling time.Duration
	// misfits counts tasks run under executor.unit.enforce=warn although they
	// do not fit their size (ADR 0066 §3). Nil: not counted.
	misfits UnitMisfitRecorder
	// sourceModeImage is the operator's runtime image when source mode is on
	// (ADR 0067 §3), "" when it is off. A version on that image that carries a
	// source dispatches in source mode, always on a cold pod.
	sourceModeImage string
}

// UnitMisfitRecorder counts a task that does not fit the resource unit but is
// let through under executor.unit.enforce=warn, by stage ("register" or
// "dispatch"). observability.Metrics satisfies it.
type UnitMisfitRecorder interface {
	RecordUnitMisfit(stage string)
}

// SetUnitMisfitRecorder wires the counter for tolerated unit misfits.
func (d *Dispatcher) SetUnitMisfitRecorder(r UnitMisfitRecorder) { d.misfits = r }

// SetAttemptLifetimeCeiling wires the operator's attempt credential ceiling
// (auth.max_attempt_credential_lifetime) into every dispatched request, where the
// Kubernetes executor floors the pod's ActiveDeadlineSeconds with it when the
// task declares no execution timeout. A non-positive value is "no ceiling" and
// applies no floor; the subprocess executor ignores it.
func (d *Dispatcher) SetAttemptLifetimeCeiling(ceiling time.Duration) {
	d.attemptLifetimeCeiling = ceiling
}

// wholeSeconds converts a positive duration to whole seconds for the executor
// request; a non-positive duration ("disabled") maps to 0 so the executor applies
// no floor.
func wholeSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(d.Seconds())
}

// SetSecretsBackend configures the operator's external secrets backend (ADR 0060):
// the provider class the in-pod resolver drives and its raw kwargs JSON, delivered
// to the pod as operator-owned LEOFLOW_SECRETS_* env. Empty leaves external secrets
// off (the chain stays vault-only).
func (d *Dispatcher) SetSecretsBackend(class, kwargs string) {
	d.secretsBackend, d.secretsBackendKwargs = class, kwargs
}

// warmSACompatible reports whether a task can run on a warm worker, whose
// ServiceAccount is the operator default fixed at pod creation. A task that pins a
// different execution.service_account is incompatible (the warm pod cannot adopt
// it) and must take the dedicated path; a task with no SA (uses the default) or one
// equal to the default is compatible.
func warmSACompatible(task domain.TaskSpec, warmSA string) bool {
	if task.Execution == nil || task.Execution.ServiceAccount == "" {
		return true
	}
	return task.Execution.ServiceAccount == warmSA
}

// SetDefaultTaskServiceAccount sets the ServiceAccount task pods run as when a
// DAG's task does not specify execution.service_account. Empty leaves pods on the
// namespace default SA (today's behavior). Wiring the chart's task ServiceAccount
// here closes the trap where creating the SA silently had no effect until every
// DAG also set execution.service_account.
func (d *Dispatcher) SetDefaultTaskServiceAccount(name string) { d.defaultTaskServiceAccount = name }

// NewDispatcher builds a Dispatcher that launches tasks via exec, resolves their
// context with resolver, mints tokens with issuer (valid for tokenTTL), and
// tells the agent to reach the control plane at controlAddr.
func NewDispatcher(exec executor.Executor, resolver Resolver, issuer TokenIssuer, controlAddr string, tokenTTL time.Duration) *Dispatcher {
	return &Dispatcher{exec: exec, resolver: resolver, issuer: issuer, controlAddr: controlAddr, tokenTTL: tokenTTL}
}

// SetWarmPlacer wires the warm-worker placement seam (ADR 0058 N1b1-place). With
// a placer set, Dispatch tries to place an admitted attempt on a free warm worker
// of its dag_version and only falls back to a dedicated pod on a warm miss. Leave
// it unset (nil) — the default — to keep dedicated pod-per-task, today's behavior.
func (d *Dispatcher) SetWarmPlacer(p WarmPlacer) { d.placer = p }

// SetSourceModeImage turns Pro source mode on for versions on image (ADR 0067
// §3); "" leaves it off, today's behavior. Lite does not set it: the subprocess
// executor always runs from the source.
func (d *Dispatcher) SetSourceModeImage(image string) { d.sourceModeImage = image }

// SetAgentTLSCAConfigMap configures the CA ConfigMap mounted into task pods so
// agents verify the control plane's gRPC TLS cert (issue #58). Empty = the agent
// stays on the insecure channel (dev).
func (d *Dispatcher) SetAgentTLSCAConfigMap(name string) { d.tlsCAConfigMap = name }

// SetTaskSecret configures a Kubernetes Secret mounted read-only into every task
// pod at mountPath, so tasks can read a credential (e.g. a GCP service-account
// key referenced by a connection's key_path) from the cluster's secret store
// rather than from Dexaflow (ADR 0035). Empty name = nothing mounted.
func (d *Dispatcher) SetTaskSecret(name, mountPath string) {
	d.taskSecret, d.taskSecretPath = name, mountPath
}

// SetPlatformDefaults configures the per-cluster task defaults applied at
// dispatch to fill gaps the DAG artifact left empty (ADR 0023, layer L0).
func (d *Dispatcher) SetPlatformDefaults(p PlatformDefaults) { d.defaults = p }

// SetAgentTokenTransport selects how the agent's bearer credential reaches the
// task pod (ADR 0055 Fix #3). transport is "" / "envvar" (the plaintext env-var
// default) or "exchange" (project a ServiceAccount token the agent exchanges for
// a task-scoped JWT). audience and expirationSeconds configure the projected
// token and are read only under the exchange transport. Ignored by the
// subprocess (Lite) executor, which has no pod.
func (d *Dispatcher) SetAgentTokenTransport(transport, audience string, expirationSeconds int64) {
	d.tokenTransport, d.tokenAudience, d.tokenExpirationSeconds = transport, audience, expirationSeconds
}

// Dispatch resolves the task, mints its agent token, and executes it. The
// executor classifies its own dispatch outcome and returns it as an
// executor.Disposition (ADR 0051 Phase 4). A dispatcher-INTERNAL failure that
// happens BEFORE Execute (task resolve, token mint) is permanent and so returns
// executor.Rejected — those bare errors classified as permanent before this
// change, so Rejected preserves the behavior exactly.
func (d *Dispatcher) Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error) {
	r, err := d.resolver.ResolveTask(ctx, runID, task.TaskID)
	if err != nil {
		return executor.Rejected, fmt.Errorf("resolving task %s: %w", task.TaskID, err)
	}
	// Registration refuses a task larger than its size once a unit is
	// configured; this catches a DAG registered before that (ADR 0066 §3), so no
	// task runs larger than the slots it is charged.
	if refused := d.checkUnit(runID, dagID, task); refused != nil {
		return executor.Refused, refused
	}
	// Register caps a source-mode source only while the mode is on; a version
	// registered on the runtime image before it was turned on was never
	// checked. Refuse it here with register's message rather than let the
	// apiserver reject an oversize annotation on every try (ADR 0067 §3).
	sourceMode := domain.SourceModeApplies(d.sourceModeImage, r.Image, r.Source != "")
	if sourceMode {
		if refused := domain.CheckSourceModeSize(dagID, r.Source); refused != nil {
			return executor.Refused, refused
		}
	}
	token, err := d.issuer.IssueAgentToken(auth.AgentIdentity{
		TaskInstanceID: r.TaskInstanceID,
		TenantID:       r.TenantID,
		DagID:          dagID,
		RunID:          runID,
		TaskID:         task.TaskID,
		TryNumber:      r.TryNumber,
		// The epoch the resolver just claimed for this execution (ADR 0051
		// amendment), so the token can never be mistaken for another execution
		// of the same try.
		AttemptEpoch:    r.AttemptEpoch,
		HasAttemptEpoch: true,
	}, d.tokenTTL)
	if err != nil {
		return executor.Rejected, fmt.Errorf("issuing agent token for %s: %w", task.TaskID, err)
	}

	// Warm placement (ADR 0058 N1b1-place). With warm pools on, offer the attempt
	// to a free warm worker of this dag_version, carrying the same identity
	// (run/dag/task/try) and per-attempt token the dedicated path would use. On a
	// hit the worker owns the attempt — return Dispatched. On a miss (no free
	// worker) fall through to the dedicated pod path: degrade, never strand.
	//
	// N1b1-place is assign-if-free-else-dedicated. The pool-size cap and
	// defer-at-max belong to N1b2/N1d, where real pool accounting (registered
	// workers + in-flight pods) exists; there is no admission cap here.
	//
	// Staging is excluded from warm reuse (ADR 0058 D5): a warm worker carries no
	// per-run /staging mount, so a staging attempt placed on one would silently
	// lose the run's shared volume. Staging attempts always take the dedicated
	// path below, which provisions the StagingClaim.
	// A warm worker is pre-created with the operator's default task ServiceAccount,
	// fixed for its lifetime, so a task that pins a DIFFERENT execution.service_account
	// cannot run on it (it would silently run as the wrong identity and break keyless
	// resolution). Such a task takes the dedicated path below, which sets its own SA —
	// the same degrade-not-strand exclusion as staging (ADR 0058 D5).
	// With a resource unit a warm pod is one unit, so only a task of size 1
	// that declares no resources fits on it (ADR 0066 §3).
	// A source-mode attempt never goes warm either (ADR 0067 §3): a warm pod is
	// built before its task is known, so it cannot carry the task's dag.py.
	if d.placer != nil && !sourceMode && d.warmEligible(r, task) {
		wa := &agentv1.WorkAssignment{
			AssignmentId: uuid.NewString(),
			AttemptToken: token,
			DagRunId:     runID,
			TaskId:       task.TaskID,
			TryNumber:    int32(r.TryNumber), //nolint:gosec // try number is a small bounded attempt counter, never near int32 max
			DagVersionId: dagVersionID,
			LeaseSeconds: warmLeaseSeconds,
			AttemptEpoch: int64(r.AttemptEpoch),
		}
		if d.placer.Assign(dagVersionID, wa) {
			return executor.Dispatched, nil
		}
	}

	req := executor.Request{
		TaskInstanceID:       r.TaskInstanceID,
		TenantID:             r.TenantID,
		DagID:                dagID,
		RunID:                runID,
		TaskID:               task.TaskID,
		TryNumber:            r.TryNumber,
		AttemptEpoch:         r.AttemptEpoch,
		Image:                r.Image,
		ImagePullPolicy:      r.ImagePullPolicy,
		Source:               r.Source,
		SourceMode:           sourceMode,
		Operator:             string(task.Type),
		Entrypoint:           task.Entrypoint,
		Env:                  stripReservedEnv(task.Env),
		SecretsBackend:       d.secretsBackend,
		SecretsBackendKwargs: d.secretsBackendKwargs,
		ControlPlaneAddr:     d.controlAddr,
		AgentToken:           token,
		// Cluster-operator policy, not a per-task choice — see PlatformDefaults.
		PodSecurity:                   d.defaults.PodSecurity,
		AttemptLifetimeCeilingSeconds: wholeSeconds(d.attemptLifetimeCeiling),
	}
	if task.ExecutionTimeoutSeconds != nil {
		req.TimeoutSeconds = *task.ExecutionTimeoutSeconds
	}
	req.Resources = d.taskResources(task)
	if task.Execution != nil {
		req.Execution = *task.Execution
	}
	// Default the task pod's ServiceAccount to the operator-configured one when the
	// DAG did not set execution.service_account, so keyless works without every DAG
	// opting in (the execution.service_account trap). An explicit per-task value
	// always wins.
	if req.Execution.ServiceAccount == "" {
		req.Execution.ServiceAccount = d.defaultTaskServiceAccount
	}
	if r.Staging != nil && r.Staging.Enabled {
		// All of the run's tasks share one PVC, named deterministically so a
		// clear+re-run re-attaches it (ADR 0022). The executor provisions it.
		req.StagingClaim = executor.StagingClaimName(dagID, runID)
		// L0: the DAG opted into staging but may not have pinned size/class; fill
		// from the per-cluster default without overriding an explicit value.
		req.StagingSize = firstNonEmpty(r.Staging.Size, d.defaults.StagingSize)
		req.StagingStorageClass = firstNonEmpty(r.Staging.StorageClass, d.defaults.StagingStorageClass)
		req.StagingAccessMode = d.defaults.StagingAccessMode
	}
	req.AgentTLSCAConfigMap = d.tlsCAConfigMap
	req.TaskSecretName = d.taskSecret
	req.TaskSecretMountPath = d.taskSecretPath
	// Agent-token transport (ADR 0055 Fix #3). Under the exchange transport the
	// executor projects a ServiceAccount token instead of placing the plaintext
	// token on the pod; the token is still minted above (the env-var path needs it,
	// and it is harmless under exchange).
	req.AgentTokenTransport = d.tokenTransport
	req.AgentTokenAudience = d.tokenAudience
	req.AgentTokenExpirationSeconds = d.tokenExpirationSeconds
	return d.exec.Execute(ctx, req)
}

// checkUnit applies the resource unit to a task about to dispatch and returns
// the refusal, if any. A misfit tolerated under enforce: warn runs with the
// task's own resources, but is logged and counted.
func (d *Dispatcher) checkUnit(runID, dagID string, task domain.TaskSpec) error {
	warned, refused := d.defaults.Unit.Check(task)
	if warned != nil {
		slog.Warn("task does not fit its size; running it under executor.unit.enforce=warn",
			"run", runID, "dag", dagID, "task", task.TaskID, "error", warned)
		if d.misfits != nil {
			d.misfits.RecordUnitMisfit("dispatch")
		}
	}
	return refused
}

// warmEligible reports whether an attempt may go to a warm worker: no
// staging, the warm ServiceAccount, and, with a resource unit, a size-1 task
// without resources, since a warm pod is one unit.
func (d *Dispatcher) warmEligible(r Resolved, task domain.TaskSpec) bool {
	return (r.Staging == nil || !r.Staging.Enabled) &&
		warmSACompatible(task, d.defaultTaskServiceAccount) &&
		d.defaults.Unit.WarmEligible(task)
}

// taskResources picks the task pod's resources. With a resource unit the unit
// sizes every task: its own values where it set them, the rest filled so
// requests never exceed limits (see ResourceUnit.Apply, ADR 0066 §3). Without one, the task's
// own resources win, then the L0 platform default (ADR 0023), else none.
func (d *Dispatcher) taskResources(task domain.TaskSpec) domain.Resources {
	switch {
	case d.defaults.Unit != nil:
		return *d.defaults.Unit.Apply(task)
	case task.Resources != nil:
		return *task.Resources
	case d.defaults.Resources != nil:
		return *d.defaults.Resources
	}
	return domain.Resources{}
}

// firstNonEmpty returns a if it is non-empty, otherwise b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
