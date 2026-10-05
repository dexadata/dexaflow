---
# --- AUTO redirect aliases (build_redirects.py) — do not edit by hand ---
aliases:
  - /adr/0019-secret-encryption-at-rest.html
# --- end AUTO redirect aliases ---
title: "ADR 0019: Secret Encryption at Rest (Connections)"
linkTitle: 0019 · Secret Encryption at Rest (Connections)
weight: 190
description: "ADR 0019: Secret Encryption at Rest (Connections)"
---

**Status:** Accepted
**Date:** 2026-05-23
**Deciders:** Project founder

## Context

The Admin UI manages **Connections** (Airflow-compatible): credentials and
endpoints for operators — `conn_id`, `conn_type`, `host`, `schema`, `login`,
`password`, `port`, `extra`. Several fields are secrets (notably `password` and
often `extra`), so they must not be stored in cleartext in Postgres, must not be
echoed back to the UI, and must not leak into logs.

Airflow encrypts these at rest with Fernet, keyed by a configured secret. We want
an equivalent that is simple, standard, available immediately, and does not lock
us out of a stronger key-management story later.

Variables (ADR-less, see #45) are stored plaintext with API masking for the MVP —
acceptable because a variable is config, not inherently a credential. Connections
are different: a `password` field is a credential by definition.

## Decision

Encrypt sensitive connection fields at rest with **AES-256-GCM**, using a
**32-byte key supplied via configuration** (`LEOFLOW_SECRET_KEY`, i.e.
`auth.secret_key`).

- Algorithm: AES-256-GCM (authenticated encryption). Each value gets a fresh
  random 96-bit nonce; the stored form is `base64(nonce || ciphertext || tag)`.
- Key: 32 bytes, provided as a base64 or 64-char-hex string in config; decoded at
  startup. A malformed or missing key disables connection writes (the API returns
  a clear error) rather than silently storing plaintext.
- Scope: only the secret fields (`password`, `extra`) are encrypted; non-secret
  metadata (`conn_id`, `conn_type`, `host`, `schema`, `login`, `port`) is stored
  in the clear so it remains queryable.
- API: `password` is **never returned** (write-only); responses mask it. `extra`
  is returned but secret-looking keys within it may be masked.
- Rotation: `LEOFLOW_SECRET_KEY` takes a comma-separated list where the first
  entry encrypts and the rest only decrypt, the same rule as Airflow's
  `fernet_key`. The control plane re-encrypts stored secrets onto the first key
  at startup, so a rotation finishes and the old key can be removed.

  **Note (2026-10-04, ADR 0065):** this startup re-encryption no longer runs
  for Dexaflow Lite. `dexaflow lite` starts the server with
  `LEOFLOW_SECRET_KEY_REENCRYPT_ON_BOOT=false`, and a Lite install moves its
  stored secrets between keys only through `dexaflow lite migrate-key`, which
  records every key before it touches a row and verifies the pass before it
  commits. Pro keeps the behavior described here.

  **This supersedes the original decision**, which read: "changing the key
  invalidates existing ciphertexts; connections are re-entered". That was
  acceptable while the key was set once by an operator who chose it. It stopped
  being acceptable when Leoflow Lite had to move every install off a key
  compiled into this repository (#486): a rotation that orphans existing
  credentials is worse than the published key it replaces, so nobody would take
  it.

  Trying keys in order is safe only because AES-GCM is authenticated: a wrong
  key fails to open rather than returning plausible garbage. Envelope
  encryption and per-row key versioning remain a future evolution; this is key
  rotation, not a key hierarchy.

## Consequences

- **Standard and immediate.** AES-256-GCM from the Go stdlib (`crypto/aes`,
  `crypto/cipher`); no new dependency, no external service.
- **Honest failure.** Without a configured key, connection writes fail loudly; we
  never downgrade to plaintext for a credential.
- **Bounded key management.** A single symmetric key in config is the weak point;
  it must be delivered via a Secret (the Helm chart already supports
  `auth.existingSecret`). KMS/Vault envelope encryption is a future ADR built on
  the same `secrets.Cipher` interface, so the call sites do not change.
- **Variables stay plaintext+masked** for now; if a variable must hold a secret,
  the same `Cipher` can be applied later behind a per-variable `is_encrypted`
  flag.

## Alternatives considered

- **Plaintext + masking (as Variables):** rejected for connections — a `password`
  in cleartext in the metadata DB is unacceptable beyond a throwaway demo.
- **KMS / external secret manager:** stronger, but requires infra and a provider
  abstraction; deferred. The `Cipher` interface keeps it open.
- **Fernet (to mirror Airflow exactly):** no first-class Go Fernet; AES-256-GCM is
  the idiomatic, equally-strong stdlib choice and the stored format is internal.
