#!/usr/bin/env bash
#
# netpol: prove, or fail to prove, #1089's standing NetworkPolicy rows on a CNI
# that actually enforces.
#
# WHY THIS CANNOT BE DONE LOCALLY. k3d's kindnet ACCEPTS a NetworkPolicy object
# and never enforces it. So every assertion of the form "the task pod cannot
# reach X" passes on k3d whether or not the policy works, whether or not the
# podSelector matches, and whether or not the chart rendered anything at all.
# test/release/rc-cluster-validation.md §3b carries three rows in exactly that
# state, and #958 is a SECURITY POSTURE claim: if `allowMetadataEgress` does not
# really scope to one host, the documentation is wrong whether or not a release
# is in flight.
#
# THE VACUOUS-PASS PROBLEM, AND WHAT IS DONE ABOUT IT. "The pod could not reach
# the metadata server" is not evidence of anything on its own. It is equally
# produced by:
#
#   - a CNI that enforces and dropped the packet          (the finding)
#   - a CNI that does not enforce, and a host that is
#     simply not there on this cloud                      (vacuous)
#   - a probe image whose `nc` does not do what we think  (vacuous)
#   - a pod that never got the policy's labels            (vacuous)
#
# So nothing here asserts a block until three things are established first:
#
#   1. THE DATAPLANE IS WHAT WE THINK. datapathProvider is ADVANCED_DATAPATH and
#      the Dataplane V2 node agent is actually Ready on every node. A cluster
#      that silently came up on the legacy datapath makes the whole run vacuous,
#      so the run STOPS here rather than reporting passes.
#   2. THE TARGET IS REACHABLE WITH NO POLICY AT ALL. Probed before anything is
#      applied. A host that does not answer at baseline can never demonstrate a
#      block, and any row that depends on it is reported UNPROVABLE rather than
#      passed.
#   3. A DROP IS DISTINGUISHED FROM A REFUSAL. A NetworkPolicy drop is silent:
#      the connection hangs to the timeout. A closed port answers instantly with
#      a reset. Collapsing those two into "failed to connect" is precisely how a
#      host that was never listening reads as a policy win, so every probe is
#      TIMED and classified ALLOWED / DROPPED / REFUSED.
#
# AND THE CONTROL THAT MAKES IT A MEASUREMENT. Two probe pods run side by side
# in the task namespace for the whole run:
#
#   probe-task   carries leoflow.io/run-id, which is what the chart's task
#                policy selects (helm/dexaflow/templates/task-networkpolicy.yaml
#                selects on `leoflow.io/run-id Exists`).
#   probe-plain  identical, WITHOUT that label. Nothing selects it, ever.
#
# The finding is the DIFFERENCE between them at the same instant on the same
# cluster. probe-plain keeping its access is what rules out "the metadata server
# went away", "the node lost egress", and "the cloud is having a moment", none
# of which a single-pod probe can separate from enforcement.
#
# Usage:
#   test/gcp/netpol.sh                 # print the plan and the cost, create nothing
#   test/gcp/netpol.sh --execute       # provision, run, and ALWAYS tear down
#   test/gcp/netpol.sh --self-test
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXP_REPO_ROOT="$(cd "$HERE/../.." && pwd)"
# shellcheck source=lib/experiment.sh
. "$HERE/lib/experiment.sh"

NODES="${NODES:-1}"
TTL="${TTL:-40m}"
EXECUTE=0
TASK_NS="leoflow"
# busybox for `nc`: small, and its connect-with-timeout is the whole probe. The
# tag is pinned because an experiment whose probe changes between runs is an
# experiment whose runs cannot be compared.
PROBE_IMAGE="${PROBE_IMAGE:-busybox:1.36}"
# 5 seconds, so a drop (which hangs until the timeout) is unambiguously separable
# from a reset (which returns in well under a second) at 1-second clock
# resolution, which is all busybox `date` offers.
NC_TIMEOUT="${NC_TIMEOUT:-5}"
DROP_FLOOR="${DROP_FLOOR:-4}"   # elapsed >= this means the packet was dropped, not refused

