---
title: In-transit encryption
weight: 101
description: "Encrypt every hop inside the cluster: a service mesh with mTLS, or TLS on the API listener for load balancer re-encryption, and who may dial the agent gRPC port."
---

A Pro install already encrypts two hops: the browser or client to your load
balancer or Ingress (TLS you terminate there), and the task pod's agent to the
control plane's gRPC port (server TLS, see [Pro TLS](/operate/pro-tls/)). One
hop is plain HTTP by default: **from the load balancer or Ingress controller to
the API pod**. A regulated deployment usually needs that hop encrypted too,
because a passive observer on the pod network (a compromised CNI, a
`hostNetwork` pod on the node, VPC traffic mirroring) would otherwise read, in
clear text:

- bearer tokens and session cookies on every API and UI request;
- the password in `POST /auth/token` (sign-in);
- the password in `POST /api/v2/users` (creating a user), a durable credential.

A NetworkPolicy limits **who can connect**; it does not encrypt anything. Pick
one of the two options below.

## Option A (preferred): a service mesh with mTLS

A mesh such as Istio or Linkerd encrypts and authenticates every pod-to-pod
connection with mutual TLS, with no change to Dexaflow. It covers both planes at
once: Ingress to API pod, and task pod to the scheduler's gRPC port, which also
gives the gRPC port a workload identity check that the agent token alone does
not (see [Who may dial the gRPC port](#who-may-dial-the-agent-grpc-port)).

1. Enable sidecar injection (or ambient mode) in the release namespace and in
   the task namespace (`taskNamespace`, `leoflow` by default), for example
   `kubectl label namespace leoflow istio-injection=enabled`. Task pods are
   created by the scheduler, so they get the mesh through their namespace.
2. Require mTLS, for example an Istio `PeerAuthentication` with
   `mtls.mode: STRICT` in both namespaces, and include the Ingress controller in
   the mesh so its hop to the API pod is mTLS too.
3. Task pods run to completion. Make sure your mesh's proxy does not keep a
   finished task pod alive: use Kubernetes native sidecars or your mesh's
   equivalent for run-to-completion pods, and check it with one test DAG before
   rolling out.
4. Leave `httpTLS.enabled` off. The mesh proxy talks plain HTTP to the API
   container on localhost and mTLS on the wire.

The agent's own server TLS on gRPC stays on; inside a mesh it is carried within
the mTLS tunnel. The chart renders the gRPC port with `appProtocol: tcp`, so
the mesh passes that TLS through as opaque TCP. A mesh that guesses the
protocol from the port name `grpc` instead would parse the agent's TLS as
plaintext gRPC, and every task would stay `queued`.

## Option B: TLS on the API listener (no mesh)

When there is no mesh, the API server can serve HTTPS itself, so the load
balancer re-encrypts the hop to the pod: AWS ALB with
`backend-protocol: HTTPS`, GKE with `app-protocols` HTTPS, or an Ingress
controller with an HTTPS backend. It is off by default; plain HTTP stays the
default.

The certificate is yours (bring your own). In the chart, point `httpTLS` at a
`kubernetes.io/tls` Secret:

```yaml
httpTLS:
  enabled: true
  secretName: dexaflow-api-tls   # keys tls.crt and tls.key
```

The chart mounts the Secret into the pods that serve the API (the `api` role
when the control plane is split, never the scheduler), sets
`DEXAFLOW_SERVER_HTTP_TLS_CERT` and `DEXAFLOW_SERVER_HTTP_TLS_KEY` (the legacy
`LEOFLOW_*` names work too, as do `server.http_tls_cert` and
`server.http_tls_key` in `dexaflow.yaml` or `leoflow.yaml`), and switches the
kubelet probes to `scheme: HTTPS`. Outside the chart, set both settings to the
PEM file paths yourself. What changes:

- The API port speaks TLS only. A plain `http://` request to it gets
  `400 Bad Request`. Anything inside the cluster that calls the API (an
  in-cluster CLI, a script, a `kubectl port-forward`) uses `https://`.
- TLS 1.2 is the minimum version.
- The server re-reads the certificate and key when the files change, so a
  renewed Secret (cert-manager, or one you re-create) is served on the next
  connection without a restart. A rotation caught half written, or a new pair
  that does not load (for example a key that does not match the certificate),
  keeps serving the previous certificate and logs one warning, `API TLS
  certificate reload failed`, naming the files. The broken pair is not read
  again until one of the files changes. Alert on that warning: the previous
  certificate is served only until it expires.
- Setting only one of the two files fails boot, so a typo cannot leave the hop
  silently in plain HTTP.
- The metrics port is not affected.

### Getting a certificate

The certificate's SANs must cover the name the load balancer dials: the
DNS name of the Service in front of the API pods (the `api` Service when the
control plane is split), or nothing in particular when the load balancer does not verify the
backend certificate. AWS ALB and GKE backends accept any certificate on the
backend hop; they encrypt, they do not verify. NGINX Ingress verifies only when
you configure `proxy-ssl-verify`.

With cert-manager, a `Certificate` whose `secretName` is your
`httpTLS.secretName`. Without cert-manager, create the Secret from PEM files
your own CA issued:

```bash
kubectl -n dexaflow create secret tls dexaflow-api-tls \
  --cert=api.crt --key=api.key
```

To rotate it, replace the Secret (`kubectl create secret tls ... --dry-run=client -o yaml | kubectl apply -f -`).
The kubelet updates the mounted files within about a minute and the server
picks them up.

### Load balancer settings

| Front end | Setting |
|---|---|
| AWS ALB (Ingress) | `ingress.annotations`: `alb.ingress.kubernetes.io/backend-protocol: HTTPS` and `alb.ingress.kubernetes.io/healthcheck-protocol: HTTPS` |
| GKE Ingress | `service.annotations`: `cloud.google.com/app-protocols: '{"http":"HTTPS"}'` (the port is named `http`) |
| NGINX Ingress | `ingress.annotations`: `nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"` |
| AWS NLB | A **TLS listener that terminates at the NLB**, re-encrypting to the pod with a TLS target group. Never a plain TCP listener passing through to a plain HTTP pod: that is clear text from the client all the way in. |

## Creating users: the password is on the wire

`POST /api/v2/users` carries the new user's password in the request body.
Wherever users are created through the API, run Option A or B, so that the
password is encrypted on every hop and not only up to the load balancer. If you
can do neither, prefer SSO ([OIDC](/operate/sso-other-providers/)), where
Dexaflow never receives a password, or create users only from inside the
cluster over a port-forward and change the password at first sign-in.

## Who may dial the agent gRPC port

The agent gRPC port is server TLS: the agent verifies the control plane, and the
control plane identifies the agent by its bearer token, not by a client
certificate. A token that leaks (for example read from a pod spec under the
default `envvar` transport; see
[agent credential transport](/operate/agent-credential-transport/)) can be
replayed from any pod that can reach the port. Two ways to narrow that:

- **Restrict the source pods.** With `networkPolicy.enabled`, set
  `networkPolicy.grpcFrom` to give the gRPC port a rule of its own, separate from
  the HTTP sources in `networkPolicy.ingressFrom`. Empty (the default) keeps
  today's single rule. For example, only task pods and warm workers in the task
  namespace:

  ```yaml
  networkPolicy:
    enabled: true
    grpcFrom:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: leoflow   # your taskNamespace
        podSelector:
          matchExpressions:
            - key: leoflow.io/run-id
              operator: Exists
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: leoflow
        podSelector:
          matchExpressions:
            - key: leoflow.io/warm-worker
              operator: Exists
  ```

  Labels are set by whoever can create pods in that namespace, so this narrows
  the network path; it is not an identity check. Keep pod creation in the task
  namespace to the control plane.
- **Bind the token to the pod.** `auth.agent_token_transport: exchange` keeps
  the token off the pod spec and binds it to the pod's projected ServiceAccount
  token ([ADR 0055](/project/adrs/0055-secret-scoping-and-token-liveness/)). With
  a mesh (Option A), mTLS adds a workload identity to every gRPC connection too.

Dexaflow does not issue per-pod client certificates for gRPC.
