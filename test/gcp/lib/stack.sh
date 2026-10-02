#!/usr/bin/env bash
#
# Bring a leoflow control plane up on a throwaway GKE cluster, the same shape
# that was installed by hand on k3d: Postgres and Redis as plain Deployments,
# then `helm install` against the published server image.
#
# WHAT THIS IS NOT. These datastores are Deployments with `emptyDir` storage and
# one replica. They are not a reference deployment and nothing here should be
# copied into one: a restarted Postgres pod loses the database. That is correct
# for a bounded experiment whose cluster is deleted at the end, and it is a data
# loss bug anywhere else. It is also a thing the experiments CANNOT see: nothing
# below measures a managed Postgres, its connection limits, or its latency, so
# no result from these runners says anything about either.
#
# ONE CORRECTION TO THE RECIPE. The hand install was described as passing
# `auth.agentTLS.enabled=false`. That value cannot install the 0.4.7 chart: the
# chart FAILS THE RENDER on it, by design, at
# helm/dexaflow/templates/deployment.yaml:41 (added in b5aea55, present in the
# v0.4.7 tag). The message is explicit that the Pro edition refuses to boot
# without a gRPC cert, so turning TLS off yields a CrashLoopBackOff rather than
# a plaintext deployment, and the chart would rather refuse than render that.
# So this file uses `agentTLS.autoGenerate=true`, which is the chart default and
# needs neither cert-manager nor a pre-created Secret.
#
# Sourced, never executed, except for --self-test.

STACK_NS="${STACK_NS:-leoflow-system}"      # control plane
STACK_TASK_NS="${STACK_TASK_NS:-leoflow}"   # task pods (chart's taskNamespace)
STACK_SERVER_IMAGE_REPO="${STACK_SERVER_IMAGE_REPO:-ghcr.io/dexadata/dexaflow-server}"
STACK_SERVER_IMAGE_TAG="${STACK_SERVER_IMAGE_TAG:-0.4.7}"
# The published Postgres/Redis the chart is normally pointed at. Pinned by tag
# rather than `latest`: an experiment whose datastore version changes between
# runs is an experiment whose runs cannot be compared.
STACK_PG_IMAGE="${STACK_PG_IMAGE:-postgres:16-alpine}"
STACK_REDIS_IMAGE="${STACK_REDIS_IMAGE:-redis:7-alpine}"

# ------------------------------------------------------------- datastores

# stack_datastores_manifest prints the Postgres + Redis manifests. A function
# that prints rather than applies, so the render is testable with no cluster
# (and so a reader can see exactly what lands).
stack_datastores_manifest() { # <namespace>
  local ns="$1"
  cat <<YAML
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: $ns
spec:
  selector: { app: postgres }
  ports: [{ port: 5432, targetPort: 5432 }]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
  namespace: $ns
spec:
  replicas: 1
  selector: { matchLabels: { app: postgres } }
  template:
    metadata:
      labels: { app: postgres }
    spec:
      containers:
        - name: postgres
          image: $STACK_PG_IMAGE
          env:
            - { name: POSTGRES_USER,     value: leoflow }
            - { name: POSTGRES_PASSWORD, value: leoflow }
            - { name: POSTGRES_DB,       value: leoflow }
            # The data directory is a subdirectory of the mount on purpose:
            # initdb refuses a non-empty directory, and a bare emptyDir mount at
            # PGDATA is empty until something like a lost+found appears.
            - { name: PGDATA,            value: /var/lib/postgresql/data/pgdata }
          ports: [{ containerPort: 5432 }]
          # Requests AND limits, equal, on both dimensions: that is Guaranteed
          # QoS. A BestEffort Postgres is first out under the node pressure
          # these experiments deliberately create, and a datastore evicted
          # mid-run would be read as the control plane saturating.
          resources:
            requests: { cpu: "500m", memory: "512Mi" }
            limits:   { cpu: "500m", memory: "512Mi" }
          readinessProbe:
            exec: { command: ["pg_isready", "-U", "leoflow", "-d", "leoflow"] }
            initialDelaySeconds: 5
            periodSeconds: 3
          volumeMounts: [{ name: data, mountPath: /var/lib/postgresql/data }]
      volumes: [{ name: data, emptyDir: {} }]
---
apiVersion: v1
kind: Service
metadata:
  name: redis
  namespace: $ns
spec:
  selector: { app: redis }
  ports: [{ port: 6379, targetPort: 6379 }]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
  namespace: $ns
spec:
  replicas: 1
  selector: { matchLabels: { app: redis } }
  template:
    metadata:
      labels: { app: redis }
    spec:
      containers:
        - name: redis
          image: $STACK_REDIS_IMAGE
          ports: [{ containerPort: 6379 }]
          resources:
            requests: { cpu: "250m", memory: "256Mi" }
            limits:   { cpu: "250m", memory: "256Mi" }
          readinessProbe:
            exec: { command: ["redis-cli", "ping"] }
            initialDelaySeconds: 3
            periodSeconds: 3
YAML
}