# ------------------------------------------------------------------ targets
#
# GKE's metadata server is 169.254.169.254. The §3b row is written against EKS
# and names 169.254.170.23, the EKS Pod Identity address, which DOES NOT EXIST
# on GKE. That difference is not a detail: probing 169.254.170.23 on GKE and
# finding it unreachable would "pass" the row while proving nothing at all,
# which is the vacuous shape this whole file exists to refuse. It is probed
# anyway, at baseline, precisely to show that it is unreachable BEFORE any
# policy, and is therefore useless as evidence here.
METADATA_HOST="169.254.169.254"; METADATA_PORT=80
EKS_PI_HOST="169.254.170.23";    EKS_PI_PORT=80
# A target outside the metadata range. The chart policy allows 0.0.0.0/0 EXCEPT
# 169.254.0.0/16, so this must stay ALLOWED for the selected pod. If it did not,
# the policy would be a blanket deny and "metadata is blocked" would be true for
# an uninteresting reason.
EXTERNAL_HOST="8.8.8.8";         EXTERNAL_PORT=53

# ------------------------------------------------------------- pure helpers

# classify_probe turns an exit status and an elapsed time into one of three
# words. This is the function that keeps a closed port from being reported as a
# policy drop, and it is pure so every branch is tested without a cluster.
classify_probe() { # <nc exit status> <elapsed seconds>
  local rc="$1" elapsed="$2"
  if [ "$rc" = "0" ]; then echo ALLOWED; return 0; fi
  if [ "${elapsed:-0}" -ge "$DROP_FLOOR" ]; then echo DROPPED; return 0; fi
  echo REFUSED
}

# row_verdict decides one §3b row from an observed classification and what the
# baseline said about that target. UNPROVABLE is a first-class outcome and is
# never folded into PASS: a row that could not be tested here has not passed.
row_verdict() { # <baseline classification> <observed classification> <expected: ALLOWED|DROPPED>
  local base="$1" got="$2" want="$3"
  # A target that was not reachable before any policy existed cannot demonstrate
  # that a policy blocked it. This is the guard for the EKS address on GKE.
  if [ "$base" != "ALLOWED" ] && [ "$want" = "DROPPED" ]; then
    echo UNPROVABLE; return 0
  fi
  [ "$got" = "$want" ] && { echo PASS; return 0; }
  echo FAIL
}

# enforcement_verdict is the gate everything else hangs from. It reads the same
# target from the selected pod and the unselected control pod, under the same
# policy, at the same moment.
#
# ENFORCING       selected dropped, control still allowed. The only shape that
#                 proves the CNI enforces AND the podSelector matched.
# NOT-ENFORCING   selected still allowed while a policy that should block it is
#                 applied. This is kindnet's behavior and it makes every other
#                 row in the run meaningless, so it is fatal.
# CONFOUNDED      both pods lost the target. Something other than the policy
#                 changed: the metadata server, the node, the cloud. Not a pass
#                 and not a failure of the policy; a failure of the experiment.
# INCONCLUSIVE    anything else, including the control being unreachable to
#                 begin with.
enforcement_verdict() { # <selected classification> <control classification>
  local sel="$1" ctl="$2"
  if [ "$sel" = "DROPPED" ] && [ "$ctl" = "ALLOWED" ]; then echo ENFORCING; return 0; fi
  if [ "$sel" = "ALLOWED" ]; then echo NOT-ENFORCING; return 0; fi
  if [ "$sel" = "DROPPED" ] && [ "$ctl" = "DROPPED" ]; then echo CONFOUNDED; return 0; fi
  echo INCONCLUSIVE
}

# ---------------------------------------------------------------- self-test

