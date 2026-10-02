{{/* Expand the name of the chart. */}}
{{- define "leoflow.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "leoflow.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "leoflow.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "leoflow.labels" -}}
helm.sh/chart: {{ include "leoflow.chart" . }}
{{ include "leoflow.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "leoflow.selectorLabels" -}}
app.kubernetes.io/name: {{ include "leoflow.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Role-aware selector labels (ADR 0049). Takes a dict {ctx, role}. For the "all"
role (the default, non-split monolith) this is EXACTLY leoflow.selectorLabels —
no extra label — so the single Deployment's immutable selector is unchanged and
`helm upgrade` from a pre-split install does not trip "field is immutable". For
the api/scheduler roles it adds app.kubernetes.io/component so each Deployment
selects only its own pods and Services can target one role.
*/}}
{{- define "leoflow.roleSelectorLabels" -}}
{{ include "leoflow.selectorLabels" .ctx }}
{{- if and .role (ne .role "all") }}
app.kubernetes.io/component: {{ .role }}
{{- end }}
{{- end -}}

{{/*
suffixRole appends "-<role>" to a base name. It truncates the BASE to 53 first,
so even with a max-length base the result fits in 63 chars AND the "-api" /
"-scheduler" suffix survives — without this, a ~62-char base truncates the suffix
off and api and scheduler collapse to the same name (an install-time collision).
An empty or "all" role returns the base unchanged (byte-identical to non-split).
Takes {base, role}.
*/}}
{{- define "leoflow.suffixRole" -}}
{{- if and .role (ne .role "all") -}}
{{- printf "%s-%s" (.base | trunc 53 | trimSuffix "-") .role | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .base -}}
{{- end -}}
{{- end -}}

{{/*
Role-suffixed resource name. "all" keeps the bare fullname (byte-identical to the
non-split Deployment/Service names); api/scheduler get a "-api"/"-scheduler"
suffix so the two Deployments, Services, SAs, etc. do not collide. Takes {ctx, role}.
*/}}
{{- define "leoflow.roleName" -}}
{{- include "leoflow.suffixRole" (dict "base" (include "leoflow.fullname" .ctx) "role" .role) -}}
{{- end -}}

{{/*
Application name of the migration hook's pod: the chart name with a "-migrate"
suffix. It is deliberately NOT leoflow.name.

A Service selector matches every pod whose labels CONTAIN its map, so the migrate
Job's pod template used to be selected by the control-plane Service and by the
PodDisruptionBudget: for the default "all" role, leoflow.roleSelectorLabels is
exactly leoflow.selectorLabels, and the Job carried that same pair. A Job pod has
no readinessProbe, so it is Ready the instant its container starts. `helm install`
never showed it (no Service exists yet when the pre-install hook runs) and split
mode never showed it either (api and scheduler add a component label the Job
lacks), which left `helm upgrade` on the default install as the one exposed path
(#1055).

What that cost, measured on k3s v1.33.6 (k3d) against the pre-fix chart with the hook pod
held open:

  - Disruption accounting was wrong for the whole migration window. An integer
    minAvailable budget reported currentHealthy=2 over ONE real replica — the
    hook pod padded the count — so the budget permitted one more simultaneous
    eviction than it was sized for. A percentage-valued budget failed outright:
    DisruptionAllowed=False, "jobs.batch does not implement the scale
    subresource", disruptionsAllowed pinned at 0, every voluntary eviction
    stalled until the hook pod exited.
  - Traffic was NOT lost, and the reason is worth writing down because it is
    accidental. The pod joined the Service's EndpointSlice (ready=true) but in a
    slice with `ports: null`: all three Service ports use NAMED targetPorts and
    this pod declares no containerPort, so the EndpointSlice controller could
    resolve nothing and kube-proxy programmed nothing. Give any of those ports a
    NUMERIC targetPort and the protection is gone — the same cluster then
    black-holed 19 of 40 requests to the Service. Consumers that read
    EndpointSlices and resolve ports themselves (AWS Load Balancer Controller in
    IP-target mode, service meshes, Gateway API implementations) are not covered
    by it either, and no in-cluster CI here can settle that.

Adding a component label alongside the shared pair does NOT fix this — a superset
still matches the selector. The pod has to differ on a key the selector CARRIES,
which is why the name changes rather than being decorated. It is also the honest
label: this pod runs a different image (leoflow-migrate) with a different
lifetime, not a replica of the control plane.

Suffixed through leoflow.suffixRole so a long nameOverride cannot produce a label
value over the 63-char limit — this is a pre-install hook, so an invalid label is
a failed install, not a degraded one.
*/}}
{{- define "leoflow.migrateName" -}}
{{- include "leoflow.suffixRole" (dict "base" (include "leoflow.name" .) "role" "migrate") -}}
{{- end -}}

{{/*
Pod-template labels for the migration hook Job. Kept whole and in one place so
the set that must NOT be a superset of any control-plane selector has a single
definition. instance is retained on purpose: `kubectl -n <ns> get pods -l
app.kubernetes.io/instance=<release>` is how an operator finds a wedged migration,
and it is not a key any control-plane selector can match alone.

scripts/check-migrate-pod-selection.sh renders the chart and asserts no
Service/PDB selector is a subset of this map, in every mode.
*/}}
{{- define "leoflow.migratePodLabels" -}}
app.kubernetes.io/name: {{ include "leoflow.migrateName" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: migrate
{{- end -}}

{{- define "leoflow.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "leoflow.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Role-aware ServiceAccount name (ADR 0049). Takes {ctx, role}. "all" keeps the
existing leoflow.serviceAccountName (honoring serviceAccount.create/name), so the
non-split chart is unchanged. api/scheduler each get their own SA named after the
role, so the api can carry a RESTRICTED identity (no pod-create/apiserver RBAC)
while the scheduler keeps the privileged one — the split's security payoff.
*/}}
{{- define "leoflow.roleServiceAccountName" -}}
{{- if and .role (ne .role "all") -}}
{{- /* Honor serviceAccount.name as the base (default fullname), then suffix by
role. So `serviceAccount.name=X` yields X-api / X-scheduler — the names an
operator using create=false must pre-provision (IRSA / Workload Identity). */ -}}
{{- $base := default (include "leoflow.fullname" .ctx) .ctx.Values.serviceAccount.name -}}
{{- include "leoflow.suffixRole" (dict "base" $base "role" .role) -}}
{{- else -}}
{{- include "leoflow.serviceAccountName" .ctx -}}
{{- end -}}
{{- end -}}

{{/*
Name of the cluster-scoped TokenReview ClusterRole + ClusterRoleBinding. Cluster
scope means one global namespace for these names, so the name carries BOTH the
release name and the namespace it was installed into: two Leoflow releases in one
cluster must not fight over a single object (and a `helm uninstall` of one must
not revoke the other's permission). Kubernetes allows a 253-char DNS-subdomain
name here, so neither half needs truncating.
*/}}
{{- define "leoflow.tokenReviewName" -}}
{{- printf "%s-%s-tokenreview" (include "leoflow.fullname" .) .Release.Namespace -}}
{{- end -}}

{{/*
Guaranteed replica floor of the Deployment a PodDisruptionBudget would protect:
the smallest number of pods the chart promises to keep, not the largest it may
scale to. Non-split: replicaCount, or autoscaling.minReplicas when the HPA owns
the count (it never scales below that floor, and replicaCount is ignored). Split
(ADR 0049): the active-active api Deployment's count — the scheduler is a single
leader and is never covered by a PDB. The PDB auto mode keys on this value: a
budget over a floor of one blocks every voluntary eviction of that pod (node
drains and auto-upgrades stall), so it is only safe-by-default above one.
*/}}
{{- define "leoflow.controlPlaneReplicaFloor" -}}
{{- $floor := .Values.replicaCount -}}
{{- if .Values.split.enabled -}}
{{- $floor = .Values.split.api.replicaCount -}}
{{- end -}}
{{- if .Values.autoscaling.enabled -}}
{{- $floor = .Values.autoscaling.minReplicas -}}
{{- end -}}
{{- int $floor -}}
{{- end -}}

{{/*
Largest number of control-plane pods that may mount the logs volume at once:
the ceiling, not the floor. Non-split: replicaCount, or autoscaling.maxReplicas
when the HPA owns the count. Split (ADR 0049): the scheduler writes logs and
every api replica reads them from the same volume, so the mounters are the api
replicas (or their HPA ceiling) plus the one scheduler. Both the single-writer
PVC render guard and the per-pod emptyDir warning key on this value, so a shape
one of them misses is a shape both miss — there is one definition.
*/}}
{{- define "leoflow.logMounterCeiling" -}}
{{- $n := .Values.replicaCount -}}
{{- if .Values.autoscaling.enabled -}}
{{- $n = .Values.autoscaling.maxReplicas -}}
{{- end -}}
{{- if .Values.split.enabled -}}
{{- $api := .Values.split.api.replicaCount -}}
{{- if .Values.autoscaling.enabled -}}
{{- $api = .Values.autoscaling.maxReplicas -}}
{{- end -}}
{{- $n = add (int $api) 1 -}}
{{- end -}}
{{- int $n -}}
{{- end -}}

{{/*
Whether the PodDisruptionBudget renders. podDisruptionBudget.enabled is
tri-state: an explicit true/false wins (true on a single replica is the
operator's informed choice, and NOTES.txt says what it costs — via
leoflow.pdbEnabledExplicit, so the warning follows every spelling this helper
honours); unset (auto) renders the PDB exactly when the guaranteed replica floor
is above one, i.e. when there is a second pod to keep serving while one is
evicted.

The explicit half accepts the STRING spellings of the two booleans as well as
real booleans, because a GitOps tool does not send booleans: Argo CD's
`helm.parameters` and `helm --set-string` pass every override as a string, and a
`kindIs "bool"` test alone read `"true"` as "not a bool" and fell through to
auto — so an operator who asked for a budget on a single replica got none, with
no diagnostic anywhere (#905). Anything else non-empty is a `fail` rather than
another silent fallback to auto: the whole failure mode here was a value that
looked accepted and did nothing.
*/}}
{{- define "leoflow.pdbEnabled" -}}
{{- $enabled := .Values.podDisruptionBudget.enabled -}}
{{- $auto := gt (include "leoflow.controlPlaneReplicaFloor" . | int) 1 -}}
{{- if kindIs "bool" $enabled -}}
{{- $enabled -}}
{{- else if kindIs "invalid" $enabled -}}
{{- $auto -}}
{{- else -}}
{{- $spelled := lower (toString $enabled) -}}
{{- if eq $spelled "" -}}
{{- $auto -}}
{{- else if eq $spelled "true" -}}
true
{{- else if eq $spelled "false" -}}
false
{{- else -}}
{{- /* %q over the raw interface garbles anything that is not a string: an
integer renders as a quoted rune (`'\x05'`) or a bad-verb error, and neither
names what the operator typed. Quote the string CONVERSION and add the kind, so
the message is legible for every value that can reach here (#905). */ -}}
{{- fail (printf "podDisruptionBudget.enabled must be a boolean, the string \"true\" or \"false\", or empty for auto (got %q, kind %s). It is tri-state: empty renders the PodDisruptionBudget exactly when the guaranteed replica floor is above one, true forces it on, false forces it off. The string spellings are accepted because Argo CD's helm.parameters and helm --set-string pass every override as a string; any other value is refused rather than silently falling back to auto, which would leave a budget the operator asked for unrendered with no diagnostic. See #905." (toString $enabled) (kindOf $enabled)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Whether podDisruptionBudget.enabled was set EXPLICITLY — "true" when it is, empty
when the release is in auto mode. It reports the SPELLING, not the YAML type: a
bool, or a non-empty value that lowercases to `true` or `false`, is explicit;
unset, null and empty are auto.

NOTES.txt needs this and `kindIs "bool"` will not do. Both of its budget branches
used to key on the value being a real bool, which was correct only while the
helper above ignored the string spellings. Once it honoured them, a
string-spelled `"true"` at a single replica rendered the budget and skipped the
warning that says what the budget costs — drains hanging on the one pod,
auto-upgrades stalling — so the Argo CD path this exists to serve got the trap
without the diagnostic, where before it got neither. The upgrade note has the
mirror bug: it attributes the budget to auto-selection ("replica floor > 1") over
a value the operator set by hand. One predicate, used by both branches (#905).

It deliberately does NOT validate: an unparseable value is `fail`ed by
leoflow.pdbEnabled, which every consumer of this predicate also renders, so
duplicating the refusal here would only risk the two disagreeing.
*/}}
{{- define "leoflow.pdbEnabledExplicit" -}}
{{- $enabled := .Values.podDisruptionBudget.enabled -}}
{{- if kindIs "bool" $enabled -}}
true
{{- else if kindIs "invalid" $enabled -}}
{{- else if has (lower (toString $enabled)) (list "true" "false") -}}
true
{{- end -}}
{{- end -}}

{{/*
The EXPLICIT deployment.strategy, trimmed and stringified once — empty means the
caller should auto-select. It is the one definition both the render
(_controlplane-deployment.tpl) and the single-writer RollingUpdate refusal
(deployment.yaml) read, so the value that is judged is the value that renders.

It is an allowlist for the same reason the access mode became one. The template
rendered this value VERBATIM into spec.strategy.type and the refusal compared it
EXACTLY, which left two holes on the same key:

  - `rollingupdate` matched neither, so it skipped the refusal AND the
    auto-selection, rendered as-is, and the apiserver rejected the install after
    a clean render — the defect class this chart refuses at render time.
  - `RollingUpdate ` — one trailing space — skipped the refusal too, but YAML
    strips a trailing space from a plain scalar, so the apiserver ACCEPTED it.
    That is worse: the operator gets exactly the Multi-Attach deadlock the
    refusal exists to prevent, reached THROUGH the refusal.

So: trim, allow only empty / RollingUpdate / Recreate, and fail anything else
rather than pass it to the apiserver (#905).

ASSUMES the chart renders no surge knob. Deployment.spec.strategy.rollingUpdate
(maxSurge / maxUnavailable) is deliberately absent, which is what makes
"RollingUpdate over a single-writer volume" unconditionally a deadlock: the
default maxSurge of 25% rounds up to one extra pod. A future PR that exposes
maxSurge makes this guard WRONG — `maxSurge: 0` rolls a single-writer volume
safely — and it must then gate the refusal on the surge being non-zero. The one
shape where an operator legitimately wants the surge today is a node-pinned
local volume, where both pods land on the same node and the volume is
re-attachable there; that operator can patch spec.strategy on the rendered
Deployment rather than have the chart weaken the guard for everyone.
*/}}
{{- define "leoflow.deploymentStrategy" -}}
{{- $raw := .Values.deployment.strategy -}}
{{- $strategy := "" -}}
{{- if not (kindIs "invalid" $raw) -}}
{{- $strategy = trim (toString $raw) -}}
{{- end -}}
{{- if not (has $strategy (list "" "RollingUpdate" "Recreate")) -}}
{{- fail (printf "deployment.strategy=%q (kind %s) is not a Deployment update strategy: set RollingUpdate, Recreate, or \"\" to let the chart auto-select (Recreate over a single-writer logs PVC, RollingUpdate otherwise). The value is rendered verbatim into spec.strategy.type and compared exactly, so an unrecognized spelling used to bypass both the single-writer RollingUpdate refusal and the auto-selection: the apiserver then rejected the install, or — for a spelling it accepts after trimming, like a trailing space — accepted the surge onto a volume only one pod can hold. See #905." (toString $raw) (kindOf $raw)) -}}
{{- end -}}
{{- $strategy -}}
{{- end -}}

{{/*
Name of the HOOK Secret. It exists only to feed the pre-install/pre-upgrade
migration Job, which runs before the normal manifest is applied and therefore
cannot read a tracked resource. Nothing long-lived may reference it: under Argo
CD a hook is deleted and recreated in separate passes of one operation, is
excluded from the compared state, and is not restored by self-heal, so an
interrupted sync removes it permanently while the Application still reports
Synced (#1142).
*/}}
{{- define "leoflow.secretName" -}}
{{- printf "%s-secrets" (include "leoflow.fullname" .) -}}
{{- end -}}

{{/*
Name of the DURABLE Secret holding inline credentials. This is an ordinary
tracked resource, which is what the control-plane Deployment reads. A new name
rather than the hook's: Helm stamps ownership metadata only on main-manifest
resources, so the live hook Secret carries no meta.helm.sh/release-name and
adopting its name into the manifest would abort every `helm upgrade` from 0.4.6
with an ownership conflict.
*/}}
{{- define "leoflow.credentialsSecretName" -}}
{{- printf "%s-credentials" (include "leoflow.fullname" .) -}}
{{- end -}}

{{/*
agentTLS mode resolution (#690). Precedence, matching the three states the chart
supports:
  1. BYO      — serverCertSecret AND caConfigMap both set: the operator brings a
                cert-manager Certificate Secret + CA trust bundle. Use them verbatim.
  2. auto-gen — otherwise, when agentTLS.autoGenerate is true: the chart renders a
                stable self-signed CA + server cert (agent-tls-autogen.yaml) so a
                fresh cluster needs no cert-manager and no pre-created Secret (#690).
  3. none     — auto-gen off and nothing supplied: the deployment guard `fail`s.
leoflow.agentTLS.autoGenerated returns "true" only in case 2, so every template
that must branch on auto-gen keys off one predicate.
*/}}
{{- define "leoflow.agentTLS.autoGenerated" -}}
{{- if and .Values.agentTLS.enabled (not (and .Values.agentTLS.serverCertSecret .Values.agentTLS.caConfigMap)) .Values.agentTLS.autoGenerate -}}
true
{{- end -}}
{{- end -}}

{{/*
Effective name of the kubernetes.io/tls Secret holding the gRPC server cert. In
BYO mode it is the operator-supplied serverCertSecret; in auto-gen mode it is the
chart-generated "<fullname>-agent-tls" (agent-tls-autogen.yaml renders it under
this exact name). Keeping the name in one helper means the Deployment volume and
the generator can never disagree.
*/}}
{{- define "leoflow.agentTLS.serverCertSecretName" -}}
{{- if eq (include "leoflow.agentTLS.autoGenerated" .) "true" -}}
{{- printf "%s-agent-tls" (include "leoflow.fullname" .) -}}
{{- else -}}
{{- .Values.agentTLS.serverCertSecret -}}
{{- end -}}
{{- end -}}

{{/*
Effective name of the ConfigMap (key ca.crt) task pods mount to verify the server
cert. BYO: the operator-supplied caConfigMap; auto-gen: "<fullname>-agent-ca".
*/}}
{{- define "leoflow.agentTLS.caConfigMapName" -}}
{{- if eq (include "leoflow.agentTLS.autoGenerated" .) "true" -}}
{{- printf "%s-agent-ca" (include "leoflow.fullname" .) -}}
{{- else -}}
{{- .Values.agentTLS.caConfigMap -}}
{{- end -}}
{{- end -}}

{{/* The image reference, defaulting the tag to the chart appVersion. */}}
{{- define "leoflow.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
In-cluster gRPC address task pods dial, unless overridden. When split.enabled
(ADR 0049) the agent gRPC endpoint lives on the scheduler Service, so task pods
must dial "<fullname>-scheduler", not the bare fullname (which in split mode is
the api Service and serves no gRPC).
*/}}
{{- define "leoflow.agentControlPlaneAddr" -}}
{{- if .Values.config.agentControlPlaneAddr -}}
{{- .Values.config.agentControlPlaneAddr -}}
{{- else -}}
{{- $svc := include "leoflow.fullname" . -}}
{{- if .Values.split.enabled -}}
{{- /* Use roleName (not a raw printf) so this DNS name matches the scheduler
Service exactly, including its trunc-63 — a long release name would otherwise
diverge and agents would dial a name no Service answers to. */ -}}
{{- $svc = include "leoflow.roleName" (dict "ctx" . "role" "scheduler") -}}
{{- end -}}
{{- printf "%s.%s.svc.cluster.local:%d" $svc .Release.Namespace (int .Values.ports.grpc) -}}
{{- end -}}
{{- end -}}

{{/*
Whether this apiserver supports the native preStop `sleep` hook action, which is
gated on PodLifecycleSleepAction: BETA and on by default from 1.30, ALPHA and OFF
in 1.29, and not present in the API types at all before that. Below 1.30 the
apiserver nils the field and its own validation then rejects the empty
`preStop: {}` it is left with ("must specify a handler type"), so rendering the
hook onto an older cluster REJECTS the Deployment and hard-fails `helm install`
— it is not silently dropped. The project's floor is 1.27, so this is a render
gate rather than a `kubeVersion` in Chart.yaml, which would refuse to install on
1.27-1.29 outright; below 1.30 the install succeeds and keeps the pre-hook
behavior instead (#918).

Returns "true" only when the hook can be rendered. Both the renderer
(_controlplane-deployment.tpl) and the NOTES.txt warning that tells the operator
the hook was dropped ask THIS helper, so the two can never disagree about which
clusters get the hook.

Note the limits of a semver test, both documented on
`deployment.preStopSleepSeconds`: a `helm template | kubectl apply` pipeline on a
helm 3 client defaults its capabilities to v1.20.0 and so omits the hook even
against a modern cluster (safe direction — the sleep is an optimization, not a
correctness requirement); and this is a proxy for a feature gate, so a
self-managed 1.30+ apiserver started with
`--feature-gates=PodLifecycleSleepAction=false` renders the hook and rejects the
Deployment, because nothing in discovery reports gate state.
*/}}
{{- define "leoflow.preStopSleepHookSupported" -}}
{{- if semverCompare ">=1.30.0-0" .Capabilities.KubeVersion.Version -}}
true
{{- end -}}
{{- end -}}

{{/*
probes.readiness.timeoutSeconds, validated against the server's own probe
budget.

/readyz bounds its whole handler at 2s (api.probeBudget) and that number was
chosen to sit UNDER the kubelet's timeout, because the two are one relationship
read from both ends: the server has to give up first, or the kubelet cancels a
probe that was about to answer and the operator gets a timeout with no log line
naming the dependency instead of a 503 that names it.

Nothing enforced the relationship. It was a comment in the Go source next to a
plain, floorless chart value, so `--set probes.readiness.timeoutSeconds=1`
rendered happily and inverted it: the kubelet gives up at 1s while the server is
still willing to spend 2s answering, and every probe against a database slower
than 1s becomes a silent timeout (#1041). Failing the render is the right end to
fail at — the alternative is a cluster that installs and then reports nothing.

The chart cannot read the Go constant, so scripts/check-readiness-probe-budget.sh
asserts the two still agree.
*/}}
{{- define "leoflow.readinessTimeoutSeconds" -}}
{{- $min := 3 -}}
{{- $raw := .Values.probes.readiness.timeoutSeconds -}}
{{- $v := int $raw -}}
{{- if lt $v $min -}}
{{- fail (printf "probes.readiness.timeoutSeconds=%v is below the %ds floor. The leoflow-server /readyz handler bounds itself at 2s so it answers before the kubelet gives up; a shorter kubelet timeout inverts that and turns every slow-dependency probe into a timeout that logs nothing, instead of a 503 naming the dependency. Raise it to %d or more. If you need the kubelet to react faster than 3s, lower probes.readiness.periodSeconds or failureThreshold instead — those shorten time-to-unready without cutting off the answer. See #1041." $raw $min $min) -}}
{{- end -}}
{{- $v -}}
{{- end -}}

{{/*
taskNetworkPolicy.allowMetadataEgress, validated and rendered as ipBlock peers.

This list is the only way through the 169.254.0.0/16 exception the task-pod
policy always installs, and that exception is the one containment the policy's
whole security story rests on. values.yaml documented the hatch as "one /32
each, never the whole range" and nothing enforced it, so `169.254.0.0/16`
rendered happily — and it does not merely widen the hatch, it removes the block:
egress rules are ADDITIVE. k8s.io/api networking/v1 (NetworkPolicySpec.Egress)
defines outgoing traffic as allowed "if the traffic matches at least one egress
rule across all of the NetworkPolicy objects whose podSelector matches the pod",
and IPBlock.Except narrows only the ipBlock it belongs to. So a second rule
allowing the range overrides the first rule's except-list, and the rendered
manifest still shows the except-list while nothing is blocked (#958).

Hence a render-time refusal, the same end the chart fails at for the other
unsafe couplings (deployment.yaml's single-writer volume, pdb.yaml's
minAvailable/maxUnavailable pair): the alternative is a cluster that installs
and is silently less contained than its own documentation claims.

What is accepted, deliberately:
  - an IPv4 address with /32, or an IPv6 address with /128 — "one host" is the
    invariant, and it is the ONLY one enforced.

    Membership of 169.254.0.0/16 is deliberately NOT checked, and the honest
    statement of what that costs is: this field re-permits ANY single host,
    including one that blockPrivateNetworks or extraExcept took away.
    `blockPrivateNetworks=true` with `allowMetadataEgress: [10.96.0.1/32]`
    renders, and hands back the EKS apiserver ClusterIP the except list had just
    removed. That is a real footgun and it is a deliberate one: a single host is
    the smallest thing this field can grant, `extraEgress` is not needed for it,
    and an in-range check would refuse the legitimate IPv6 endpoint below, which
    lives outside 169.254.0.0/16 entirely.

    An earlier version of this comment claimed the containment was "the same
    either way" while naming that exact counterexample in a parenthesis. It was
    not; the render above disproves it.
  - IPv6 at all, because the allow-all rule is `0.0.0.0/0` and therefore matches
    no IPv6 destination — this list is the only way to reach an IPv6 metadata
    endpoint (EKS Pod Identity serves one at fd00:ec2::23) while the policy is on.
  - surrounding whitespace, trimmed here and in what renders. A quoted
    " 169.254.169.254/32 " keeps its padding through YAML and renders a cidr the
    apiserver rejects; trimming is a value fix, not cosmetics.
  - string-typed values, which is all Helm ever delivers: Argo CD's
    helm.parameters and `--set-string` pass every override as a string.

What is refused, and why each one is not just the string the issue named:
  - any prefix below the host width (/16, /24, /0, IPv6 /64) — the reported hole;
  - a prefix above it (/33) or a non-numeric one, which renders clean and is
    rejected by the apiserver after the install reports success;
  - a bare address with no prefix, same clean-render/failed-install shape;
  - an empty or blank entry;
  - anything that is not an address, so a typo is not silently installed;
  - a scalar instead of a list, which is what an unindexed GitOps override
    delivers. `range` over it used to die with "range can't iterate over
    169.254.169.254/32", naming neither the value nor the fix.

An IPv4-mapped IPv6 literal is refused in BOTH of its spellings — the dotted
`::ffff:169.254.169.254/128` and the hex `::ffff:a9fe:a9fe/128`. The dotted one
fell out of the IPv6 shape check (no `.` in its class) and the hex one did not,
so the same address was accepted or refused depending on how it was written.
The plain IPv4 /32 spelling is the one to use.

The refusal lives with the RENDER, not beside it: task-networkpolicy.yaml emits
the peers only through this helper, so a template that stops calling it stops
rendering the exceptions too. A guard the template no longer calls is the hole
that leaves every gate green while the protection is gone.

Not evaluated when taskNetworkPolicy.enabled is false: nothing renders then, so
there is no policy to reopen.
*/}}
{{- define "leoflow.taskMetadataEgressPeers" -}}
{{- $why := "Every entry must name exactly ONE host, because NetworkPolicy egress rules are additive: k8s.io/api networking/v1 defines outgoing traffic as allowed if it matches at least one egress rule, so an allow rule here overrides the 169.254.0.0/16 entry in the allow-all rule's except-list and anything broader than a single host reopens the cloud-metadata range this policy exists to block. Accepted: an IPv4 address with /32 (GKE Workload Identity 169.254.169.254/32, EKS Pod Identity 169.254.170.23/32) or an IPv6 address with /128; surrounding whitespace is trimmed. To reach a wider range that is NOT the metadata range, use taskNetworkPolicy.extraEgress. See #958." -}}
{{- $raw := .Values.taskNetworkPolicy.allowMetadataEgress -}}
{{- if not (kindIs "slice" $raw) -}}
{{- fail (printf "taskNetworkPolicy.allowMetadataEgress must be a list, one CIDR per entry (got %q, kind %s). A scalar arrives when the override is sent unindexed; use list syntax instead: --set taskNetworkPolicy.allowMetadataEgress[0]=169.254.169.254/32, or --set \"taskNetworkPolicy.allowMetadataEgress={169.254.169.254/32,169.254.170.23/32}\". %s" (toString $raw) (kindOf $raw) $why) -}}
{{- end -}}
{{- $peers := list -}}
{{- range $i, $e := $raw -}}
{{- $s := trim (toString $e) -}}
{{- if eq $s "" -}}
{{- fail (printf "taskNetworkPolicy.allowMetadataEgress[%d]=%q is empty. %s" $i $s $why) -}}
{{- end -}}
{{- $parts := splitList "/" $s -}}
{{- if eq (len $parts) 1 -}}
{{- fail (printf "taskNetworkPolicy.allowMetadataEgress[%d]=%q has no prefix length. %s" $i $s $why) -}}
{{- end -}}
{{- if ne (len $parts) 2 -}}
{{- fail (printf "taskNetworkPolicy.allowMetadataEgress[%d]=%q is not an IPv4 or IPv6 address in CIDR form. %s" $i $s $why) -}}
{{- end -}}
{{- $host := index $parts 0 -}}
{{- $prefix := index $parts 1 -}}
{{- $v6 := contains ":" $host -}}
{{- /* IPv4 is validated in full (leading zeros included, which net.ParseIP also
rejects). IPv6 is validated for shape: hex groups, at most one "::", and the
group count a valid address can have. */ -}}
{{- $hostOK := false -}}
{{- if $v6 -}}
{{- $hostOK = and (regexMatch "^[0-9A-Fa-f]{0,4}(:[0-9A-Fa-f]{0,4}){2,7}$" $host) (le (len (splitList "::" $host)) 2) -}}
{{- if and $hostOK (not (contains "::" $host)) -}}
{{- $hostOK = eq (len (splitList ":" $host)) 8 -}}
{{- end -}}
{{- else -}}
{{- $hostOK = regexMatch "^((25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\\.){3}(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])$" $host -}}
{{- end -}}
{{- /* Both spellings of an IPv4-mapped address, so the same host cannot be
       accepted in hex and refused in dotted form. */ -}}
{{- if hasPrefix "::ffff:" (lower $host) -}}
{{- fail (printf "taskNetworkPolicy.allowMetadataEgress[%d]=%q is an IPv4-mapped IPv6 literal; write the address as a plain IPv4 /32 instead. %s" $i $s $why) -}}
{{- end -}}
{{- if not $hostOK -}}
{{- fail (printf "taskNetworkPolicy.allowMetadataEgress[%d]=%q is not an IPv4 or IPv6 address in CIDR form. %s" $i $s $why) -}}
{{- end -}}
{{- if not (regexMatch "^(0|[1-9][0-9]*)$" $prefix) -}}
{{- fail (printf "taskNetworkPolicy.allowMetadataEgress[%d]=%q has a prefix length of /%s, which is not a plain decimal number without leading zeros. %s" $i $s $prefix $why) -}}
{{- end -}}
{{- /* Sprig's atoi discards strconv's error and returns 0, so a prefix too long
       to parse would otherwise fall into the "selects more than one host" branch
       below and be refused for the wrong reason. */ -}}
{{- if gt (len $prefix) 3 -}}
{{- fail (printf "taskNetworkPolicy.allowMetadataEgress[%d]=%q has a prefix length of /%s, which is too long to be one. %s" $i $s $prefix $why) -}}
{{- end -}}
{{- $want := 32 -}}
{{- $family := "IPv4" -}}
{{- if $v6 -}}
{{- $want = 128 -}}
{{- $family = "IPv6" -}}
{{- end -}}
{{- $n := atoi $prefix -}}
{{- if lt $n $want -}}
{{- fail (printf "taskNetworkPolicy.allowMetadataEgress[%d]=%q has prefix /%s, which selects more than one host (%s needs /%d). %s" $i $s $prefix $family $want $why) -}}
{{- end -}}
{{- if gt $n $want -}}
{{- fail (printf "taskNetworkPolicy.allowMetadataEgress[%d]=%q has prefix /%s, which is not a valid %s prefix length (%s needs /%d). %s" $i $s $prefix $family $family $want $why) -}}
{{- end -}}
{{- $peers = append $peers (printf "- ipBlock:\n    cidr: %q" $s) -}}
{{- end -}}
{{- join "\n" $peers -}}
{{- end -}}

{{/*
autoscaling.maxReplicas, validated against autoscaling.minReplicas.

The two are independent values with independent defaults (2 and 6), so
overriding only one silently inverts them: `--set autoscaling.maxReplicas=1`
renders `minReplicas: 2` / `maxReplicas: 1`, which the apiserver rejects
outright. The operator gets a failed install and a message about HPA field
semantics, naming neither leoflow value nor the one they actually set.

This is the class the chart already refuses everywhere else — a render that
looks clean and then fails the install (#905's PDB and strategy guards, #1041's
readiness floor). Refusing at render time puts the diagnosis in front of the
person who typed the value, while they are still typing.

Setting max BELOW min is far likelier than the reverse, because 1 is the
obvious value for someone shrinking a deployment and the min default is 2.
*/}}
{{- define "leoflow.autoscalingBound" -}}
{{- $which := .which -}}
{{- $ctx := .ctx -}}
{{- $rawMin := $ctx.Values.autoscaling.minReplicas -}}
{{- $rawMax := $ctx.Values.autoscaling.maxReplicas -}}
{{- /* Both bounds are validated and RENDERED through here, never raw.
       An earlier version validated `int $v` and rendered the raw value, so
       `minReplicas: 2.9` / `maxReplicas: 2.1` in a values file coerced to 2 and
       2, passed the comparison, and then rendered 2.9 and 2 — the apiserver
       rejects that, which is #947 straight back. The chart already learned this
       once: leoflow.deploymentStrategy's own docstring says it judges the
       validated value the template renders, "not the raw one". */ -}}
{{- range $name, $raw := dict "minReplicas" $rawMin "maxReplicas" $rawMax -}}
{{- if not (kindIs "int" $raw) -}}
{{- if not (regexMatch "^-?[0-9]+$" (toString $raw)) -}}
{{- fail (printf "autoscaling.%s=%v (kind %s) is not a whole number of replicas. A HorizontalPodAutoscaler bound is an int32; a float or a non-numeric string is either rejected by the apiserver or silently truncated. See #947." $name $raw (kindOf $raw)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $min := int $rawMin -}}
{{- $max := int $rawMax -}}
{{- /* min first: a negative min otherwise reports "raise the maximum to at
       least -1", which is advice that produces another invalid HPA. */ -}}
{{- if lt $min 1 -}}
{{- fail (printf "autoscaling.minReplicas=%v, and a HorizontalPodAutoscaler minimum below 1 is rejected unless the HPAScaleToZero feature gate is on, which this chart does not assume. Set it to 1 or more. See #947." $rawMin) -}}
{{- end -}}
{{- if lt $max 1 -}}
{{- fail (printf "autoscaling.maxReplicas=%v, and a HorizontalPodAutoscaler maximum below 1 is meaningless — the target would be scaled to nothing. Set it to at least autoscaling.minReplicas (%v). See #947." $rawMax $rawMin) -}}
{{- end -}}
{{- if gt $max 2147483647 -}}
{{- fail (printf "autoscaling.maxReplicas=%v exceeds the int32 ceiling a HorizontalPodAutoscaler bound is stored in. See #947." $rawMax) -}}
{{- end -}}
{{- if lt $max $min -}}
{{- fail (printf "autoscaling.maxReplicas=%v is below autoscaling.minReplicas=%v, and the apiserver rejects a HorizontalPodAutoscaler whose maximum is under its minimum. The two are independent values with independent defaults (min 2, max 6), so setting only one inverts them — this is what `--set autoscaling.maxReplicas=1` alone produces. Set both, or raise the maximum to at least %v. See #947." $rawMax $rawMin $min) -}}
{{- end -}}
{{- if eq $which "min" -}}{{- $min -}}{{- else -}}{{- $max -}}{{- end -}}
{{- end -}}

{{/*
Name of the ConfigMap carrying the OIDC settings that CANNOT travel as env vars.
Only auth.oidc.tenantClaims and auth.oidc.roleMappings live there: both are maps,
and viper binds env only for the scalar leaves registered in serverDefaults,
these two are deliberately absent from it because a Google `hd` key is a dotted
domain that viper's "." delimiter would split into nested maps (#826). The server
reads them out-of-band from the YAML file named by LEOFLOW_CONFIG
(internal/config/server.go, decodeDottedOIDCMaps), which is the file this
ConfigMap holds.

Suffixed off fullname rather than roleName: the maps are identical for every
role, so the split api and scheduler Deployments mount one object instead of two
copies that could drift apart on a partial upgrade.
*/}}
{{- define "leoflow.oidcConfigMapName" -}}
{{- printf "%s-oidc" (include "leoflow.fullname" .) -}}
{{- end -}}
