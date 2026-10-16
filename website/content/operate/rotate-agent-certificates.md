---
title: Rotate agent certificates
weight: 102
description: "Renew the agent gRPC server certificate and roll the CA that task pods trust, without restarting the control plane or killing running tasks."
---

Task pods verify the control plane's gRPC server certificate against the CA
bundle in `agentTLS.caConfigMap` (see [Pro TLS](/operate/pro-tls/) for how the
trust is set up). This page covers the two rotations that channel needs:
renewing the **server certificate**, and replacing the **CA** that signs it.

Both are picked up without a restart:

- The control plane re-reads `tls.crt` and `tls.key` on the next agent
  handshake after either file changes, so a renewed Secret is served as soon as
  the kubelet updates the mounted volume.
- The agent re-reads `ca.crt` every time it opens a connection to the control
  plane (its first connection and every reconnect), and the file may hold
  several CAs. A running task that reconnects after the bundle changed trusts
  the new bundle.

Connections already established keep the certificate they were opened with;
only new handshakes see the change. That is enough: a connection that is up was
already verified.

{{% alert title="Auto-generated certificates" color="info" %}}
With `agentTLS.autoGenerate: true` (the default when you supply nothing) the
chart mints the CA once and reuses it on every upgrade, so there is nothing to
rotate. This page is for certificates you bring yourself, usually through
cert-manager.
{{% /alert %}}

## Renew the server certificate

Nothing to do. When cert-manager renews the `Certificate` behind
`agentTLS.serverCertSecret`, the kubelet rewrites the mounted files (within
about a minute) and the next handshake serves the new certificate. Each control
plane replica picks it up on its own.

The renewed certificate must chain to a CA that is in `caConfigMap`. When it is
signed by the same CA, as with a normal cert-manager renewal, that is already
true. When it is signed by a new CA, follow the next section.

To check what a replica serves, forward the gRPC port (`ports.grpc`, 9091 by
default) of a control plane pod that runs the scheduler and read the
certificate:

```console
$ kubectl -n <namespace> port-forward pod/<control-plane-pod> 9091:9091 &
$ openssl s_client -connect localhost:9091 -alpn h2 </dev/null 2>/dev/null \
    | openssl x509 -noout -serial -issuer -enddate
```

## Rotate the CA

Never replace the CA in `caConfigMap` in one step. If the bundle switches to the
new CA while the server still presents a certificate from the old one (or the
reverse), every task that reconnects in between fails verification and is
eventually reaped as agent lost. Instead, trust both CAs for the duration of
the rotation:

1. **Add the new CA to the bundle.** Put both PEM certificates in the `ca.crt`
   key of `caConfigMap`, old and new, one after the other. Wait for the kubelet
   to update the volumes of running task pods (about a minute with default
   kubelet settings). Pods created from now on start with both CAs.
2. **Switch the server certificate to the new CA.** Point the `Certificate`'s
   `issuerRef` at the new CA's issuer (or replace the BYO Secret) and let it
   reissue. Every agent trusts both CAs, so reconnects keep working.
3. **Remove the old CA from the bundle** once every control plane replica
   serves the new certificate (check with the command above).

Agents from a release before this behavior read `ca.crt` only at startup. While
any task pod of such a release may still be running, wait in step 1 for the
longest task to finish rather than for the kubelet sync.

[trust-manager](https://cert-manager.io/docs/trust/trust-manager/) can keep the
union bundle for you: a `Bundle` with one source per CA writes their
concatenation to the ConfigMap, so step 1 is adding a source and step 3 is
removing it.

```yaml
apiVersion: trust.cert-manager.io/v1alpha1
kind: Bundle
metadata:
  name: dexaflow-agent-ca            # == agentTLS.caConfigMap
spec:
  sources:
    - secret:                        # the old CA
        name: dexaflow-ca-2025
        key: ca.crt
    - secret:                        # the new CA, added in step 1
        name: dexaflow-ca-2026
        key: ca.crt
  target:
    configMap:
      key: ca.crt
    namespaceSelector:
      matchLabels:
        kubernetes.io/metadata.name: <namespace>   # every namespace task pods run in
```

## When a file does not load

A rotation caught half written, a key that does not match its certificate, or
a bundle with no certificate in it never replaces what is in use:

- The control plane keeps serving the last certificate that loaded, logs
  `TLS certificate reload failed` with `listener=agent_grpc` and the file paths,
  and counts the failure once on `dexaflow_tls_reload_failures_total{listener="agent_grpc"}`.
  It tries again when either file changes. Alert on any increase of that
  counter: the served certificate will expire unless the files are fixed.
- The agent keeps verifying against the last bundle that loaded and logs
  `agent TLS CA bundle reload failed` with the file path to the task pod's
  output (`kubectl logs`).

A certificate or bundle that is missing or invalid **at startup** is still a
startup error, on both sides.