self_test() {
  local fail=0
  _eq() { [ "$1" = "$2" ] && { echo "  ok   $3"; return 0; }; echo "  FAIL $3: '$1' != '$2'"; fail=1; }

  # ----------------------------------------- classify_probe
  _eq "$(classify_probe 0 0)" "ALLOWED" "a connection that succeeded is allowed"
  _eq "$(classify_probe 1 5)" "DROPPED" "a failure that took the whole timeout is a silent drop"
  # THE case the whole experiment turns on. A closed port fails instantly, and
  # calling that a policy drop is how a host that was never listening becomes a
  # security finding.
  _eq "$(classify_probe 1 0)" "REFUSED" "a failure that returned instantly is a reset, NOT a policy drop"
  _eq "$(classify_probe 1 4)" "DROPPED" "the drop floor itself counts as dropped"
  _eq "$(classify_probe 1 3)" "REFUSED" "just under the floor is still a refusal"

  # ----------------------------------------- enforcement_verdict
  _eq "$(enforcement_verdict DROPPED ALLOWED)" "ENFORCING" \
      "selected dropped while the unselected control still reaches it is the only proof of enforcement"
  # kindnet's shape, and the reason this file exists.
  _eq "$(enforcement_verdict ALLOWED ALLOWED)" "NOT-ENFORCING" \
      "a selected pod that still reaches a blocked host means the CNI ignores the policy"
  _eq "$(enforcement_verdict ALLOWED DROPPED)" "NOT-ENFORCING" \
      "the selected pod reaching it is decisive whatever the control did"
  # Both losing it is the cloud, not the policy.
  _eq "$(enforcement_verdict DROPPED DROPPED)" "CONFOUNDED" \
      "both pods losing the target means something other than the policy changed"
  _eq "$(enforcement_verdict REFUSED ALLOWED)" "INCONCLUSIVE" \
      "a reset on the selected pod proves nothing about enforcement"
  _eq "$(enforcement_verdict DROPPED REFUSED)" "INCONCLUSIVE" \
      "a control that cannot reach the target cannot serve as a control"

  # ----------------------------------------- row_verdict
  _eq "$(row_verdict ALLOWED DROPPED DROPPED)" "PASS" "reachable at baseline, dropped under policy, is a real pass"
  _eq "$(row_verdict ALLOWED ALLOWED DROPPED)"  "FAIL" "reachable at baseline and still reachable under policy is a failure"
  # THE cross-cloud guard. The §3b row names 169.254.170.23, which does not
  # exist on GKE, so it is unreachable at baseline and a "block" on it is
  # meaningless. It must not be allowed to read as a pass.
  _eq "$(row_verdict REFUSED DROPPED DROPPED)" "UNPROVABLE" \
      "a target unreachable before any policy cannot demonstrate a block (the EKS address on GKE)"
  _eq "$(row_verdict DROPPED DROPPED DROPPED)" "UNPROVABLE" \
      "a target already dropped at baseline cannot demonstrate that a policy dropped it"
  _eq "$(row_verdict ALLOWED ALLOWED ALLOWED)" "PASS" "the allow-hatch row passes when the host answers"
  _eq "$(row_verdict ALLOWED DROPPED ALLOWED)" "FAIL" "an allow-hatch that does not restore the host is a failure"
  # An expectation of ALLOWED is provable even from a baseline that was not,
  # because "the hatch made it reachable" is a statement about the hatch.
  _eq "$(row_verdict REFUSED ALLOWED ALLOWED)" "PASS" "an unreachable baseline does not block an ALLOW expectation"

  # ----------------------------------------- the policy argv
  # THE defect a real run found, at the cost of a provisioned cluster: an
  # optional flag appended via an array that is EMPTY in the most important
  # case. Under `set -u` on bash 3.2 that is a fatal unbound-variable error,
  # and the empty hatch is exactly the case the enforcement gate depends on.
  build_policy_args ""
  [ "${#POLICY_ARGS[@]}" -gt 0 ] \
    && echo "  ok   the empty-hatch argv builds at all (bash 3.2 + set -u dies on an empty array expansion)" \
    || { echo "  FAIL the empty-hatch argv is empty"; fail=1; }
  case " ${POLICY_ARGS[*]} " in
    *"allowMetadataEgress"*) echo "  FAIL the empty hatch still passes allowMetadataEgress"; fail=1 ;;
    *) echo "  ok   an empty hatch passes no allowMetadataEgress at all, leaving the chart default" ;;
  esac
  case " ${POLICY_ARGS[*]} " in
    *"taskNetworkPolicy.enabled=true"*) echo "  ok   the policy is actually enabled, or the render would be empty and apply nothing" ;;
    *) echo "  FAIL the argv does not enable the task policy, so the render would be empty"; fail=1 ;;
  esac
  build_policy_args "169.254.169.254/32"
  case " ${POLICY_ARGS[*]} " in
    *"allowMetadataEgress={169.254.169.254/32}"*) echo "  ok   a hatch value reaches the render as a one-element list" ;;
    *) echo "  FAIL the hatch value does not reach the render"; fail=1 ;;
  esac
  # The render must be the CHART's template, not a hand-written copy: a copy
  # drifts from the artifact operators actually install, and then the
  # experiment proves something about the copy.
  case " ${POLICY_ARGS[*]} " in
    *"templates/task-networkpolicy.yaml"*) echo "  ok   the policy under test is the chart's own template, not a local copy" ;;
    *) echo "  FAIL the policy is not rendered from the chart template"; fail=1 ;;
  esac

  # ----------------------------------------- the cross-cloud constants
  # Guards a plausible and damaging edit: copying the §3b row's literal EKS
  # address into the GKE probe. The row says 169.254.170.23 because it was
  # written for EKS Pod Identity; GKE Workload Identity is 169.254.169.254.
  _eq "$METADATA_HOST" "169.254.169.254" "the GKE metadata host is the GKE one, not the EKS one copied from the row"
  [ "$METADATA_HOST" != "$EKS_PI_HOST" ] \
    && echo "  ok   the EKS address is kept as a separate, deliberately-unreachable baseline probe" \
    || { echo "  FAIL the EKS and GKE metadata addresses have been collapsed into one"; fail=1; }
  # The external target must be outside the blocked range, or "egress still
  # works" would be testing the same range as "egress is blocked".
  case "$EXTERNAL_HOST" in
    169.254.*) echo "  FAIL the external control target is inside the blocked metadata range"; fail=1 ;;
    *) echo "  ok   the external control target is outside 169.254.0.0/16" ;;
  esac
  # The timeout must exceed the drop floor, or nothing can ever be classified
  # DROPPED and every block would read as a refusal.
  [ "$NC_TIMEOUT" -gt "$DROP_FLOOR" ] \
    && echo "  ok   the connect timeout exceeds the drop floor, so a drop is classifiable" \
    || { echo "  FAIL the timeout is at or below the drop floor; no probe could ever be DROPPED"; fail=1; }

  [ "$fail" = "0" ] && { echo "netpol self-test: ok"; return 0; }
  return 1
}

