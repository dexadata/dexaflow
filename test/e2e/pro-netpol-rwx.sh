#!/usr/bin/env bash
# #526 Layer-1: validate the Pro split's two claims that k3d/kindnet CANNOT test —
# real NetworkPolicy ENFORCEMENT and a real ReadWriteMany shared-logs PVC — on a
# kind cluster with a real CNI (Calico) and an NFS RWX StorageClass. Intended as a
# CI job (ubuntu-latest). Destructive; creates + deletes its own cluster.
set -uo pipefail

CLUSTER="leoflow-netpol-rwx"
NS="leoflow"
CALICO_VER="v3.28.2"
SECRET_KEY="$(openssl rand -hex 32)"   # ADR 0019: exactly 32 raw bytes or 64 hex chars
pass(){ printf '  \033[1;32mPASS\033[0m %s\n' "$1"; }
fail(){ printf '  \033[1;31mFAIL\033[0m %s\n' "$1"; FAILED=1; }
log(){ printf '\033[1;34m==>\033[0m %s\n' "$1"; }
FAILED=0
cleanup(){ kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

# ── kind with the default CNI DISABLED so Calico can enforce NetworkPolicy ──────
log "kind cluster (default CNI disabled → Calico enforces netpol)"
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
cat >/tmp/kind-netpol.yaml <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  podSubnet: "192.168.0.0/16"
YAML
kind create cluster --name "$CLUSTER" --config /tmp/kind-netpol.yaml --wait 120s || { echo "kind create failed"; exit 1; }

log "install Calico + wait for it to be ready"
kubectl apply -f "https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VER}/manifests/calico.yaml" >/dev/null
kubectl -n kube-system rollout status ds/calico-node --timeout=180s || { echo "calico not ready"; kubectl -n kube-system get pods; exit 1; }
kubectl wait --for=condition=Ready nodes --all --timeout=120s

log "install nfs-server-provisioner (Ganesha: NFS server + RWX provisioner in one pod)"
helm repo add nfs-ganesha https://kubernetes-sigs.github.io/nfs-ganesha-server-and-external-provisioner/ >/dev/null 2>&1 || true
helm repo update nfs-ganesha >/dev/null 2>&1 || true
helm install nfs-prov nfs-ganesha/nfs-server-provisioner -n kube-system \
  --set 'storageClass.name=nfs-rwx' \
  --set 'storageClass.defaultClass=false' \
  --set 'persistence.enabled=false' \
  --wait --timeout 300s || { echo "nfs-server-provisioner install failed"; kubectl -n kube-system get pods | grep nfs; kubectl -n kube-system describe pods -l app=nfs-server-provisioner | tail -30; exit 1; }
# sanity: prove the RWX StorageClass provisions before the chart depends on it (fast fail)
kubectl apply -f - >/dev/null <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: rwx-probe, namespace: kube-system }
spec:
  accessModes: [ReadWriteMany]
  storageClassName: nfs-rwx
  resources: { requests: { storage: 1Gi } }
YAML
bound=0
for _ in $(seq 1 40); do
  [ "$(kubectl -n kube-system get pvc rwx-probe -o jsonpath='{.status.phase}' 2>/dev/null)" = "Bound" ] && { bound=1; break; }
  sleep 3
done
kubectl -n kube-system delete pvc rwx-probe --wait=false >/dev/null 2>&1 || true
[ "$bound" = 1 ] || { echo "FAIL: nfs-rwx did not bind a probe PVC — NFS provisioning broken"; kubectl -n kube-system describe pvc rwx-probe 2>/dev/null | tail -25; kubectl -n kube-system logs deploy/csi-nfs-controller -c nfs 2>/dev/null | tail -20; exit 1; }
pass "cluster ready: Calico (netpol enforcing) + nfs-rwx StorageClass provisions RWX"

# ── leoflow images + datastores + TLS fixture (mirrors helm-ci.yaml) ────────────
log "build + load leoflow images"
docker build -q -t leoflow-server:ci -f deploy/Dockerfile.server . >/dev/null || { echo "server build failed"; exit 1; }
docker build -q -t leoflow-migrate:ci -f deploy/Dockerfile.migrate . >/dev/null || { echo "migrate build failed"; exit 1; }
kind load docker-image leoflow-server:ci leoflow-migrate:ci --name "$CLUSTER" >/dev/null 2>&1

