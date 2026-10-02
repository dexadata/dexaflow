{{/*
Control-plane Deployment (ADR 0049), parameterized by role so one template
renders the monolith ("all") and the split api/scheduler Deployments. Takes a
dict {ctx, role, replicas}. The "all" role reproduces the pre-split Deployment
byte-for-byte (same name, selector, env, ports, probes); api/scheduler differ
only where the role demands (name suffix, component selector label, role env,
served ports, and the scheduler's health probes on the metrics port since it
serves no HTTP API).
*/}}
{{- define "leoflow.controlPlaneDeployment" -}}
{{- /* One definition of where the OIDC config file lands, because three places
     have to agree on it: the volumeMount, the volume, and the LEOFLOW_CONFIG
     env var the server resolves. A disagreement between them is not a render
     error: it is a boot that reads no file and then fails closed on a tenant
     pin the operator can see configured in the ConfigMap right next to it. */ -}}
{{- $oidcMountPath := "/etc/leoflow/oidc" -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "leoflow.roleName" (dict "ctx" .ctx "role" .role) }}
  namespace: {{ .ctx.Release.Namespace }}
  labels:
    {{- include "leoflow.labels" .ctx | nindent 4 }}
spec:
  replicas: {{ .replicas }}
  {{- /* Update strategy (#868): an explicit value wins; otherwise default to
       Recreate when the logs PVC is ReadWriteOnce, because a RollingUpdate surges
       a second pod that Multi-Attach-deadlocks on the RWO volume (the rollout
       never converges). RWX or an ephemeral emptyDir tolerate RollingUpdate.
       leoflow.deploymentStrategy is the explicit half: validated and TRIMMED, so
       what renders here is what deployment.yaml's refusal judged. Rendering the
       raw value let `RollingUpdate ` past the refusal and into a spec the
       apiserver accepts once YAML drops the space (#905). */ -}}
  {{- $strategy := include "leoflow.deploymentStrategy" .ctx }}
  {{- if not $strategy }}
    {{- $am := .ctx.Values.logs.persistence.accessMode }}
    {{- if and .ctx.Values.logs.persistence.enabled (or (eq $am "ReadWriteOnce") (eq $am "ReadWriteOncePod")) }}
      {{- $strategy = "Recreate" }}
    {{- else }}
      {{- $strategy = "RollingUpdate" }}
    {{- end }}
  {{- end }}
  strategy:
    type: {{ $strategy }}
  selector:
    matchLabels:
      {{- include "leoflow.roleSelectorLabels" (dict "ctx" .ctx "role" .role) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "leoflow.roleSelectorLabels" (dict "ctx" .ctx "role" .role) | nindent 8 }}
      annotations:
        # #316: tie the podTemplate hash to the rendered Secret so `helm
        # upgrade` rolls the pod whenever a Secret-backed value
        # (database.url, redis.url, auth.jwtSecret, secretKey,
        # bootstrap.password) changes. Without this annotation the Secret
        # is updated but K8s leaves the running pod on the OLD values
        # until a manual `kubectl rollout restart`. Note: this only covers
        # the chart-managed Secret; credentials wired via
        # `*.existingSecret` are outside the chart's visibility and still
        # require a manual restart on rotation (callout in chart README).
        checksum/secret: {{ include (print .ctx.Template.BasePath "/secret.yaml") .ctx | sha256sum }}
        {{- if .ctx.Values.auth.oidc.enabled }}
        # Same reason, for the other rendered object a pod's config depends on.
        # The OIDC tenant pin and role mappings live in a mounted ConfigMap (env
        # vars cannot carry a map), and a ConfigMap volume's projection is NOT
        # what the server re-reads: LoadServer parses the file once at boot, so
        # an edited map would sit on disk unread until something restarted the
        # pod. Tying the podTemplate hash to it makes `helm upgrade` do that.
        # Gated on `enabled` so an install without SSO keeps the annotation set
        # it has today rather than gaining a constant-valued one.
        checksum/oidc-config: {{ include (print .ctx.Template.BasePath "/oidc-config.yaml") .ctx | sha256sum }}
        {{- end }}
        {{- with .ctx.Values.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ include "leoflow.roleServiceAccountName" (dict "ctx" .ctx "role" .role) }}
      {{- with .ctx.Values.terminationGracePeriodSeconds }}
      # A voluntary eviction (drain, consolidation, upgrade) sends SIGTERM and
      # waits this long before SIGKILL. What the grace buys is headroom for the
      # HTTP shutdown (in-flight requests get up to 10s to finish) and the
      # dispatch-pool drain (in-flight dispatches settle instead of leaving task
      # instances stuck queued). It does NOT decide leadership handoff: the
      # scheduler lease is released within a tick of SIGTERM and frees anyway
      # when the connection drops. With tasks running, open agent log streams
      # are closed and flushed at SIGTERM and the gRPC stop is bounded (5s
      # graceful, then up to 5s for handlers after the forced stop), so a normal
      # shutdown completes in well under a second. A shutdown that hits every
      # bound (HTTP 10s + drain 15s + gRPC up to 10s) is ~35s and exceeds the
      # default 30s. deployment.preStopSleepSeconds runs inside this same grace
      # but BEFORE SIGTERM, so the grace has to hold both: size it as that sleep
      # plus ~35s, with headroom, when running the object log sink at scale (the
      # HA profile ships 60 alongside a 5s sleep). `with` omits the field for nil,
      # 0 and "" alike, so a default install's pod spec is unchanged and Kubernetes
      # applies its own 30s. An explicit 0 landing there is deliberate, not a
      # dropped value: a literal 0 in the spec is SIGKILL with nothing drained —
      # HTTP cut mid-request, the dispatch pool never settling, log streams never
      # flushed — and it makes any preStop sleep unsatisfiable. The chart's 0 means
      # "the Kubernetes default applies", and the render guard in deployment.yaml
      # reasons about that same 30 (#905).
      terminationGracePeriodSeconds: {{ . }}
      {{- end }}
      {{- with .ctx.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        {{- toYaml .ctx.Values.podSecurityContext | nindent 8 }}
      containers:
        - name: leoflow-server
          image: {{ include "leoflow.image" .ctx | quote }}
          imagePullPolicy: {{ .ctx.Values.image.pullPolicy }}
          securityContext:
            {{- toYaml .ctx.Values.securityContext | nindent 12 }}
          ports:
            {{- if or (eq .role "all") (eq .role "api") }}
            - name: http
              containerPort: {{ .ctx.Values.ports.http }}
            {{- end }}
            - name: metrics
              containerPort: {{ .ctx.Values.ports.metrics }}
            {{- if or (eq .role "all") (eq .role "scheduler") }}
            - name: grpc
              containerPort: {{ .ctx.Values.ports.grpc }}
            {{- end }}
          env:
            {{- if ne .role "all" }}
            # ADR 0049 — select this process's role. Omitted for the "all"
            # Deployment so its rendered env is identical to the pre-split chart.
            - name: LEOFLOW_SERVER_ROLE
              value: {{ .role | quote }}
            {{- end }}
            # Mark this deployment as the Pro edition. The control plane uses
            # this signal for two things: (a) refuse the
            # LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true dev escape hatch at boot
            # (#58 / ADR 0014) so a Pro install cannot accidentally ship secrets
            # over a plaintext gRPC channel; (b) inject the gold PRO badge into
            # the served SPA shell, mirroring Lite's silver LITE pill.
            - name: LEOFLOW_UI_EDITION
              value: "pro"
            {{- with .ctx.Values.ui.autoRefreshIntervalSeconds }}
            # Omitted entirely when unset, so the server's own default decides.
            # Rendering an empty string here would bind the variable to "" and
            # send the operator looking at the chart for a value the chart did
            # not actually choose.
            - name: LEOFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS
              value: {{ . | quote }}
            {{- end }}
            - name: LEOFLOW_SERVER_HTTP_ADDR
              value: ":{{ .ctx.Values.ports.http }}"
            - name: LEOFLOW_SERVER_METRICS_ADDR
              value: ":{{ .ctx.Values.ports.metrics }}"
            - name: LEOFLOW_SERVER_GRPC_ADDR
              value: ":{{ .ctx.Values.ports.grpc }}"
            - name: LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR
              value: {{ include "leoflow.agentControlPlaneAddr" .ctx | quote }}
            # The namespace the control plane creates task pods + staging PVCs in.
            # MUST equal the namespace the executor Role/RoleBinding are granted in
            # (rbac.yaml, also .Values.taskNamespace) or every dispatch 403s (#480).
            - name: LEOFLOW_EXECUTOR_TASK_NAMESPACE
              value: {{ .ctx.Values.taskNamespace | quote }}
            {{- if .ctx.Values.config.cors.allowedOrigins }}
            # CORS origins the API accepts (server.cors.allowed_origins, #1144).
            # Comma-joined for the same reason as trusted_proxies below: the chart
            # ships no server config file, and viper's decode hook splits the single
            # env var back into a list. Omitted when empty, which leaves the server
            # default in place. This key existed in values.yaml with no consumer at
            # all until #1144, so anything set here before that was silently ignored.
            - name: LEOFLOW_SERVER_CORS_ALLOWED_ORIGINS
              value: {{ join "," .ctx.Values.config.cors.allowedOrigins | quote }}
            {{- end }}
            {{- if .ctx.Values.config.trustedProxies }}
            # Proxy IPs/CIDRs whose X-Forwarded-For the server honors (#725).
            # Rendered comma-joined because the chart ships no server config file
            # and env is the only override path; viper's decode hook splits the
            # single env var back into server.trusted_proxies ([]string). Without
            # it the per-IP login limiter keys on the ingress IP and one client's
            # bad logins lock out every user. Omitted when empty (trust none).
            - name: LEOFLOW_SERVER_TRUSTED_PROXIES
              value: {{ join "," .ctx.Values.config.trustedProxies | quote }}
            {{- end }}
            {{- if .ctx.Values.executor.defaults.resources.cpu }}
            # L0 per-cluster CPU default (ADR 0023). The server applies it as both
            # request and limit (#725). Guaranteed QoS needs the MEMORY default set
            # too — cpu alone is Burstable, and the server WARNs at boot (#802).
            - name: LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_CPU
              value: {{ .ctx.Values.executor.defaults.resources.cpu | quote }}
            {{- end }}
            {{- if .ctx.Values.executor.defaults.resources.memory }}
            # L0 per-cluster memory default (ADR 0023), request == limit (#725).
            # Pairs with the cpu default above; either one alone is Burstable.
            - name: LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_MEMORY
              value: {{ .ctx.Values.executor.defaults.resources.memory | quote }}
            {{- end }}
            {{- if .ctx.Values.executor.defaults.staging.size }}
            # L0 per-cluster staging-volume size default (ADR 0023). Env is the only
            # override path since the chart ships no server config file (#743).
            - name: LEOFLOW_EXECUTOR_DEFAULTS_STAGING_SIZE
              value: {{ .ctx.Values.executor.defaults.staging.size | quote }}
            {{- end }}
            {{- if .ctx.Values.executor.defaults.staging.storageClass }}
            # L0 per-cluster staging-volume StorageClass default (ADR 0023, #743).
            - name: LEOFLOW_EXECUTOR_DEFAULTS_STAGING_STORAGE_CLASS
              value: {{ .ctx.Values.executor.defaults.staging.storageClass | quote }}
            {{- end }}
            - name: LEOFLOW_LOGS_DIR
              value: {{ .ctx.Values.config.logsDir | quote }}
            {{- if ne .ctx.Values.logs.sink.provider "disk" }}
            # Object-store log backend (opt-in, ADR 0035/0056 keyless-first). With
            # provider s3 or gcs, task logs ship to a bucket instead of the PVC; set
            # logs.persistence.enabled=false to drop the (RWX) log PVC entirely.
            # Keyless: bind this control-plane ServiceAccount to a cloud identity
            # (IRSA for s3, Workload Identity for gcs) via serviceAccount.annotations
            # and leave the credential fields empty.
            - name: LEOFLOW_LOGS_BACKEND
              value: {{ .ctx.Values.logs.sink.provider | quote }}
            - name: LEOFLOW_LOGS_SINK_BUCKET
              value: {{ .ctx.Values.logs.sink.bucket | quote }}
            - name: LEOFLOW_LOGS_SINK_PREFIX
              value: {{ .ctx.Values.logs.sink.prefix | quote }}
            {{- if eq .ctx.Values.logs.sink.provider "s3" }}
            - name: LEOFLOW_LOGS_SINK_REGION
              value: {{ .ctx.Values.logs.sink.region | quote }}
            - name: LEOFLOW_LOGS_SINK_ENDPOINT
              value: {{ .ctx.Values.logs.sink.endpoint | quote }}
            - name: LEOFLOW_LOGS_SINK_FORCE_PATH_STYLE
              value: {{ .ctx.Values.logs.sink.forcePathStyle | quote }}
            {{- if .ctx.Values.logs.sink.existingSecret }}
            - name: LEOFLOW_LOGS_SINK_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.logs.sink.existingSecret }}
                  key: accessKeyId
            - name: LEOFLOW_LOGS_SINK_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.logs.sink.existingSecret }}
                  key: secretAccessKey
            {{- end }}
            {{- end }}
            {{- end }}
            - name: LEOFLOW_SCHEDULER_ENABLED
              value: {{ .ctx.Values.config.scheduler.enabled | quote }}
            - name: LEOFLOW_SCHEDULER_LOOP_INTERVAL_MS
              value: {{ .ctx.Values.config.scheduler.loopIntervalMs | quote }}
            - name: LEOFLOW_DATABASE_MAX_OPEN_CONNS
              value: {{ .ctx.Values.database.maxOpenConns | quote }}
            - name: LEOFLOW_DATABASE_MAX_IDLE_CONNS
              value: {{ .ctx.Values.database.maxIdleConns | quote }}
            - name: LEOFLOW_AUTH_JWT_TOKEN_TTL_SECONDS
              value: {{ .ctx.Values.auth.tokenTtlSeconds | quote }}
            - name: LEOFLOW_OBSERVABILITY_LOG_FORMAT
              value: {{ .ctx.Values.observability.logFormat | quote }}
            - name: LEOFLOW_OBSERVABILITY_LOG_LEVEL
              value: {{ .ctx.Values.observability.logLevel | quote }}
            {{- if .ctx.Values.observability.otel.enabled }}
            - name: LEOFLOW_OBSERVABILITY_OTEL_ENABLED
              value: "true"
            - name: LEOFLOW_OBSERVABILITY_OTEL_ENDPOINT
              value: {{ .ctx.Values.observability.otel.endpoint | quote }}
            {{- end }}
            {{- if .ctx.Values.agentTLS.enabled }}
            # TLS on the agent gRPC channel (issue #58). The server cert is
            # mounted from agentTLS.serverCertSecret; the agent CA is delivered to
            # task pods via agentTLS.caConfigMap (the dispatcher mounts + selects it).
            - name: LEOFLOW_SERVER_GRPC_TLS_CERT
              value: /etc/leoflow/grpc-tls/tls.crt
            - name: LEOFLOW_SERVER_GRPC_TLS_KEY
              value: /etc/leoflow/grpc-tls/tls.key
            - name: LEOFLOW_EXECUTOR_AGENT_TLS_CA_CONFIGMAP
              value: {{ include "leoflow.agentTLS.caConfigMapName" .ctx | quote }}
            {{- end }}
            {{- if .ctx.Values.taskSecret.name }}
            # Mount a Kubernetes Secret read-only into every task pod so a task can
            # read a credential (e.g. a GCP service-account key referenced by a
            # connection's key_path) from the cluster's secret store — Leoflow
            # never stores the key itself (ADR 0035).
            - name: LEOFLOW_EXECUTOR_TASK_SECRET_NAME
              value: {{ .ctx.Values.taskSecret.name | quote }}
            - name: LEOFLOW_EXECUTOR_TASK_SECRET_MOUNT_PATH
              value: {{ .ctx.Values.taskSecret.mountPath | quote }}
            {{- end }}
            {{- if .ctx.Values.taskServiceAccount.create }}
            # Default task pods to the chart-created task ServiceAccount when a DAG
            # does not set execution.service_account, so keyless (IRSA / Workload
            # Identity, ADR 0035/0060) works without every DAG opting in. An explicit
            # per-task execution.service_account still wins.
            - name: LEOFLOW_EXECUTOR_TASK_SERVICE_ACCOUNT
              value: {{ .ctx.Values.taskServiceAccount.name | quote }}
            {{- end }}
            {{- if .ctx.Values.secrets.backend }}
            # External secrets backend (ADR 0060): a declared Connection/Variable is
            # resolved pod-side from the provider store under the pod's own keyless
            # identity, instead of Leoflow's vault. Operator-only — delivered to task
            # pods as LEOFLOW_SECRETS_*, which an author's task env can never set.
            - name: LEOFLOW_SECRETS_BACKEND
              value: {{ .ctx.Values.secrets.backend | quote }}
            - name: LEOFLOW_SECRETS_BACKEND_KWARGS
              value: {{ .ctx.Values.secrets.backendKwargs | quote }}
            {{- end }}
            # Task-pod hardening. Always stamped, both directions: leaving the
            # secure value implicit would mean a chart upgrade could not turn an
            # opt-out back off without an operator noticing.
            - name: LEOFLOW_EXECUTOR_DEFAULTS_RUN_TASKS_AS_NON_ROOT
              value: {{ .ctx.Values.taskPodSecurity.runAsNonRoot | quote }}
            - name: LEOFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM
              value: {{ .ctx.Values.taskPodSecurity.readOnlyRootFilesystem | quote }}
            # Agent-credential posture (ADR 0055) and warm worker pools (ADR 0058).
            # These three are always stamped, at their defaults too, for the same
            # reason as the task-pod hardening above: they are the security
            # coupling ADR 0058 D2 rests on, so leaving a default implicit would
            # let a hand-applied `kubectl set env` survive a `helm upgrade`
            # unnoticed. Stamping them makes the chart the single source of truth
            # and lets an upgrade reassert the safe value.
            - name: LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT
              value: {{ .ctx.Values.auth.agentTokenTransport | quote }}
            - name: LEOFLOW_AUTH_SECRET_LIVENESS_MODE
              value: {{ .ctx.Values.auth.secretLivenessMode | quote }}
            # The scope-by-declaration policy (ADR 0055 D9) is stamped alongside
            # them for a different reason: it is coupled to nothing, but it is the
            # knob that decides whether a task receives the whole tenant vault, and
            # before #803 `extraEnv` was its only route — so values.yaml, the file
            # an operator reads to find what is tunable, did not show it or its
            # default. It is deliberately NOT in deployment.yaml's $guarded list:
            # that list is for the variables the chart validates against each
            # other, and guarding this one would fail the render for every operator
            # who already set it through extraEnv. extraEnv renders after this
            # block, so last-wins keeps them working (tests/secret_scoping_test.yaml).
            - name: LEOFLOW_AUTH_SECRET_SCOPING
              value: {{ .ctx.Values.auth.secretScoping | quote }}
            - name: LEOFLOW_EXECUTION_WARM_POOLS_ENABLED
              value: {{ .ctx.Values.execution.warmPoolsEnabled | quote }}
            {{- if .ctx.Values.execution.warmPoolsEnabled }}
            # The D6/D9/M4 pool bounds. Stamped only with the flag on: the server
            # reads none of them while warm pools are off (its boot validation
            # returns before they are looked at), so an OFF install keeps exactly
            # the env it has today. Durations travel as strings — viper parses
            # them, the same way it does the auth credential ceiling.
            - name: LEOFLOW_EXECUTION_MIN_IDLE_WORKERS
              value: {{ .ctx.Values.execution.minIdleWorkers | quote }}
            - name: LEOFLOW_EXECUTION_MAX_POOL_SIZE
              value: {{ .ctx.Values.execution.maxPoolSize | quote }}
            - name: LEOFLOW_EXECUTION_MAX_ATTEMPTS_PER_WORKER
              value: {{ .ctx.Values.execution.maxAttemptsPerWorker | quote }}
            - name: LEOFLOW_EXECUTION_MAX_WORKER_LIFETIME
              value: {{ .ctx.Values.execution.maxWorkerLifetime | quote }}
            - name: LEOFLOW_EXECUTION_WORKER_IDLE_TTL
              value: {{ .ctx.Values.execution.workerIdleTtl | quote }}
            - name: LEOFLOW_EXECUTION_MAX_WARM_PODS_PER_TENANT
              value: {{ .ctx.Values.execution.maxWarmPodsPerTenant | quote }}
            {{- end }}
            - name: LEOFLOW_DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.database.existingSecret | default (include "leoflow.credentialsSecretName" .ctx) }}
                  key: databaseUrl
            - name: LEOFLOW_REDIS_URL
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.redis.existingSecret | default (include "leoflow.credentialsSecretName" .ctx) }}
                  key: redisUrl
            {{- if .ctx.Values.redis.caConfigMap }}
            # #312 — Verified TLS to managed Redis (Memorystore
            # SERVER_AUTHENTICATION, ElastiCache in-transit, Azure Cache).
            # The CA bundle from the named ConfigMap is mounted read-only at
            # /etc/leoflow/redis-ca/ca.crt and the server is told where to
            # find it via LEOFLOW_REDIS_CA_FILE.
            - name: LEOFLOW_REDIS_CA_FILE
              value: /etc/leoflow/redis-ca/ca.crt
            {{- end }}
            - name: LEOFLOW_AUTH_JWT_SECRET
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.auth.existingSecret | default (include "leoflow.credentialsSecretName" .ctx) }}
                  key: jwtSecret
            {{- if .ctx.Values.auth.oidc.enabled }}
            # OIDC/SSO (#1143). The scalars ride the env path the rest of this
            # chart uses; the two MAPS (tenant_claims, role_mappings) cannot,
            # viper binds env only for the scalar leaves in serverDefaults, and
            # those two are deliberately unregistered because a Google `hd` key
            # is a dotted domain its "." delimiter would split (#826). They are
            # in the ConfigMap mounted below, and LEOFLOW_CONFIG names it.
            #
            # Mixing the two routes is safe in exactly one direction, which is
            # this one: viper ranks env ABOVE the config file, so every scalar
            # here wins over anything the file might say, and a key the file
            # omits keeps its default. That is why the file holds only the maps.
            #
            # LEOFLOW_UI_EDITION is the literal "pro" above and validateOIDC
            # requires the Pro edition, so this chart satisfies that half by
            # construction.
            - name: LEOFLOW_AUTH_PROVIDER
              value: "oidc"
            - name: LEOFLOW_CONFIG
              value: {{ printf "%s/config.yaml" $oidcMountPath | quote }}
            - name: LEOFLOW_AUTH_OIDC_ISSUER
              value: {{ .ctx.Values.auth.oidc.issuer | quote }}
            - name: LEOFLOW_AUTH_OIDC_CLIENT_ID
              value: {{ .ctx.Values.auth.oidc.clientId | quote }}
            - name: LEOFLOW_AUTH_OIDC_REDIRECT_URL
              value: {{ .ctx.Values.auth.oidc.redirectUrl | quote }}
            - name: LEOFLOW_AUTH_OIDC_TENANT_CLAIM
              value: {{ .ctx.Values.auth.oidc.tenantClaim | quote }}
            # Comma-joined for the same reason as config.trustedProxies: one env
            # var, and viper's StringToSliceHookFunc(",") splits it back into the
            # []string the server wants.
            - name: LEOFLOW_AUTH_OIDC_SCOPES
              value: {{ join "," .ctx.Values.auth.oidc.scopes | quote }}
            - name: LEOFLOW_AUTH_OIDC_GROUPS_CLAIM
              value: {{ .ctx.Values.auth.oidc.groupsClaim | quote }}
            - name: LEOFLOW_AUTH_OIDC_JIT_PROVISIONING
              value: {{ .ctx.Values.auth.oidc.jitProvisioning | quote }}
            - name: LEOFLOW_AUTH_OIDC_AUTO_REDIRECT
              value: {{ .ctx.Values.auth.oidc.autoRedirect | quote }}
            - name: LEOFLOW_AUTH_OIDC_CLOCK_SKEW_SECONDS
              value: {{ .ctx.Values.auth.oidc.clockSkewSeconds | quote }}
            {{- if .ctx.Values.auth.oidc.defaultRole }}
            # The three below are omitted when empty rather than stamped with a
            # blank: each has a meaning at its zero value that the server already
            # implements (strict default-deny; no domain restriction; no password
            # login at all while SSO is on), and stamping "" would only add a
            # variable that says what the default already says.
            - name: LEOFLOW_AUTH_OIDC_DEFAULT_ROLE
              value: {{ .ctx.Values.auth.oidc.defaultRole | quote }}
            {{- end }}
            {{- if .ctx.Values.auth.oidc.allowedEmailDomains }}
            - name: LEOFLOW_AUTH_OIDC_ALLOWED_EMAIL_DOMAINS
              value: {{ join "," .ctx.Values.auth.oidc.allowedEmailDomains | quote }}
            {{- end }}
            {{- if .ctx.Values.auth.oidc.breakGlassEmails }}
            - name: LEOFLOW_AUTH_OIDC_BREAK_GLASS_EMAILS
              value: {{ join "," .ctx.Values.auth.oidc.breakGlassEmails | quote }}
            {{- end }}
            {{- if or .ctx.Values.auth.oidc.existingSecret .ctx.Values.auth.oidc.clientSecret }}
            # Code-exchange credential only, ID-token verification is keyless
            # against the issuer's JWKS. Delivered by secretKeyRef, never in the
            # ConfigMap. Optional: a public client using PKCE has none, and
            # validateOIDC does not require it.
            - name: LEOFLOW_AUTH_OIDC_CLIENT_SECRET
              valueFrom:
                secretKeyRef:
                  # The DURABLE Secret, not the hook one. #1142 split them: the hook
                  # copy exists only to feed the pre-install migration Job, carries
                  # databaseUrl alone, and is deleted when that hook succeeds. A
                  # Deployment reading from it lands in CreateContainerConfigError,
                  # which is the exact outage #1142 was filed for.
                  name: {{ .ctx.Values.auth.oidc.existingSecret | default (include "leoflow.credentialsSecretName" .ctx) }}
                  key: oidcClientSecret
            {{- end }}
            {{- end }}
            {{- if or .ctx.Values.secretKeyExistingSecret .ctx.Values.secretKey }}
            # LEOFLOW_SECRET_KEY (ADR 0019) — Connection password / Extra
            # encryption-at-rest key. Without it, the API refuses Connection
            # writes (Variables still work). Optional so users who only use
            # Variables can omit it; recommended for any real Pro install.
            - name: LEOFLOW_SECRET_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.secretKeyExistingSecret | default (include "leoflow.credentialsSecretName" .ctx) }}
                  key: secretKey
            {{- end }}
            {{- if or .ctx.Values.bootstrap.existingSecret .ctx.Values.bootstrap.password }}
            - name: LEOFLOW_BOOTSTRAP_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: {{ .ctx.Values.bootstrap.existingSecret | default (include "leoflow.credentialsSecretName" .ctx) }}
                  key: bootstrapPassword
            {{- end }}
            {{- if .ctx.Values.extraEnv }}
            # Operator escape hatch for LEOFLOW_* settings the chart does not
            # model as a value. Appended last, and never a way around a guard:
            # deployment.yaml refuses to render an entry that shadows one of the
            # variables whose values the chart validates against each other.
            #
            # Rendered field by field rather than dumped with toYaml so `value` is
            # always quoted: a Kubernetes env value is a string, and a YAML number
            # or bool (which is what `--set extraEnv[0].value=4` produces) is
            # rejected by the apiserver on apply — long after the render looked
            # fine. name/value/valueFrom is the whole of an EnvVar, so nothing is
            # dropped by being explicit.
            {{- range .ctx.Values.extraEnv }}
            - name: {{ .name | quote }}
              {{- if hasKey . "value" }}
              value: {{ .value | quote }}
              {{- end }}
              {{- if hasKey . "valueFrom" }}
              valueFrom:
                {{- toYaml .valueFrom | nindent 16 }}
              {{- end }}
            {{- end }}
            {{- end }}
          {{- $probePort := "http" }}
          {{- if eq .role "scheduler" }}{{ $probePort = "metrics" }}{{ end }}
          {{- if and .ctx.Values.probes.startup .ctx.Values.probes.startup.enabled }}
          # Gate the other two probes on boot completing. Liveness and readiness
          # both target the API listener, which binds at the END of boot, so
          # without this the kubelet answers a slow or stuck boot by restarting
          # the container: the loop #1083 produced, where every cycle exits 0 and
          # names nothing. A startupProbe makes that state "not ready" instead,
          # and keeps the restart for a boot that overruns the whole budget.
          startupProbe:
            httpGet:
              path: /healthz
              port: {{ $probePort }}
            periodSeconds: {{ .ctx.Values.probes.startup.periodSeconds }}
            failureThreshold: {{ .ctx.Values.probes.startup.failureThreshold }}
          {{- end }}
          readinessProbe:
            httpGet:
              path: /readyz
              port: {{ $probePort }}
            initialDelaySeconds: {{ .ctx.Values.probes.readiness.initialDelaySeconds }}
            periodSeconds: {{ .ctx.Values.probes.readiness.periodSeconds }}
            timeoutSeconds: {{ include "leoflow.readinessTimeoutSeconds" .ctx }}
            failureThreshold: {{ .ctx.Values.probes.readiness.failureThreshold }}
          livenessProbe:
            httpGet:
              path: /healthz
              port: {{ $probePort }}
            initialDelaySeconds: {{ .ctx.Values.probes.liveness.initialDelaySeconds }}
            periodSeconds: {{ .ctx.Values.probes.liveness.periodSeconds }}
            timeoutSeconds: {{ .ctx.Values.probes.liveness.timeoutSeconds }}
            failureThreshold: {{ .ctx.Values.probes.liveness.failureThreshold }}
          {{- /* leoflow.preStopSleepHookSupported holds the capability test and the
               reasoning behind it: the native `sleep` action is gated on
               PodLifecycleSleepAction, and below 1.30 rendering the hook REJECTS
               the Deployment rather than being silently dropped, so the render is
               gated instead of Chart.yaml's kubeVersion (#918). NOTES.txt asks the
               same helper, so what it warns about and what this renders can never
               disagree. No `exec` fallback: the image is distroless, so a command
               hook has no shell and no sleep binary to call. */ -}}
          {{- $sleepHookSupported := eq (include "leoflow.preStopSleepHookSupported" .ctx) "true" }}
          {{- if and (gt (int .ctx.Values.deployment.preStopSleepSeconds) 0) $sleepHookSupported }}
          # Endpoint removal is asynchronous. From the moment this replica starts
          # terminating, kube-proxy and every already-connected client still hold
          # it in their endpoint set for a propagation window, so task pods keep
          # OPENING new agent log streams against a control plane that is on its
          # way out — and each one creates an empty log object for an attempt
          # whose lines then have nowhere to land. Sleeping here spends that
          # window before the process is signalled, so those streams land on a
          # replica that will still be alive to flush them. The sleep runs INSIDE
          # terminationGracePeriodSeconds, ahead of SIGTERM, so it adds to the
          # shutdown budget; keep the grace above preStop + the drain bounds.
          lifecycle:
            preStop:
              sleep:
                seconds: {{ int .ctx.Values.deployment.preStopSleepSeconds }}
          {{- end }}
          resources:
            {{- toYaml .ctx.Values.resources | nindent 12 }}
          volumeMounts:
            - name: logs
              mountPath: {{ .ctx.Values.config.logsDir }}
            {{- if and .ctx.Values.agentTLS.enabled (ne .role "api") }}
            # #726 — the private key is mounted only into the role that runs the
            # agent gRPC server. The api role never builds a gRPC server
            # (startAgentGRPC is reached only from the scheduler side), so mounting
            # tls.key into its internet-facing pod only widens the blast radius
            # ADR 0049 set out to shrink. The env vars above stay on every role:
            # the Pro boot guard (guardTLSForEdition) checks only that both strings
            # are non-empty, never reading the files, so the dangling path is
            # harmless on api while scoping the env would CrashLoopBackOff it.
            - name: grpc-tls
              mountPath: /etc/leoflow/grpc-tls
              readOnly: true
            {{- end }}
            {{- if .ctx.Values.database.caConfigMap }}
            # Managed-Postgres CA bundle (#315). The DSN references this path
            # via `sslmode=verify-full&sslrootcert=/etc/leoflow/db-ca/ca.crt` —
            # pgx reads sslrootcert natively, so no Go-side wiring is needed.
            - name: db-ca
              mountPath: /etc/leoflow/db-ca
              readOnly: true
            {{- end }}
            {{- if .ctx.Values.redis.caConfigMap }}
            - name: redis-ca
              mountPath: /etc/leoflow/redis-ca
              readOnly: true
            {{- end }}
            {{- if .ctx.Values.auth.oidc.enabled }}
            # The only server config FILE this chart mounts, and it holds only
            # the two OIDC maps no env var can carry (see the env block above).
            # Read-only: the server parses it once at boot and never writes it.
            - name: oidc-config
              mountPath: {{ $oidcMountPath }}
              readOnly: true
            {{- end }}
      volumes:
        - name: logs
          {{- if .ctx.Values.logs.persistence.enabled }}
          persistentVolumeClaim:
            claimName: {{ include "leoflow.fullname" .ctx }}-logs
          {{- else }}
          # Dev-only fallback: logs vanish on pod restart. Enable
          # `logs.persistence.enabled` for durable storage (#227).
          emptyDir: {}
          {{- end }}
        {{- if and .ctx.Values.agentTLS.enabled (ne .role "api") }}
        # #726 — see the matching volumeMount guard above: the api role omits the
        # gRPC cert Secret volume entirely so tls.key never reaches its pod.
        - name: grpc-tls
          secret:
            # BYO serverCertSecret, or the chart-generated "<fullname>-agent-tls"
            # when auto-gen is active (#690). The helper resolves which; `required`
            # only trips in the impossible-by-guard case of neither (deployment.yaml
            # fails first with a clearer message).
            secretName: {{ required "agentTLS: no server cert Secret resolved (set agentTLS.serverCertSecret, or keep agentTLS.autoGenerate=true)" (include "leoflow.agentTLS.serverCertSecretName" .ctx) }}
        {{- end }}
        {{- if .ctx.Values.database.caConfigMap }}
        - name: db-ca
          configMap:
            name: {{ .ctx.Values.database.caConfigMap }}
            items:
              - key: ca.crt
                path: ca.crt
        {{- end }}
        {{- if .ctx.Values.redis.caConfigMap }}
        - name: redis-ca
          configMap:
            name: {{ .ctx.Values.redis.caConfigMap }}
        {{- end }}
        {{- if .ctx.Values.auth.oidc.enabled }}
        # `items` pins the projection to the single key the server is pointed at,
        # so a future key added to this ConfigMap for some other purpose cannot
        # appear inside the directory LEOFLOW_CONFIG resolves against.
        - name: oidc-config
          configMap:
            name: {{ include "leoflow.oidcConfigMapName" .ctx }}
            items:
              - key: config.yaml
                path: config.yaml
        {{- end }}
      {{- with .ctx.Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .ctx.Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .ctx.Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .ctx.Values.topologySpreadConstraints }}
      # Spread the replicas: consolidation bin-packs both onto one node unless
      # told otherwise, and two replicas on one node are one replica — a node
      # failure takes the whole control plane, and the PDB (one of two must stay
      # up, both on the drained node) then blocks the eviction. A constraint that
      # omits labelSelector gets this Deployment's own selector, so a values file
      # need not know the release name or the role.
      topologySpreadConstraints:
        {{- range . }}
        - labelSelector:
            {{- if .labelSelector }}
            {{- toYaml .labelSelector | nindent 12 }}
            {{- else }}
            matchLabels:
              {{- include "leoflow.roleSelectorLabels" (dict "ctx" $.ctx "role" $.role) | nindent 14 }}
            {{- end }}
          {{- omit . "labelSelector" | toYaml | nindent 10 }}
        {{- end }}
      {{- end }}

{{- end -}}
