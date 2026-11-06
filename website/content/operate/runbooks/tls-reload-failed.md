---
title: DexaflowTLSReloadFailed
weight: 130
description: What DexaflowTLSReloadFailed means and what to do when it fires.
---

**Symptom.** `dexaflow_tls_reload_failures_total{listener}` rose: a rotated
certificate or key on disk failed to load.

**Impact.** None yet: the listener keeps serving its last good certificate.
When that one expires, clients (browsers on `api`, agents on `agent_grpc`)
stop connecting.

**First commands.**

```bash
kubectl -n <namespace> logs deploy/<release> | grep -i 'tls' | tail -20
kubectl -n <namespace> get secret <tls-secret> -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -subject -enddate
```

**Decide.** Usually a key that does not match the certificate, or a PEM
written halfway. Fix the Secret; the next reload clears it. See
[Cluster TLS](/operate/cluster-tls/) and
[Rotate agent certificates](/operate/rotate-agent-certificates/).