# --------------------------------------------------------------- cluster side

probe_manifest() { # <namespace>
  cat <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: probe-task
  namespace: $1
  labels:
    # The label the chart's task policy selects on (podSelector: leoflow.io/run-id
    # Exists). A real task pod carries it; this probe stands in for one.
    leoflow.io/run-id: netpol-probe
spec:
  restartPolicy: Never
  containers:
    - name: probe
      image: $PROBE_IMAGE
      command: ["sh", "-c", "sleep 86400"]
      resources:
        requests: { cpu: "50m", memory: "32Mi" }
        limits:   { cpu: "50m", memory: "32Mi" }
---
apiVersion: v1
kind: Pod
metadata:
  name: probe-plain
  namespace: $1
  # NO leoflow.io/run-id. Nothing selects this pod, so it is the control: what
  # it can still reach is what the cluster can still reach.
spec:
  restartPolicy: Never
  containers:
    - name: probe
      image: $PROBE_IMAGE
      command: ["sh", "-c", "sleep 86400"]
      resources:
        requests: { cpu: "50m", memory: "32Mi" }
        limits:   { cpu: "50m", memory: "32Mi" }
YAML
}

# probe runs one timed connect from one pod and prints the classification.
# The timing happens INSIDE the pod, so kubectl/apiserver latency is not counted
# as connect time, which at a 4-second floor would otherwise be enough to turn a
# slow exec into a "drop".
probe() { # <pod> <host> <port>  -> ALLOWED|DROPPED|REFUSED
  local pod="$1" host="$2" port="$3" out rc elapsed
  out="$(kubectl -n "$TASK_NS" exec "$pod" -- sh -c "
    s=\$(date +%s)
    nc -w $NC_TIMEOUT -z $host $port >/dev/null 2>&1; rc=\$?
    e=\$(( \$(date +%s) - s ))
    echo \"\$rc \$e\"
  " 2>/dev/null)" || out="1 0"
  rc="$(printf '%s' "$out" | awk '{print $1}')"
  elapsed="$(printf '%s' "$out" | awk '{print $2}')"
  classify_probe "${rc:-1}" "${elapsed:-0}"
}