# ------------------------------------------------------------- helm values

# stack_helm_args builds the `helm install` argv. In a function, never inline,
# for the same reason provision.sh builds its gcloud argv in one: it is the only
# way the self-test can check a value key before a cluster does. That check has
# already earned its place once here, on agentTLS.
stack_helm_args() { # <release> <namespace> <task namespace> <jwt secret> <bootstrap password> [extra --set args...]
  local release="$1" ns="$2" taskns="$3" jwt="$4" pw="$5"; shift 5
  HELM_ARGS=(upgrade --install "$release" "${STACK_CHART:-./helm/dexaflow}"
    --namespace "$ns" --create-namespace
    --set "image.repository=$STACK_SERVER_IMAGE_REPO"
    --set "image.tag=$STACK_SERVER_IMAGE_TAG"
    --set "migrations.image.tag=$STACK_SERVER_IMAGE_TAG"
    --set "taskNamespace=$taskns"
    --set "database.url=postgres://leoflow:leoflow@postgres.$ns.svc.cluster.local:5432/leoflow?sslmode=disable"
    --set "redis.url=redis://redis.$ns.svc.cluster.local:6379/0"
    --set-string "auth.jwtSecret=$jwt"
    --set-string "bootstrap.password=$pw"
    # NOT agentTLS.enabled=false: see the header. autoGenerate is the chart
    # default and mints a self-signed CA with no cert-manager.
    --set "auth.agentTLS.enabled=true"
    --set "auth.agentTLS.autoGenerate=true"
    --wait --timeout "${STACK_HELM_TIMEOUT:-10m}")
  HELM_ARGS+=("$@")
  return 0
}

# ------------------------------------------------------------- bring-up

# stack_helm_with_retry runs the install, retrying ONLY a control plane that was
# briefly unreachable.
#
# A GKE endpoint can refuse connections for a few seconds after a resize, while
# kubectl was working moments earlier. Observed on a real run: `datastores
# ready` passed, and the very next command died with
#
#   Error: kubernetes cluster unreachable: Get "https://.../version":
#   dial tcp ...:443: connect: connection refused
#
# That is not a render error, a values error or a chart error, and treating it
# like one costs a whole cluster to learn that nothing was wrong.
#
# The retry is NARROW on purpose. A chart that cannot render will fail
# identically every time, so retrying it burns the budget three times over and
# reports the same thing later. Only the unreachable/refused shape is retried;
# anything else fails on the first attempt exactly as before.
stack_helm_with_retry() {
  local attempt out
  for attempt in 1 2 3; do
    if out="$(helm "${HELM_ARGS[@]}" 2>&1)"; then
      printf '%s\n' "$out"
      return 0
    fi
    printf '%s\n' "$out" >&2
    case "$out" in
      *"cluster unreachable"*|*"connection refused"*|*"i/o timeout"*|*"TLS handshake timeout"*)
        [ "$attempt" = "3" ] && return 1
        exp_warn "the cluster was unreachable on attempt $attempt; this is the endpoint settling, not the chart. Retrying."
        sleep $((attempt * 15)) ;;
      *)
        # A real failure. Fail now rather than three times.
        return 1 ;;
    esac
  done
  return 1
}

