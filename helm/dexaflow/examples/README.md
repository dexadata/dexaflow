# PoC: Dexaflow + plain Postgres + plain Redis on one cluster

> [!WARNING]
> **NOT FOR PRODUCTION.** This recipe is for evaluating Dexaflow end-to-end on a
> single Kubernetes cluster (kind, minikube, k3d, a scratch GKE/EKS namespace).
> The datastores below run as plain unprotected pods with **emptyDir storage**:
> no persistence, no HA, no auth on Redis. For real deploys, point
> `database.url` / `redis.url` at managed services (RDS, ElastiCache, CloudSQL,
> Memorystore, your own HA cluster) and drop the datastore manifests below.

The chart's `templates/deployment.yaml` deliberately fails the install when
neither `database.url`/`existingSecret` nor `redis.url`/`existingSecret` is set
— Pro Dexaflow refuses to silently fall back to embedded datastores (those are
a Lite-edition concept). This recipe satisfies that contract by deploying
plain Postgres + Redis pods first, then pointing the chart at them.

> **Why not the Bitnami charts?** Bitnami moved their versioned community
> images to `docker.io/bitnamilegacy` / behind a paywall in August 2025,
> breaking pinned chart installs (`FetchReference … not found`). Plain
> `postgres:16-alpine` + `redis:7-alpine` pods cover the same evaluation
> surface with no third-party chart dependency. Same Service names
> (`pg-postgresql`, `redis-master`) so the values file is unchanged.

## Prerequisites

- A Kubernetes cluster (`kubectl cluster-info` works).
- `helm` v3.16+.
- Outbound access to `docker.io` (or any mirror) for the official
  `postgres:16-alpine` and `redis:7-alpine` images.

## Steps

### 1. Namespace

```bash
kubectl create namespace dexaflow-poc
```

### 2. Postgres + Redis (plain official images, emptyDir)

```bash
kubectl apply -n dexaflow-poc -f - <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: pg-postgresql
type: Opaque
stringData:
  password: dexaflow-poc
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pg-postgresql
  labels: { app: pg-postgresql }
spec:
  replicas: 1
  selector: { matchLabels: { app: pg-postgresql } }
  template:
    metadata:
      labels: { app: pg-postgresql }
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          env:
            - { name: POSTGRES_USER,     value: dexaflow }
            - { name: POSTGRES_DB,       value: dexaflow }
            - { name: POSTGRES_PASSWORD, valueFrom: { secretKeyRef: { name: pg-postgresql, key: password } } }
            - { name: PGDATA,            value: /var/lib/postgresql/data/pgdata }
          ports: [{ containerPort: 5432 }]
          resources:
            requests: { cpu: 100m, memory: 128Mi }
            limits:   { cpu: "1",  memory: 512Mi }
          readinessProbe:
            exec: { command: ["pg_isready", "-U", "dexaflow", "-d", "dexaflow"] }
            initialDelaySeconds: 5
            periodSeconds: 5
          volumeMounts:
            - { name: data, mountPath: /var/lib/postgresql/data }
      volumes:
        - { name: data, emptyDir: {} }
---
apiVersion: v1
kind: Service
metadata: { name: pg-postgresql }
spec:
  selector: { app: pg-postgresql }
  ports: [{ port: 5432, targetPort: 5432 }]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis-master
  labels: { app: redis-master }
spec:
  replicas: 1
  selector: { matchLabels: { app: redis-master } }
  template:
    metadata:
      labels: { app: redis-master }
    spec:
      containers:
        - name: redis
          image: redis:7-alpine
          args: ["--save", "", "--appendonly", "no"]
          ports: [{ containerPort: 6379 }]
          resources:
            requests: { cpu: 50m,  memory: 64Mi }
            limits:   { cpu: 500m, memory: 256Mi }
          readinessProbe:
            exec: { command: ["redis-cli", "ping"] }
            initialDelaySeconds: 3
            periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata: { name: redis-master }
spec:
  selector: { app: redis-master }
  ports: [{ port: 6379, targetPort: 6379 }]
EOF
```

Wait for both to be ready:

```bash
kubectl -n dexaflow-poc rollout status deploy/pg-postgresql --timeout=180s
kubectl -n dexaflow-poc rollout status deploy/redis-master  --timeout=120s
```

- **Service names** `pg-postgresql` + `redis-master` deliberately match the
  Bitnami release names the original recipe used, so the values file is
  unchanged.
- **Storage** is `emptyDir`: data evaporates when the Postgres pod restarts.
  Swap for a PVC when you want survival across restarts.
- **Redis runs without auth** — fine on a scratch namespace, never on a
  shared cluster.
- **Resource limits** are minimal (sized for evaluation, not load).

### 3. Dexaflow

```bash
helm install dexaflow ./helm/dexaflow \
  -n dexaflow-poc \
  -f helm/dexaflow/examples/poc.yaml
```

The `poc.yaml` file in this directory carries the matching `database.url` /
`redis.url` plus fixture credentials.

Wait for it: `kubectl -n dexaflow-poc rollout status deploy/dexaflow --timeout=240s`.

## Verify

```bash
kubectl -n dexaflow-poc port-forward svc/dexaflow 8080:8080 &
curl -fsS http://127.0.0.1:8080/readyz
```

Open `http://127.0.0.1:8080/` for the UI. Log in with `admin@dexaflow.local` /
`dexaflow-poc-admin` (the `bootstrap.password` from the values file).

## Teardown

```bash
helm uninstall -n dexaflow-poc dexaflow
kubectl delete -n dexaflow-poc deploy/pg-postgresql deploy/redis-master \
                              svc/pg-postgresql   svc/redis-master    \
                              secret/pg-postgresql
kubectl delete namespace dexaflow-poc
```

No PVCs to clean up — emptyDir storage is per-pod.

## When you're ready for production

Drop the kubectl-apply step and the PoC values file. Point at managed
datastores via either inline values or existing Secrets:

```bash
helm install dexaflow ./helm/dexaflow -n dexaflow \
  --set database.url='postgres://...@<managed-pg>:5432/dexaflow?sslmode=require' \
  --set redis.url='rediss://...@<managed-redis>:6380/0' \
  --set auth.jwtSecret="$(openssl rand -base64 64)" \
  --set secretKey="$(openssl rand -hex 32)"  # 64 hex chars -> 32 bytes
```

See the main chart README (`helm/dexaflow/README.md`) for the full values
reference, and [upgrades](https://dexaflow.dexadata.ai/operate/upgrades/) / [backup and restore](https://dexaflow.dexadata.ai/operate/backup-restore/) for ongoing
operations.