# build_policy_args assembles the `helm template` argv for the task policy.
#
# In a function, never inline, for the reason provision.sh builds its gcloud
# argv in one: it is the only way a self-test can check the argv without a
# cluster. It earned that immediately. The first version appended an optional
# flag through an array expanded as "${extra[@]}", which under `set -u` on bash
# 3.2 (what macOS ships, and what provision.sh already accommodates) is an
# UNBOUND VARIABLE ERROR when the array is empty, not an empty expansion. So
# the empty-hatch case died mid-run on a live cluster, after the money was
# already spent, while the non-empty case would have worked. The argv is built
# by appending to one array that is never empty.
build_policy_args() { # <allowMetadataEgress value or empty>
  local hatch="$1"
  POLICY_ARGS=(template leoflow "$EXP_REPO_ROOT/helm/dexaflow"
    --namespace leoflow-system
    -s templates/task-networkpolicy.yaml
    --set "taskNamespace=$TASK_NS"
    --set taskNetworkPolicy.enabled=true
    --set "database.url=postgres://unused" --set "redis.url=redis://unused"
    --set-string auth.jwtSecret=unused --set-string bootstrap.password=unused)
  if [ -n "$hatch" ]; then
    POLICY_ARGS+=(--set "taskNetworkPolicy.allowMetadataEgress={$hatch}")
  fi
  return 0
}

apply_task_policy() { # <allowMetadataEgress value or empty>
  build_policy_args "$1"
  helm "${POLICY_ARGS[@]}" | kubectl apply -f - >/dev/null
}