stack_up() { # <release> <jwt> <password> [extra --set args...]
  local release="$1" jwt="$2" pw="$3"; shift 3

  exp_log "creating namespaces $STACK_NS (control plane) and $STACK_TASK_NS (task pods)"
  kubectl create namespace "$STACK_NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl create namespace "$STACK_TASK_NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

  exp_log "applying Postgres and Redis (Deployments with emptyDir: throwaway, never a reference)"
  stack_datastores_manifest "$STACK_NS" | kubectl apply -f - >/dev/null
  kubectl -n "$STACK_NS" rollout status deploy/postgres --timeout=5m \
    || exp_die "Postgres never became ready; nothing downstream of it can be measured"
  kubectl -n "$STACK_NS" rollout status deploy/redis --timeout=5m \
    || exp_die "Redis never became ready; nothing downstream of it can be measured"
  exp_ok "datastores ready"

  stack_helm_args "$release" "$STACK_NS" "$STACK_TASK_NS" "$jwt" "$pw" "$@"
  exp_log "helm ${HELM_ARGS[*]:0:3} (server image $STACK_SERVER_IMAGE_REPO:$STACK_SERVER_IMAGE_TAG)"
  stack_helm_with_retry || exp_die "helm install failed; see the error above"

  kubectl -n "$STACK_NS" rollout status "deploy/$release" --timeout=10m \
    || exp_die "the control plane never became Ready"
  exp_ok "control plane up"
}

# stack_api_forward opens a port-forward to the control plane and prints the
# local base URL. A port-forward and not a LoadBalancer on purpose: a Service of
# type LoadBalancer leaves a forwarding rule and a reserved address behind that
# `teardown.sh --leftovers` can only LIST, never delete, so every run would
# accrete billing resources nothing cleans up.
stack_api_forward() { # <release> <local port>
  local release="$1" port="$2"
  kubectl -n "$STACK_NS" port-forward "svc/$release" "$port:8080" >/dev/null 2>&1 &
  STACK_PF_PID=$!
  local _i
  for _i in $(seq 1 30); do
    if curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
      exp_ok "API reachable on 127.0.0.1:$port (port-forward pid $STACK_PF_PID)"
      return 0
    fi
    sleep 1
  done
  exp_die "the API never answered on 127.0.0.1:$port after 30s"
}

stack_api_forward_stop() {
  [ -n "${STACK_PF_PID:-}" ] && kill "$STACK_PF_PID" 2>/dev/null || true
  STACK_PF_PID=""
}

# ---------------------------------------------------------------- self-test

