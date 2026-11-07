# Validation record of `<rc tag>`

Copy this file to `test/release/results/<rc tag>.md` (for example
`v0.5.11-rc.1.md`), fill in the run header and every row of the §6 table while
you follow [rc-cluster-validation.md](../rc-cluster-validation.md), and commit
it. `scripts/check-rc-validation.sh` (run by `scripts/cut-release.sh` before a
GA) refuses the GA when the newest record of its rcs leaves a header field as a
placeholder, leaves a row without PASS, FAIL or NOT VERIFIED, or marks a row
FAIL without the issue opened for it (#1026). Keep the rows in step with the
runbook's §6 table.

## Run header

| Field | Value |
|---|---|
| RC under test | `<tag>` |
| Cloud / cluster | `<AWS EKS / GCP GKE>` (context: `<kubectl-context>`) |
| CNI | `<AWS VPC CNI / GKE Dataplane V2 / Calico>` (note it: §5 depends on it) |
| Node provisioning | (managed node group / Karpenter / GKE node pool?) |
| GKE mode | `<Standard / Autopilot>` (Autopilot mutates pod specs; see §5) |
| Image under test | `ghcr.io/dexadata/dexaflow-server:<tag>@sha256:<digest>` |
| Commit under test | `<the rc tag's commit sha>` |
| Date / operator | `<date>` / `<who>` |

## §6 Results + reporting

Record **NOT VERIFIED (cloud)** rather than PASS for anything §5 says this cloud
cannot settle. A row left blank reads as "not run"; a row marked PASS on the wrong
cloud reads as proof, and that is the failure §5 exists to prevent.

| # | Check | PASS/FAIL | Notes / evidence |
|---|---|---|---|
| §2 | smoke: task→success | | |
| #17 | hybrid: python tasks succeed (§3a) | | |
| #20 | dbt_groups project present in the image, both shapes (§3a) | | |
| #993 | bare compile + /spec: no host path in any entrypoint (§3a) | | |
| #994 | no-connection group succeeds AND carries --profiles-dir (§3a) | | |
| #994 | second arm: a group **with** `connection:` still succeeds (§3a) | | |
| #852 | read-only rootfs: pod admitted, dbt tasks succeed (§3a; no cloud delta) | | |
| #1005 | `docker run … dbt --debug parse`: full or partial? (local, §3a: a question, not a gate) | | |
| #15 | stray top-level key fails validate AND compile; Lite discovery unaffected (§3a) | | |
| #800 | zero-declaration: scope_warning naming zero declarations, task gets every connection (§3b) | | |
| #800 | stale declaration: scope_warning; enforce: only the declared connection that exists (§3b) | | |
| #722 | audit rows written | | |
| #723 | retry not wedged (§4.2) | | |
| #724 | validation → 400 | | |
| #725 | QoS Guaranteed + ClientIP (§4.3) | | |
| #802 | partial resource pair → Burstable + boot WARN + compile error (§4.3) | | |
| #804 | control-plane policy on, task policy off → no policy **selects** a task pod (§4.4) | | |
| #726 | api has no tls.key (§4.1) | | |
| #727 | migrate job: own SA, Role limited to the installation ConfigMap | | |
| #728 | warm TMPDIR fresh (§4.2) | | |
| #729 | managed-PG re-extract (Lite host) | | |
| - | task pods non-root by default | | |
| HA | HA-profile upgrade: a PDB exists that nobody asked for (§4.5) | | |
| #1023 | schema dropped under a running pod: `/readyz` 503, pod leaves endpoints, exactly one restart, no crash loop across 90s, recovery by a new `installation.id` (§4.6a) | | |
| #1023 | in-flight migration above the running binary: endpoints never empty, `/readyz` stays 200; dirty at the same version still goes NotReady (§4.6b) | | |
| ADR 0052 | `LEOFLOW_CHAOS_ONLY=CD chaos-runtime.sh`: C and D pass (§4.5) | | |