run_experiment() {
  local out="$1"
  exp_require gcloud kubectl helm
  exp_require_env

  exp_arm_teardown "$HERE/teardown.sh"
  exp_provision "$HERE/provision.sh" netpol "$NODES" "$TTL"
  gcloud container clusters get-credentials "$EXP_CLUSTER" \
    --zone "${GCP_ZONE:?}" --project "${GCP_PROJECT:?}" >/dev/null 2>&1 \
    || exp_die "could not get credentials for $EXP_CLUSTER"
  exp_kube_ready "$NODES" 600

  # ---- gate 1: the dataplane is what we think ----------------------------
  # Everything downstream is vacuous if this is wrong, so it is fatal and it is
  # first. Both halves are checked: the cluster's declared datapath AND the node
  # agent that actually enforces. A cluster can report ADVANCED_DATAPATH while
  # its agent is not yet Ready on a node, and a probe scheduled onto that node
  # would find nothing enforced.
  local dp; dp="$(gcloud container clusters describe "$EXP_CLUSTER" \
    --zone "$GCP_ZONE" --project "$GCP_PROJECT" \
    --format='value(networkConfig.datapathProvider)' 2>/dev/null || true)"
  printf 'datapathProvider=%s\n' "$dp" >> "$out/facts.txt"
  [ "$dp" = "ADVANCED_DATAPATH" ] \
    || exp_die "datapathProvider is '${dp:-unset}', not ADVANCED_DATAPATH. Dataplane V2 is the only reason this experiment is on a cloud at all; on anything else every assertion below would pass vacuously."
  exp_ok "datapathProvider=ADVANCED_DATAPATH"

  local want_agents ready_agents
  want_agents="$(kubectl get nodes --no-headers | wc -l | tr -d ' ')"
  ready_agents="$(kubectl -n kube-system get ds anetd -o jsonpath='{.status.numberReady}' 2>/dev/null || echo 0)"
  printf 'anetd_ready=%s want=%s\n' "$ready_agents" "$want_agents" >> "$out/facts.txt"
  [ "${ready_agents:-0}" -ge "${want_agents:-1}" ] \
    || exp_die "the Dataplane V2 agent (anetd) is Ready on ${ready_agents:-0} of $want_agents node(s). A pod on a node whose agent is not up is a pod whose policy is not enforced, and this run would report that as a pass."
  exp_ok "the Dataplane V2 node agent is Ready on all $want_agents node(s)"

  # ---- probes ------------------------------------------------------------
  kubectl create namespace "$TASK_NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  probe_manifest "$TASK_NS" | kubectl apply -f - >/dev/null
  kubectl -n "$TASK_NS" wait --for=condition=Ready pod/probe-task pod/probe-plain --timeout=300s >/dev/null \
    || exp_die "the probe pods never became Ready; nothing can be measured"
  exp_ok "probe-task (selected) and probe-plain (control) are Ready"

  # ---- gate 2: baseline, with NO policy applied --------------------------
  # What is reachable when nothing is blocking. Every later "it was blocked"
  # claim is read against this, and a target that fails here is reported
  # UNPROVABLE rather than passed.
  exp_log "baseline reachability, no NetworkPolicy applied at all"
  local b_meta b_eks b_ext
  b_meta="$(probe probe-task "$METADATA_HOST" "$METADATA_PORT")"
  b_eks="$(probe  probe-task "$EKS_PI_HOST"   "$EKS_PI_PORT")"
  b_ext="$(probe  probe-task "$EXTERNAL_HOST" "$EXTERNAL_PORT")"
  {
    printf 'baseline gke_metadata %s:%s %s\n' "$METADATA_HOST" "$METADATA_PORT" "$b_meta"
    printf 'baseline eks_pod_identity %s:%s %s\n' "$EKS_PI_HOST" "$EKS_PI_PORT" "$b_eks"
    printf 'baseline external %s:%s %s\n' "$EXTERNAL_HOST" "$EXTERNAL_PORT" "$b_ext"
  } >> "$out/series.txt"
  exp_log "baseline: gke_metadata=$b_meta eks_pod_identity=$b_eks external=$b_ext"
  [ "$b_meta" = "ALLOWED" ] \
    || exp_die "the GKE metadata server is $b_meta with no policy applied, so a later block cannot be attributed to the policy. Nothing here would be a measurement."

  # ---- the enforcement gate ---------------------------------------------
  exp_log "applying the chart's real task policy with allowMetadataEgress: []"
  apply_task_policy ""
  sleep "${POLICY_SETTLE:-10}"
  local sel ctl verdict
  sel="$(probe probe-task  "$METADATA_HOST" "$METADATA_PORT")"
  ctl="$(probe probe-plain "$METADATA_HOST" "$METADATA_PORT")"
  verdict="$(enforcement_verdict "$sel" "$ctl")"
  {
    printf 'closed selected %s:%s %s\n' "$METADATA_HOST" "$METADATA_PORT" "$sel"
    printf 'closed control %s:%s %s\n' "$METADATA_HOST" "$METADATA_PORT" "$ctl"
    printf 'enforcement %s\n' "$verdict"
  } >> "$out/series.txt"
  exp_log "enforcement: selected=$sel control=$ctl -> $verdict"
  case "$verdict" in
    ENFORCING) exp_ok "the CNI enforces and the podSelector matched; the rows below mean something" ;;
    NOT-ENFORCING) exp_die "the selected pod still reaches the metadata server under a policy that blocks it. This cluster is not enforcing, so every row below would pass vacuously. Stopping." ;;
    CONFOUNDED) exp_die "both the selected pod AND the unselected control lost the metadata server. Something other than the policy changed; this is a broken experiment, not a finding." ;;
    *) exp_die "enforcement is $verdict; refusing to assert anything on top of it" ;;
  esac

  # ---- #958: the hatch is exactly one host -------------------------------
  local r_block r_hatch h_meta h_ext
  r_block="$(row_verdict "$b_meta" "$sel" DROPPED)"

  exp_log "applying allowMetadataEgress: [$METADATA_HOST/32] (the GKE Workload Identity address)"
  apply_task_policy "$METADATA_HOST/32"
  sleep "${POLICY_SETTLE:-10}"
  h_meta="$(probe probe-task "$METADATA_HOST" "$METADATA_PORT")"
  h_ext="$(probe  probe-task "$EXTERNAL_HOST" "$EXTERNAL_PORT")"
  r_hatch="$(row_verdict "$b_meta" "$h_meta" ALLOWED)"
  {
    printf 'hatch selected %s:%s %s\n' "$METADATA_HOST" "$METADATA_PORT" "$h_meta"
    printf 'hatch external %s:%s %s\n' "$EXTERNAL_HOST" "$EXTERNAL_PORT" "$h_ext"
  } >> "$out/series.txt"

  # ---- the report --------------------------------------------------------
  {
    echo "cluster: $EXP_CLUSTER"
    echo "datapath: $dp (Dataplane V2), anetd Ready on $ready_agents/$want_agents nodes"
    echo "enforcement gate: $verdict"
    echo
    echo "#958 metadata blocked with an empty hatch : $r_block  (baseline=$b_meta observed=$sel)"
    echo "#958 the /32 hatch restores that one host : $r_hatch  (observed=$h_meta)"
    echo "egress outside the range still works      : $h_ext (expected ALLOWED; a blanket deny would make the rows above meaningless)"
    echo
    echo "WHAT THIS RUN CANNOT SEE"
    echo "  - EKS. The §3b row names 169.254.170.23 (EKS Pod Identity), which was"
    echo "    $b_eks at baseline here because it does not exist on GKE. The EKS half"
    echo "    of that row is UNPROVABLE on this cluster and is NOT reported as passed."
    echo "  - Calico. The row asks for GKE Dataplane V2, EKS VPC CNI and Calico."
    echo "    Only Dataplane V2 was exercised."
    echo "  - A real task pod. probe-task is a busybox pod wearing the label the"
    echo "    policy selects. It proves the POLICY and the CNI. It does not prove"
    echo "    that the executor puts that label on a real task pod; that is a"
    echo "    separate claim about executor/kubernetes.go."
    echo "  - Whether anything ELSE in 169.254.0.0/16 stayed blocked while the"
    echo "    hatch was open. That needs a second host in the range that answers"
    echo "    at baseline, and on this cluster there was none (eks_pod_identity"
    echo "    was $b_eks). The additivity concern #958 documents is therefore"
    echo "    NOT re-proven here beyond the single-host case."
  } | tee "$out/report.txt"

  case "$r_block:$r_hatch" in
    PASS:PASS) exp_ok "both #958 rows hold on Dataplane V2" ;;
    *) exp_warn "at least one row did not pass; see $out/report.txt"; return 1 ;;
  esac
}

# ---------------------------------------------------------------------- main

while [ $# -gt 0 ]; do
  case "$1" in
    --execute)   EXECUTE=1; shift ;;
    --nodes)     NODES="$2"; shift 2 ;;
    --ttl)       TTL="$2"; shift 2 ;;
    --self-test) self_test; exit $? ;;
    -h|--help)   sed -n '2,/^set -euo pipefail/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) exp_die "unknown flag: $1" ;;
  esac
done

if [ "$EXECUTE" != "1" ]; then
  "$HERE/provision.sh" --dry-run --experiment netpol --nodes "$NODES" --ttl "$TTL"
  exp_never_ran "netpol, $NODES node(s) for $TTL on GKE Dataplane V2" \
    "not invoked with --execute; this printed the plan and created nothing"
  exit 0
fi

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="$EXP_REPO_ROOT/$(exp_run_dir netpol "$STAMP")"
mkdir -p "$OUT"
exp_log "raw series and report go to $OUT"
run_experiment "$OUT"