self_test() {
  local fail=0
  _eq() { [ "$1" = "$2" ] && { echo "  ok   $3"; return 0; }; echo "  FAIL $3: '$1' != '$2'"; fail=1; }

  stack_helm_args leoflow leoflow-system leoflow jwt-abc pw-xyz

  # THE defect this argv check exists for, and it is not hypothetical: the
  # recipe this file was asked to reuse specified agentTLS.enabled=false, and
  # helm/dexaflow/templates/deployment.yaml:41 `fail`s the render on exactly
  # that. An install carrying it never reaches a cluster, so the first sign
  # would be a failed helm at the end of a paid provision.
  case " ${HELM_ARGS[*]} " in
    *"auth.agentTLS.enabled=false"*)
      echo "  FAIL the install carries agentTLS.enabled=false, which this chart refuses to render"; fail=1 ;;
    *"auth.agentTLS.enabled=true"*)
      echo "  ok   the install keeps agent TLS on, so the chart's render guard is not tripped" ;;
    *)
      echo "  FAIL the install does not decide agentTLS at all"; fail=1 ;;
  esac
  case " ${HELM_ARGS[*]} " in
    *"auth.agentTLS.autoGenerate=true"*) echo "  ok   the cert is auto-generated, so the install needs no cert-manager" ;;
    *) echo "  FAIL nothing provides a gRPC cert, so the Pro edition would refuse to boot"; fail=1 ;;
  esac

  # Every required Pro value is present. Each of these missing is a failed
  # install discovered after a cluster is already billing.
  local k
  for k in image.repository image.tag taskNamespace database.url redis.url auth.jwtSecret bootstrap.password; do
    case " ${HELM_ARGS[*]} " in
      *"$k="*) echo "  ok   the install sets $k" ;;
      *) echo "  FAIL the install never sets $k"; fail=1 ;;
    esac
  done

  # The migrate image must be pinned to the same tag as the server. A default
  # of .Chart.AppVersion is a different image than the one being tested the
  # moment the source tree is ahead of the published release, which it is here:
  # the chart is installed FROM SOURCE while the server image is the published
  # 0.4.7.
  case " ${HELM_ARGS[*]} " in
    *"migrations.image.tag=$STACK_SERVER_IMAGE_TAG"*)
      echo "  ok   the migration image is pinned to the same tag as the server image" ;;
    *) echo "  FAIL the migration image is not pinned to the server tag, so a source tree ahead of the release would migrate with a different image"; fail=1 ;;
  esac

  # --wait, or the runner starts measuring a control plane that is not up and
  # attributes its own impatience to the scheduler.
  case " ${HELM_ARGS[*]} " in
    *" --wait "*) echo "  ok   the install waits, so nothing is measured against a half-rolled deployment" ;;
    *) echo "  FAIL the install does not --wait"; fail=1 ;;
  esac

  # --set-string for the secrets: a password or an HMAC secret that happens to
  # be all digits is read by --set as a number, and the rendered Secret then
  # carries a different value than the operator passed.
  case " ${HELM_ARGS[*]} " in
    *"--set-string auth.jwtSecret="*) echo "  ok   the JWT secret goes through --set-string, so a numeric secret is not retyped" ;;
    *) echo "  FAIL the JWT secret is not --set-string; an all-digit secret would be coerced"; fail=1 ;;
  esac

  # The datastore render: the two properties that a paid run would otherwise
  # discover the hard way.
  local manifest; manifest="$(stack_datastores_manifest leoflow-system)"
  case "$manifest" in
    *"PGDATA"*"/var/lib/postgresql/data/pgdata"*)
      echo "  ok   PGDATA is a subdirectory of the mount, so initdb does not refuse a non-empty directory" ;;
    *) echo "  FAIL PGDATA is the mount root; initdb refuses that on a volume with anything in it"; fail=1 ;;
  esac
  # Guaranteed QoS for the datastores. Under the node pressure the saturation
  # experiment creates ON PURPOSE, a BestEffort Postgres is evicted first, and
  # the run would read its own datastore eviction as the control plane
  # saturating. Requests must equal limits on BOTH dimensions for Guaranteed.
  case "$manifest" in
    *'requests: { cpu: "500m", memory: "512Mi" }'*'limits:   { cpu: "500m", memory: "512Mi" }'*)
      echo "  ok   Postgres is Guaranteed QoS (requests == limits on cpu AND memory), so it is last out under node pressure" ;;
    *) echo "  FAIL Postgres is not Guaranteed QoS; it would be evicted by the pressure the experiment creates"; fail=1 ;;
  esac
  _eq "$(printf '%s' "$manifest" | grep -c 'kind: Deployment')" "2" "the datastore render is exactly two Deployments"
  _eq "$(printf '%s' "$manifest" | grep -c 'kind: Service')" "2" "the datastore render is exactly two Services"
  # emptyDir is deliberate AND dangerous, so it is asserted rather than assumed:
  # if someone later swaps in a PVC, a deleted cluster leaves a disk behind that
  # teardown.sh --leftovers can only list.
  case "$manifest" in
    *"emptyDir"*) echo "  ok   the datastores use emptyDir, so a deleted cluster leaves no disk behind to bill" ;;
    *) echo "  FAIL the datastores no longer use emptyDir; a surviving PersistentDisk is now possible"; fail=1 ;;
  esac
  case "$manifest" in
    *"PersistentVolumeClaim"*) echo "  FAIL the datastore render creates a PVC, which outlives the cluster as a billed disk"; fail=1 ;;
    *) echo "  ok   nothing in the datastore render creates a PVC" ;;
  esac
  # A LoadBalancer Service would leave a forwarding rule and an address behind.
  case "$manifest" in
    *"type: LoadBalancer"*) echo "  FAIL a LoadBalancer Service leaves a forwarding rule and a reserved address behind"; fail=1 ;;
    *) echo "  ok   no LoadBalancer Service, so no forwarding rule or address survives the cluster" ;;
  esac

  # stack_helm_with_retry, over a stub helm. The distinction it draws is the
  # whole point: a transient endpoint blip must be retried, and a chart that
  # cannot render must NOT be, because retrying it burns three clusters' worth
  # of budget to report the same thing later.
  local htmp; htmp="$(mktemp -d)"
  cat > "$htmp/helm" <<'STUB'