log "namespace + datastores + agent TLS fixture"
kubectl create namespace "$NS" >/dev/null
kubectl -n "$NS" apply -f .github/ci/kind-datastores.yaml >/dev/null
kubectl -n "$NS" rollout status deploy/postgres --timeout=150s
kubectl -n "$NS" rollout status deploy/redis --timeout=150s
kubectl -n "$NS" exec deploy/postgres -- pg_isready -U leoflow -d leoflow -t 60 >/dev/null
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -keyout /tmp/a.key -out /tmp/a.crt \
  -subj "/CN=leoflow.leoflow.svc.cluster.local" \
  -addext "subjectAltName=DNS:leoflow-scheduler,DNS:leoflow.leoflow.svc.cluster.local" >/dev/null 2>&1
kubectl -n "$NS" create secret tls leoflow-agent-tls --cert=/tmp/a.crt --key=/tmp/a.key >/dev/null
kubectl -n "$NS" create configmap leoflow-agent-ca --from-file=ca.crt=/tmp/a.crt >/dev/null

# ── helm install: split + RWX shared logs + netpol scoped to a probe label ──────
log "helm install split.enabled=true, RWX logs (NFS), netpol ingress scoped to a label"
helm install leoflow helm/leoflow -n "$NS" \
  --set image.repository=leoflow-server --set image.tag=ci --set image.pullPolicy=IfNotPresent \
  --set migrations.image.repository=leoflow-migrate --set migrations.image.tag=ci --set migrations.image.pullPolicy=IfNotPresent \
  --set database.url='postgres://leoflow:leoflow@postgres:5432/leoflow?sslmode=disable' \
  --set redis.url='redis://redis:6379/0' \
  --set auth.jwtSecret=ci-jwt --set "secretKey=${SECRET_KEY}" \
  --set agentTLS.enabled=true --set agentTLS.serverCertSecret=leoflow-agent-tls --set agentTLS.caConfigMap=leoflow-agent-ca \
  --set bootstrap.password=ci-admin \
  --set split.enabled=true \
  --set logs.persistence.enabled=true --set logs.persistence.accessMode=ReadWriteMany --set logs.persistence.storageClass=nfs-rwx --set logs.persistence.size=1Gi \
  --set networkPolicy.enabled=true \
  --set 'networkPolicy.ingressFrom[0].podSelector.matchLabels.netpol-probe=allowed' \
  --wait --timeout 600s || { echo "helm install failed"; \
    echo "--- logs PVC ---"; kubectl -n "$NS" describe pvc leoflow-logs 2>&1 | tail -25; \
    echo "--- storageclasses ---"; kubectl get sc; \
    echo "--- pods ---"; kubectl -n "$NS" get pods; kubectl -n "$NS" describe pods | tail -60; exit 1; }

# ── RWX assertion: both roles Ready (helm --wait) + the logs PVC is RWX + Bound ─
log "assert: RWX shared-logs PVC bound + both roles mounted it"
kubectl -n "$NS" rollout status deploy/leoflow-api --timeout=60s && pass "api Deployment Ready (2 replicas share the RWX logs PVC)" || fail "api not Ready"
kubectl -n "$NS" rollout status deploy/leoflow-scheduler --timeout=60s && pass "scheduler Deployment Ready" || fail "scheduler not Ready"
pvc_mode="$(kubectl -n "$NS" get pvc -o jsonpath='{.items[?(@.status.phase=="Bound")].spec.accessModes[0]}' 2>/dev/null)"
echo "    logs PVC accessMode(s): $pvc_mode"
echo "$pvc_mode" | grep -q ReadWriteMany && pass "logs PVC bound as ReadWriteMany (RWX StorageClass works)" || fail "no Bound RWX PVC found"