#!/usr/bin/env bash
echo "$*" >> "$STUB_HELM_CALLS"
n=$(wc -l < "$STUB_HELM_CALLS" | tr -d ' ')
case "$STUB_HELM_MODE" in
  # Unreachable twice, then succeeds: the shape of an endpoint settling.
  flaky)  [ "$n" -ge 3 ] && exit 0
          echo 'Error: kubernetes cluster unreachable: Get "https://1.2.3.4/version": dial tcp 1.2.3.4:443: connect: connection refused' >&2
          exit 1 ;;
  # A real chart error, identical every time.
  broken) echo 'Error: template: leoflow/templates/deployment.yaml:199: executing at <fail>: error calling fail: agentTokenTransport must be exchange' >&2
          exit 1 ;;
  *)      exit 0 ;;
esac
STUB
  chmod +x "$htmp/helm"
  HELM_ARGS=(upgrade --install probe ./helm/dexaflow)

  : > "$htmp/calls-flaky"
  if ( export PATH="$htmp:$PATH" STUB_HELM_CALLS="$htmp/calls-flaky" STUB_HELM_MODE=flaky \
              LEOFLOW_TEST_FAST_RETRY=1
       stack_helm_with_retry ) >/dev/null 2>&1; then
    echo "  ok   an unreachable endpoint is retried and the install succeeds"
  else
    echo "  FAIL a transient unreachable cluster was treated as a chart failure"; fail=1
  fi

  : > "$htmp/calls-broken"
  ( export PATH="$htmp:$PATH" STUB_HELM_CALLS="$htmp/calls-broken" STUB_HELM_MODE=broken
    stack_helm_with_retry ) >/dev/null 2>&1
  local broken_tries; broken_tries="$(wc -l < "$htmp/calls-broken" | tr -d ' ')"
  if [ "${broken_tries:-0}" = "1" ]; then
    echo "  ok   a chart that cannot render fails on the first attempt, not the third"
  else
    echo "  FAIL a real chart error was retried $broken_tries times; that is three clusters to learn the same thing"; fail=1
  fi
  rm -rf "$htmp"

  [ "$fail" = "0" ] && { echo "stack self-test: ok"; return 0; }
  return 1
}

# Only when EXECUTED, never when sourced. A sourced file inherits the caller's
# positional parameters, so without this guard `netpol.sh --self-test` would run
# this library's self-test and exit before the runner's own ever defined itself.
if [ "${BASH_SOURCE[0]}" = "$0" ] && [ "${1:-}" = "--self-test" ]; then
  self_test; exit $?
fi
return 0 2>/dev/null || true