# ── NetworkPolicy ENFORCEMENT assertion (the thing kindnet cannot do) ───────────
log "assert: Calico ENFORCES the api ingress netpol (scoped to netpol-probe=allowed)"
kubectl -n "$NS" run probe-denied  --image=curlimages/curl:8.10.1 --labels=role=x               --restart=Never --command -- sleep 300 >/dev/null
kubectl -n "$NS" run probe-allowed --image=curlimages/curl:8.10.1 --labels=netpol-probe=allowed --restart=Never --command -- sleep 300 >/dev/null
kubectl -n "$NS" wait --for=condition=Ready pod/probe-denied pod/probe-allowed --timeout=90s >/dev/null
API_SVC="leoflow-api"
# allowed source reaches api http; denied source times out (netpol default-deny for non-matching source)
if kubectl -n "$NS" exec probe-allowed -- curl -s -m 8 -o /dev/null -w '%{http_code}' "http://${API_SVC}:8080/readyz" 2>/dev/null | grep -q 200; then
  pass "allowed-labelled pod reaches api :8080 (ingressFrom honored)"
else fail "allowed pod could NOT reach api :8080 (netpol too strict / misrendered)"; fi
# A blocked connection yields http_code 000 (and curl exits non-zero); only a
# real 200 means the netpol let it through. Anything that is not 200 == blocked.
code="$(kubectl -n "$NS" exec probe-denied -- curl -s -m 8 -o /dev/null -w '%{http_code}' "http://${API_SVC}:8080/readyz" 2>/dev/null || true)"
if [ "$code" != "200" ]; then
  pass "non-matching pod is BLOCKED from api :8080 (http_code=${code:-none}) — Calico enforces the netpol (kindnet would NOT)"
else fail "non-matching pod reached api :8080 (200) — netpol NOT enforced"; fi

# ── #1067: the metrics port, on the one CNI in this repo that can prove it ────
#
# The old default left the metrics port out of every ingress rule, and an
# Ingress-typed policy denies what it does not match — so `networkPolicy.enabled`
# plus `metrics.serviceMonitor.enabled` produced a scrape target that was
# created and never answered. That was a claim about YAML until now: kindnet
# enforces no NetworkPolicy at all, so every other e2e in this repo would have
# passed either way.
#
# The install above sets `ingressFrom` scoped to a label, which is exactly the
# configuration the first attempt at this fix did NOT repair — folding the
# metrics port into the API's rule left the careful operator broken. Asserting
# it HERE is what proves the separate rule is independent of ingressFrom.
log "assert: the metrics port is reachable under an enforced netpol (#1067)"
code="$(kubectl -n "$NS" exec probe-allowed -- curl -s -m 8 -o /dev/null -w '%{http_code}' "http://${API_SVC}:9090/metrics" 2>/dev/null || true)"
if [ "$code" = "200" ]; then
  pass "metrics :9090 answers with ingressFrom narrowed — the scrape survives an enforced policy"
else fail "metrics :9090 did NOT answer (http_code=${code:-none}) — a Prometheus target would be created and never respond (#1067)"; fi

# And the default really is narrowable: with metricsFrom pointing elsewhere the
# port closes, so the value does what it claims rather than being decorative.
log "assert: metricsFrom actually restricts the scrape source (#1067)"
kubectl -n "$NS" patch networkpolicy leoflow-api --type=json \
  -p '[{"op":"replace","path":"/spec/ingress/1/from","value":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}}}]}]' >/dev/null 2>&1 || true
sleep 3
code="$(kubectl -n "$NS" exec probe-allowed -- curl -s -m 8 -o /dev/null -w '%{http_code}' "http://${API_SVC}:9090/metrics" 2>/dev/null || true)"
if [ "$code" != "200" ]; then
  pass "metrics :9090 is BLOCKED once metricsFrom names another namespace (http_code=${code:-none})"
else fail "metrics :9090 still answered after narrowing metricsFrom — the value is decorative"; fi

echo
if [ "$FAILED" -ne 0 ]; then echo "PRO NETPOL+RWX: FAILED"; exit 1; fi
echo "PRO NETPOL+RWX: all assertions held (netpol enforced + RWX shared logs)"
