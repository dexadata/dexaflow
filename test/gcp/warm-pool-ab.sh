#!/usr/bin/env bash
#
# warm-pool-ab: measure what warm pools (ADR 0058) actually buy, on the only
# executor that has them.
#
# WHY IT NEEDS A CLUSTER. Warm pools exist only in the Kubernetes executor.
# Lite's subprocess executor has no pool at all, so the local soak cannot see
# this feature even in principle.
#
# ─────────────────── THIS IS NOT A ONE-VARIABLE COMPARISON ───────────────────
#
# Turning warm pools on is not a single flag. The chart REFUSES to render
# unless two other settings move with it, and the server enforces the same
# coupling at boot, so an install that changed only one would CrashLoopBackOff
# rather than run:
#
#   helm/dexaflow/templates/deployment.yaml:199-201
#     "execution.warmPoolsEnabled requires auth.agentTokenTransport=exchange
#      AND auth.secretLivenessMode=enforce"
#
# The reason is a real security invariant, not an accident of configuration
# (ADR 0058 D2): a warm pod outlives the attempt it was created for, so a
# credential that outlives an attempt would let a SUPERSEDED attempt still
# resolve secrets. `exchange` gives each attempt a token that dies with it, and
# `enforce` denies secret delivery to an attempt that is no longer live.
#
# So "warm pools on" is really THREE changes at once:
#
#   execution.warmPoolsEnabled   false -> true    the feature
#   auth.agentTokenTransport     envvar -> exchange   a different auth path for
#                                                EVERY task pod, warm or not:
#                                                a projected ServiceAccount
#                                                token traded via a
#                                                control-plane TokenReview
#   auth.secretLivenessMode      observe -> enforce   a liveness check on every
#                                                secret delivery
#
# A two-arm A/B across that bundle CANNOT attribute a difference to warm pools.
# The exchange transport alone adds a TokenReview round trip to every pod start,
# which pushes the measured number in the OPPOSITE direction to the one warm
# pools are supposed to move it. Reporting "warm pools made it faster" from such
# a comparison would be wrong even if the number were right.
#
# THE FIX IS A THIRD ARM, and it is the reason this file exists rather than a
# two-arm script:
#
#   A   envvar   + observe + warm OFF   today's default posture
#   A'  exchange + enforce + warm OFF   the coupled prerequisites, WITHOUT the
#                                       feature. This is the real control.
#   B   exchange + enforce + warm ON    the feature
#
#   B vs A'  isolates warm pools. ONE variable. This is the scientific result.
#   B vs A   is what an operator actually experiences when they "turn on warm
#            pools", bundle and all. This is the operational result.
#   A' vs A  is the price of the prerequisites on their own, which is the number
#            nobody has and which decides whether the bundle is worth it when
#            the pool misses.
#
# Both differences are reported, separately and labeled. Neither is called "the
# warm pool speedup" on its own.
#
# ─────────────────────────── ORDERING IS A CONFOUND ──────────────────────────
#
# The arms run sequentially on one cluster, because three clusters costs three
# times as much and introduces a bigger confound than the one it removes. But
# sequential arms drift: the image is cached after arm A, the nodes are warm,
# the zone's neighbours change. So the order is A, A', B, then A AGAIN, and the
# two A measurements are compared to each other. If they disagree by more than
# DRIFT_TOLERANCE, the cluster drifted under the experiment and the A/B
# difference is reported as UNTRUSTWORTHY rather than as a result.
#
# ──────────────────────────── WHAT IT CANNOT SEE ─────────────────────────────
#
# - Pool behavior under eviction, preemption or node loss. Nothing here kills a
#   node, so the reclaim path (ADR 0058 N1b) is not exercised.
# - Multi-tenant contention. One tenant, one dag_version. maxPoolSize and the
#   per-tenant ceiling (M4) are not pressured.
# - Steady state. The run is minutes, and idleTtl / maxAttempts / maxLifetime
#   recycling are all longer than that, so recycle is NOT measured.
# - The cold-start distribution of a real DAG image. The image here is a small
#   fixture; a 2 GB DAG image changes the arithmetic entirely and in warm pools'
#   favor, so any speedup measured here is a LOWER bound for heavy images and
#   says nothing about light ones being representative.
#
# Usage:
#   test/gcp/warm-pool-ab.sh                 # plan + cost, creates nothing
#   test/gcp/warm-pool-ab.sh --execute       # provision, run all arms, tear down
#   test/gcp/warm-pool-ab.sh --self-test
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXP_REPO_ROOT="$(cd "$HERE/../.." && pwd)"
# shellcheck source=lib/experiment.sh
. "$HERE/lib/experiment.sh"
# shellcheck source=lib/stats.sh
. "$HERE/lib/stats.sh"
# shellcheck source=lib/stack.sh
. "$HERE/lib/stack.sh"

NODES="${NODES:-3}"
TTL="${TTL:-2h}"
EXECUTE=0
# Attempts per arm. Twenty is MIN_SAMPLES in lib/stats.sh, which is the floor
# below which a percentile is reported as inconclusive rather than used.
ATTEMPTS="${ATTEMPTS:-30}"
# If the two arm-A measurements differ by more than this ratio, the cluster
# drifted under the experiment and no arm comparison from it is trustworthy.
DRIFT_TOLERANCE="${DRIFT_TOLERANCE:-1.5}"

ARMS="A Aprime B"

# The CLI that builds and pushes the DAG image. Built from this tree rather than
# downloaded: the experiment is about this source, not about a released binary.
WP_CLI="${WP_CLI:-$EXP_REPO_ROOT/.bin/leoflow}"
WP_DAG_VERSION="${WP_DAG_VERSION:-gcpexp1}"
WP_JWT=""
WP_PW=""

# ------------------------------------------------------------- pure helpers

# arm_values maps an arm to the three coupled settings. One function, so the
# coupling can never be half-applied by a caller that remembers only the flag
# the experiment is named after.
arm_values() { # <arm>  -> three --set arguments
  case "$1" in
    A)
      echo "execution.warmPoolsEnabled=false auth.agentTokenTransport=envvar auth.secretLivenessMode=observe" ;;
    Aprime)
      echo "execution.warmPoolsEnabled=false auth.agentTokenTransport=exchange auth.secretLivenessMode=enforce" ;;
    B)
      echo "execution.warmPoolsEnabled=true auth.agentTokenTransport=exchange auth.secretLivenessMode=enforce" ;;
    *) return 1 ;;
  esac
}

# arm_is_renderable encodes the chart's own refusal, so a bad arm definition is
# caught by the self-test instead of by a failed helm upgrade on a paid cluster.
# The rule is at helm/dexaflow/templates/deployment.yaml:199-201.
arm_is_renderable() { # <warm> <transport> <liveness>
  local warm="$1" transport="$2" liveness="$3"
  [ "$warm" != "true" ] && return 0
  [ "$transport" = "exchange" ] && [ "$liveness" = "enforce" ]
}

# ratio_of is the comparison between two arms, guarded against a zero baseline
# producing an infinite "improvement".
ratio_of() { # <baseline> <other>
  awk -v a="$1" -v b="$2" 'BEGIN{ if (a+0 <= 0) { print "undefined"; exit } printf "%.3f", b/a }'
}

# drift_verdict compares the two arm-A measurements taken at the start and end.
# This is what decides whether anything else in the run may be believed.
drift_verdict() { # <first A p95> <second A p95>
  local a="$1" b="$2" r
  r="$(ratio_of "$a" "$b")"
  [ "$r" = "undefined" ] && { echo UNTRUSTWORTHY; return 0; }
  # Drift in EITHER direction invalidates: the cluster getting faster over the
  # run flatters a later arm exactly as much as getting slower penalizes it.
  awk -v r="$r" -v t="$DRIFT_TOLERANCE" 'BEGIN{ exit !(r <= t && r >= 1/t) }' \
    && echo STABLE || echo UNTRUSTWORTHY
}

# ---------------------------------------------------------------- self-test

self_test() {
  local fail=0
  _eq() { [ "$1" = "$2" ] && { echo "  ok   $3"; return 0; }; echo "  FAIL $3: '$1' != '$2'"; fail=1; }

  # ------------------------------------- the coupling is not optional
  # THE claim this file is built around. Arm B must move all three settings,
  # and the control arm must move exactly two.
  local a ap b
  a="$(arm_values A)"; ap="$(arm_values Aprime)"; b="$(arm_values B)"

  case "$b" in
    *"warmPoolsEnabled=true"*) echo "  ok   arm B turns warm pools on" ;;
    *) echo "  FAIL arm B does not turn warm pools on"; fail=1 ;;
  esac
  case "$b" in
    *"agentTokenTransport=exchange"*"secretLivenessMode=enforce"*)
      echo "  ok   arm B also carries BOTH coupled prerequisites, which the chart refuses to render without" ;;
    *) echo "  FAIL arm B omits a coupled prerequisite; the chart would refuse to render it"; fail=1 ;;
  esac
  # The control arm is the whole point: same prerequisites, feature off.
  case "$ap" in
    *"warmPoolsEnabled=false"*"agentTokenTransport=exchange"*"secretLivenessMode=enforce"*)
      echo "  ok   arm A' carries the prerequisites WITHOUT the feature, which is what isolates warm pools" ;;
    *) echo "  FAIL arm A' is not the coupled control; B vs A' would not isolate one variable"; fail=1 ;;
  esac
  case "$a" in
    *"warmPoolsEnabled=false"*"agentTokenTransport=envvar"*"secretLivenessMode=observe"*)
      echo "  ok   arm A is today's default posture" ;;
    *) echo "  FAIL arm A is not the default posture, so A' vs A does not price the prerequisites"; fail=1 ;;
  esac
  # A' and B must differ in EXACTLY the warm-pool flag. If they differed in
  # anything else, B vs A' would be another bundle comparison wearing the name
  # of a controlled one, which is the exact error this file exists to avoid.
  local diff_count
  diff_count="$(python3 - "$ap" "$b" <<'PY'
import sys
a = dict(kv.split("=", 1) for kv in sys.argv[1].split())
b = dict(kv.split("=", 1) for kv in sys.argv[2].split())
print(sum(1 for k in set(a) | set(b) if a.get(k) != b.get(k)))
PY
)"
  _eq "$diff_count" "1" "arm A' and arm B differ in EXACTLY one setting, so B vs A' is a controlled comparison"

  arm_values nonsense >/dev/null 2>&1 \
    && { echo "  FAIL an unknown arm produced settings"; fail=1; } \
    || echo "  ok   an unknown arm is refused rather than silently defaulted"

  # ------------------------------------- the chart's render guard, mirrored
  arm_is_renderable true exchange enforce \
    && echo "  ok   warm pools with both prerequisites renders" \
    || { echo "  FAIL the fully-coupled arm was judged unrenderable"; fail=1; }
  # Each half missing, separately: the chart refuses both, and so must we.
  arm_is_renderable true envvar enforce \
    && { echo "  FAIL warm pools with envvar transport was judged renderable; the chart fails that render"; fail=1; } \
    || echo "  ok   warm pools without exchange transport is refused, as the chart refuses it"
  arm_is_renderable true exchange observe \
    && { echo "  FAIL warm pools with observe liveness was judged renderable; the chart fails that render"; fail=1; } \
    || echo "  ok   warm pools without enforce liveness is refused, as the chart refuses it"
  # With the feature off, the prerequisites are free to be anything.
  arm_is_renderable false envvar observe \
    && echo "  ok   with warm pools off, the prerequisites are unconstrained" \
    || { echo "  FAIL the default posture was judged unrenderable"; fail=1; }
  arm_is_renderable false exchange enforce \
    && echo "  ok   the coupled control arm renders with the feature off" \
    || { echo "  FAIL the control arm was judged unrenderable"; fail=1; }

  # Every arm this run would actually execute must be renderable, checked by
  # walking ARMS rather than by listing them again here.
  local arm vals
  for arm in $ARMS; do
    vals="$(arm_values "$arm")"
    # shellcheck disable=SC2086
    set -- $vals
    local w t l
    w="${1#*=}"; t="${2#*=}"; l="${3#*=}"
    if arm_is_renderable "$w" "$t" "$l"; then
      echo "  ok   arm $arm renders"
    else
      echo "  FAIL arm $arm would be refused by the chart at render time"; fail=1
    fi
  done

  # ------------------------------------- the guard, checked against the CHART
  # arm_is_renderable above is a MIRROR of a rule that lives in the chart, and a
  # mirror drifts. This drives the real template through every combination, so
  # the day the chart's coupling changes, this fails here rather than during a
  # paid run. Skipped loudly when helm is absent, because a gate that silently
  # stops running is the defect it was meant to prevent.
  if command -v helm >/dev/null 2>&1 && [ -d "$EXP_REPO_ROOT/helm/dexaflow" ]; then
    local w t l expect got
    for w in true false; do
      for t in envvar exchange; do
        for l in observe enforce; do
          if arm_is_renderable "$w" "$t" "$l"; then expect=RENDERS; else expect=REFUSED; fi
          if helm template leoflow "$EXP_REPO_ROOT/helm/dexaflow" \
               --set "database.url=postgres://x" --set "redis.url=redis://x" \
               --set-string auth.jwtSecret=a --set-string bootstrap.password=b \
               --set "execution.warmPoolsEnabled=$w" \
               --set "auth.agentTokenTransport=$t" \
               --set "auth.secretLivenessMode=$l" >/dev/null 2>&1; then got=RENDERS; else got=REFUSED; fi
          if [ "$expect" = "$got" ]; then
            echo "  ok   chart agrees: warm=$w transport=$t liveness=$l -> $got"
          else
            echo "  FAIL chart disagrees with arm_is_renderable: warm=$w transport=$t liveness=$l expected $expect got $got"
            fail=1
          fi
        done
      done
    done
  else
    echo "  ..   skipped: no helm or no chart, so the coupling was NOT checked against the real template"
  fi

  # ------------------------------------- ratios and drift
  _eq "$(ratio_of 10 5)" "0.500" "an arm at half the baseline reports 0.5"
  _eq "$(ratio_of 10 20)" "2.000" "an arm at twice the baseline reports 2.0"
  # A zero baseline would make every comparison an infinite improvement.
  _eq "$(ratio_of 0 5)" "undefined" "a zero baseline is undefined, not an infinite speedup"

  _eq "$(drift_verdict 10 10)"  "STABLE" "two identical A measurements are stable"
  _eq "$(drift_verdict 10 12)"  "STABLE" "a small drift is tolerated"
  # THE guard. The cluster got much slower between the two A arms, so the B
  # number is not comparable to the A number and must not be reported as one.
  _eq "$(drift_verdict 10 30)"  "UNTRUSTWORTHY" "a cluster that slowed under the run invalidates the comparison"
  # And the other direction, which is the one people forget: a cluster that got
  # FASTER flatters whichever arm ran last, which is B.
  _eq "$(drift_verdict 30 10)"  "UNTRUSTWORTHY" "a cluster that sped up under the run also invalidates it, because it flatters the last arm"
  _eq "$(drift_verdict 0 10)"   "UNTRUSTWORTHY" "an undefined ratio cannot be stable"

  # ------------------------------------- the sample floor
  # An arm with fewer attempts than MIN_SAMPLES cannot carry a p95, and the
  # default must not be below the floor the stats library enforces.
  [ "$ATTEMPTS" -ge "$MIN_SAMPLES" ] \
    && echo "  ok   the default attempt count is at or above the percentile floor ($MIN_SAMPLES)" \
    || { echo "  FAIL the default attempt count ($ATTEMPTS) is below the $MIN_SAMPLES floor, so every arm would be inconclusive"; fail=1; }

  # ------------------------------------- the run order
  # A must be measured twice, first and last, or drift cannot be detected at all.
  case "$RUN_ORDER" in
    "A "*" A") echo "  ok   the run order starts and ends with arm A, so drift is detectable" ;;
    *) echo "  FAIL the run order does not bracket the experiment with arm A: '$RUN_ORDER'"; fail=1 ;;
  esac

  # ------------------------------------- the report, driven from fixtures
  #
  # wp_report is the step that used to `return 1` unimplemented, so a paid run
  # ended with raw JSON and no summary (#1203). These cases drive it over series
  # files on disk, with no cluster and no API, and the refusals matter more than
  # the happy path: an arm that measured nothing must never render as an arm
  # that was very fast.
  local t
  t="$(mktemp -d)"

  _series() { # <dir> <arm> <idx> <values...>
    local d="$1" a="$2" i="$3"; shift 3
    printf '%s\n' "$@" > "$d/start-$a-$i.series"
  }

  # A complete, stable run: arm B twice as fast as A', A stable end to end.
  mkdir -p "$t/ok"
  _series "$t/ok" A      1 $(seq 1 30 | sed 's/.*/10/')
  _series "$t/ok" Aprime 2 $(seq 1 30 | sed 's/.*/12/')
  _series "$t/ok" B      3 $(seq 1 30 | sed 's/.*/6/')
  _series "$t/ok" A      4 $(seq 1 30 | sed 's/.*/10/')
  if wp_report "$t/ok" >/dev/null 2>&1; then
    echo "  ok   a complete run with stable arm A reports"
  else
    echo "  FAIL a complete, stable run was refused"; fail=1
  fi
  _eq "$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["ratios"]["b_vs_aprime"])' "$t/ok/verdict.json")" \
      "0.500" "B at half of A-prime is reported as 0.500, the feature in isolation"
  _eq "$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["drift"])' "$t/ok/verdict.json")" \
      "STABLE" "arm A measured the same twice is STABLE"
  _eq "$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["trustworthy"])' "$t/ok/verdict.json")" \
      "True" "and the run is marked trustworthy"
  grep -q 'B vs A' "$t/ok/report.md" \
    && echo "  ok   the report names the comparison that isolates the feature" \
    || { echo "  FAIL the report omits the B vs A-prime row"; fail=1; }

  # An arm that measured NOTHING. This is the shape of a warm pool that never
  # filled, a deploy that 404'd, and a token that expired mid-run.
  mkdir -p "$t/missing"
  _series "$t/missing" A      1 $(seq 1 30 | sed 's/.*/10/')
  _series "$t/missing" Aprime 2 $(seq 1 30 | sed 's/.*/12/')
  _series "$t/missing" A      4 $(seq 1 30 | sed 's/.*/10/')
  if wp_report "$t/missing" >/dev/null 2>&1; then
    echo "  FAIL an arm with no observations was reported as a result"; fail=1
  else
    echo "  ok   an arm that measured nothing REFUSES a verdict rather than reading as fast"
  fi
  [ -f "$t/missing/verdict.json" ] \
    && { echo "  FAIL a refused run still wrote a verdict file"; fail=1; } \
    || echo "  ok   and writes no verdict.json for a later reader to mistake for one"

  # A cluster that moved under the run. The numbers are kept; the conclusion is
  # withheld, because arm B ran last and drift flatters or penalizes it.
  mkdir -p "$t/drifted"
  _series "$t/drifted" A      1 $(seq 1 30 | sed 's/.*/10/')
  _series "$t/drifted" Aprime 2 $(seq 1 30 | sed 's/.*/12/')
  _series "$t/drifted" B      3 $(seq 1 30 | sed 's/.*/6/')
  _series "$t/drifted" A      4 $(seq 1 30 | sed 's/.*/40/')
  if wp_report "$t/drifted" >/dev/null 2>&1; then
    echo "  FAIL a run whose cluster drifted reported success"; fail=1
  else
    echo "  ok   a drifted cluster fails the run"
  fi
  _eq "$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["trustworthy"])' "$t/drifted/verdict.json")" \
      "False" "and its verdict says so rather than omitting the field"
  grep -q 'must not be quoted' "$t/drifted/report.md" \
    && echo "  ok   and the report says the numbers must not be quoted" \
    || { echo "  FAIL the drifted report does not warn the reader"; fail=1; }

  # Below the percentile floor. The arms are all present, so a verdict IS
  # written, but the run is not a pass.
  mkdir -p "$t/thin"
  _series "$t/thin" A      1 10 10 10
  _series "$t/thin" Aprime 2 12 12 12
  _series "$t/thin" B      3 6 6 6
  _series "$t/thin" A      4 10 10 10
  if wp_report "$t/thin" >/dev/null 2>&1; then
    echo "  FAIL three observations per arm was reported as a pass"; fail=1
  else
    echo "  ok   an arm under the $MIN_SAMPLES-sample floor is not a pass"
  fi

  rm -rf "$t"

  # The login identity must be an email, because that is what the server looks
  # up. Cheap to assert, and a cluster to discover: FindUserByLogin passes
  # `username` straight to GetUserByEmail, so "admin" is a 401 and
  # "admin@leoflow.local" is a token.
  case "${WP_ADMIN_LOGIN:-admin@leoflow.local}" in
    *@*) echo "  ok   the admin login is an email, which is what the server matches on" ;;
    *)   echo "  FAIL the admin login is not an email; FindUserByLogin looks up by email and this will 401"; fail=1 ;;
  esac
  # And the fatal token call must keep its stderr. Sending it to /dev/null cost
  # a run: the script died saying it could not get a token and discarded the
  # sentence that said why.
  # Anchored on the INVOCATION, not on the words "auth create-token", which also
  # appear in this very check. The first version matched its own source and
  # failed always, which is the same shape as a test that passes always: it
  # stops carrying information about the thing it names.
  if grep -A 3 '"\$WP_CLI" auth create-token' "${BASH_SOURCE[0]}" | grep -q 'password-stdin 2>/dev/null'; then
    echo "  FAIL the token call discards stderr; its failure is fatal and the reason is what you need"; fail=1
  else
    echo "  ok   the token call keeps its stderr, so a failure says why"
  fi

  # The runner must actually CALL run_experiment. It was defined and never
  # invoked, and an unconditional refusal was the last statement in the file, so
  # nothing ever reached the point of needing it. Remove the refusal and the
  # script falls off its own end and exits 0 having created nothing, which is
  # indistinguishable from a successful run.
  #
  # Checked by reading the file rather than by running it, because running it is
  # the thing that costs money.
  if grep -qE '^run_experiment "\$OUT"$' "${BASH_SOURCE[0]}"; then
    echo "  ok   the runner invokes run_experiment with its run directory"
  else
    echo "  FAIL run_experiment is not called with its output directory; --execute dies on an unbound \$1, or does nothing at all"; fail=1
  fi

  # Build and deploy must name the SAME image tag. The packer pushes
  # $WP_DAG_VERSION and deploy defaults to `git describe`, so leaving it off the
  # deploy makes the two derive the tag by different rules and the run dies on
  # "no such object" against an image that was never pushed. Checked by reading,
  # because finding out costs a cluster.
  # Both invocations span several lines, so this reads three lines forward from
  # each rather than matching one. The first version anchored on a single line
  # and failed always, which is the second time today a check of mine did that:
  # a test that cannot pass says nothing about the code, it only gets silenced.
  local compile_pinned deploy_pinned
  # `|| true` on both, and it is not noise. grep -c exits 1 when the count is
  # zero, so under set -e the assignment itself aborts the script: the check
  # died silently at exactly the moment it had something to report. Third time
  # today a check of mine could not report its own failure.
  compile_pinned="$(grep -A 3 '"\$WP_CLI" compile' "${BASH_SOURCE[0]}" | grep -c -- '--dag-version "\$WP_DAG_VERSION"' || true)"
  deploy_pinned="$(grep -A 3 '"\$WP_CLI" deploy' "${BASH_SOURCE[0]}" | grep -c -- '--dag-version "\$WP_DAG_VERSION"' || true)"
  if [ "${compile_pinned:-0}" -ge 1 ] && [ "${deploy_pinned:-0}" -ge 1 ]; then
    echo "  ok   the build and the deploy name the same image tag"
  else
    echo "  FAIL build and deploy do not both pin --dag-version (compile=$compile_pinned deploy=$deploy_pinned); they will disagree on the tag"; fail=1
  fi

  [ "$fail" = "0" ] && { echo "warm-pool-ab self-test: ok"; return 0; }
  return 1
}

# The order arms are executed in. A is measured twice, at both ends, so drift
# across the run is a measurement rather than an assumption.
RUN_ORDER="${RUN_ORDER:-A Aprime B A}"

# --------------------------------------------------------------- cluster side

apply_arm() { # <arm>
  local arm="$1" vals sets=() v
  vals="$(arm_values "$arm")" || exp_die "unknown arm: $arm"
  for v in $vals; do sets+=(--set "$v"); done
  exp_log "arm $arm: $vals"
  stack_up leoflow "$WP_JWT" "$WP_PW" "${sets[@]}"
}

run_experiment() {
  local out="$1"
  exp_require gcloud kubectl helm jq python3
  exp_require_env

  exp_arm_teardown "$HERE/teardown.sh"
  exp_provision "$HERE/provision.sh" warm-pool-ab "$NODES" "$TTL"
  gcloud container clusters get-credentials "$EXP_CLUSTER" \
    --zone "${GCP_ZONE:?}" --project "${GCP_PROJECT:?}" >/dev/null 2>&1 \
    || exp_die "could not get credentials for $EXP_CLUSTER"
  exp_kube_ready "$NODES" 600

  WP_JWT="$(openssl rand -base64 48)"
  WP_PW="$(openssl rand -base64 18)"

  # ---------------------------------------------------------------------
  # THE STEP THE PREVIOUS SCRIPT NEVER HAD.
  #
  # test/soak/warmpool-ab.sh never built or pushed the DAG images it needed, so
  # every trigger it made would have 404'd against a dag_id the control plane
  # had never been told about: an empty series, and three arms that all
  # measured nothing equally well.
  #
  # PROVEN: the build-and-push below has been run for real. From an arm64 Mac it
  # produced linux/amd64 and pushed it to Artifact Registry:
  #   us-central1-docker.pkg.dev/<project>/leoflow-validate/gcp-probe:gcpexp1
  #   sha256:ee3708486dc3ed224bb91d615f30c36ef0f81397e35b4256c90fde3d6ad22f92
  # The platform pin in test/gcp/dags/gcp_probe/dexaflow.yaml is what makes that
  # work; without it the image is arm64 and every task pod fails with an exec
  # format error AFTER the cluster has been paid for.
  #
  # That image was DELETED again after it was verified. An Artifact Registry
  # repository outlives every cluster that pulled from it, carries none of the
  # labels that make deleting a cluster safe, and `teardown.sh --leftovers` can
  # therefore only ever LIST it. A first real run rebuilds and repushes, which
  # is a few minutes and the correct default; leaving an image behind to save
  # them is how a registry becomes a bill nobody remembers agreeing to.
  #
  # NOT PROVEN: everything from the login onward. It has never run against a
  # control plane, and the defect class that has already bitten this directory
  # four times (a flag on the wrong subcommand, a label split on the wrong
  # separator, a missing release channel, an empty-array expansion under bash
  # 3.2) is exactly the class only a real run finds. Treat the code below as a
  # specification that compiles, not as a working pipeline.
  # ---------------------------------------------------------------------
  wp_build_and_push "$out" || exp_die "the DAG image could not be built or pushed; no arm can run without it"

  local api_port="${API_PORT:-18080}" token
  stack_up leoflow "$WP_JWT" "$WP_PW" $(wp_sets A)
  stack_api_forward leoflow "$api_port"
  # The login identity is an EMAIL, not a name. FindUserByLogin calls
  # GetUserByEmail with whatever `username` carries
  # (internal/storage/repository.go), and the bootstrap admin is created as
  # LEOFLOW_BOOTSTRAP_EMAIL or admin@leoflow.local
  # (cmd/dexaflow-server/main.go). This passed "admin" and got a 401 that the
  # first real run spent a cluster to discover.
  local admin_login="${WP_ADMIN_LOGIN:-admin@leoflow.local}"
  # Stderr is KEPT. It was sent to /dev/null on the one call whose failure is
  # fatal, so the run died saying "could not obtain an API token" and threw away
  # the sentence that said why. A diagnostic discarded on the fatal path is the
  # one you needed.
  local token_err
  token_err="$out/token-attempt.log"
  token="$(printf '%s' "$WP_PW" | "$WP_CLI" auth create-token \
             --server "http://127.0.0.1:$api_port" --username "$admin_login" \
             --password-stdin 2>"$token_err")" \
    || exp_die "could not obtain an API token for $admin_login: $(tr '\n' ' ' < "$token_err")"
  [ -n "$token" ] || exp_die "the token was empty; every later call would 401 and every arm would measure nothing"

  # --dag-version is passed HERE TOO, and the two must agree.
  #
  # deploy defaults it to `git describe`, so with --skip-build it recompiled
  # against a tag nobody had pushed and died on
  # "no such object: .../gcp-probe:15cb19f" while the image sitting in the
  # registry was gcp-probe:gcpexp1. Two steps deriving the same tag by different
  # rules, which works right up until the rules disagree.
  "$WP_CLI" deploy "$out/dag-project" --server "http://127.0.0.1:$api_port" \
    --token "$token" --skip-build --yes --dag-version "$WP_DAG_VERSION" \
    || exp_die "registering the DAG failed; a trigger would 404, which is exactly the shape this runner exists to fix"

  # An exploratory stop, for mapping what is left instead of discovering it one
  # paid cluster at a time.
  #
  # Nine runs found eight real defects, each hidden behind the one before it, at
  # a steady rate of one per cluster. This exists to break that rate: it holds
  # the stack up long enough to inspect a LIVE control plane with the DAG
  # registered, which is the state every remaining barrier lives in.
  #
  # BOUNDED, and the bound is not a formality. An unbounded pause is a forgotten
  # cluster, which is the single outcome this whole directory is built to
  # prevent. The node TTL is still the backstop underneath it, and the pause is
  # deliberately far shorter than the TTL so the pause expires first.
  if [ -n "${WP_PAUSE_AFTER_DEPLOY:-}" ]; then
    local pause="${WP_PAUSE_SECONDS:-1200}"
    exp_warn "PAUSING for ${pause}s with the stack up, at the operator's request."
    exp_log "  cluster:  $EXP_CLUSTER"
    exp_log "  api:      http://127.0.0.1:$api_port (port-forward is live)"
    exp_log "  token:    $out/token.txt"
    exp_log "  teardown is still armed; the node TTL is still the backstop."
    printf '%s' "$token" > "$out/token.txt"
    sleep "$pause"
    exp_log "pause over, continuing"
  fi

  # The arms, in the bracketed order, with A measured at both ends.
  local arm idx=0
  for arm in $RUN_ORDER; do
    idx=$((idx + 1))
    wp_run_arm "$arm" "$idx" "$out" "$api_port" "$token"
  done
  stack_api_forward_stop

  wp_report "$out"
}

# wp_build_and_push is the proven half. It substitutes the project id into the
# registry URL (a project id is an account identifier and is never committed)
# and shells out to the CLI's own compile, which builds for linux/amd64 and
# pushes.
wp_build_and_push() { # <out dir>
  local out="$1"
  exp_require docker
  [ -x "$WP_CLI" ] || exp_die "no leoflow CLI at $WP_CLI. Build one: go build -o $WP_CLI ./cmd/dexaflow"
  mkdir -p "$out/dag-project"
  cp "$EXP_REPO_ROOT"/test/gcp/dags/gcp_probe/* "$out/dag-project/"
  # The registry URL carries ${GCP_PROJECT} in the committed file precisely so
  # no account identifier is in git. Substituted here, into the run directory,
  # which is gitignored.
  python3 - "$out/dag-project/dexaflow.yaml" "$GCP_PROJECT" <<'SUBST'
import sys
p, proj = sys.argv[1], sys.argv[2]
src = open(p).read()
if "${GCP_PROJECT}" not in src:
    sys.exit("dexaflow.yaml no longer carries ${GCP_PROJECT}; refusing to guess the registry")
open(p, "w").write(src.replace("${GCP_PROJECT}", proj))
SUBST
  gcloud auth configure-docker "${AR_HOST:-us-central1-docker.pkg.dev}" --quiet >/dev/null 2>&1 \
    || exp_warn "could not configure docker auth for Artifact Registry; the push will probably fail"
  exp_log "building and pushing the DAG image (linux/amd64, cross-built if this is an arm64 host)"
  "$WP_CLI" compile "$out/dag-project" --output "$out/dag-project/dag.json" \
    --build --push --dag-version "$WP_DAG_VERSION" || return 1
  exp_ok "DAG image pushed; the registry repository SURVIVES the cluster and nothing here deletes it"
}

wp_sets() { # <arm> -> --set arguments
  local v
  for v in $(arm_values "$1"); do printf -- '--set %s ' "$v"; done
}

# wp_run_arm applies one arm and measures ATTEMPTS task starts under it.
#
# NEVER EXECUTED against a cluster. The series extraction it calls IS proven:
# lib/warm_deltas.py was run against a live leoflow API and produced one
# observation per attempt (see its self-test for the shapes, and #1203 for why
# this interval replaced the pod-derived one).
wp_run_arm() { # <arm> <index> <out> <api port> <token>
  local arm="$1" idx="$2" out="$3" port="$4" token="$5" i
  exp_log "arm $arm (position $idx in: $RUN_ORDER)"
  stack_up leoflow "$WP_JWT" "$WP_PW" $(wp_sets "$arm")
  stack_api_forward leoflow "$port"
  # A warm pool is empty on the first attempt of a dag_version, so the first
  # attempts of arm B measure a COLD start and would drag its p95 toward A's for
  # a reason that has nothing to do with the steady state the feature is for.
  # They are run and discarded, and the count is recorded so the discard is
  # visible rather than silent.
  for i in $(seq 1 "${WARMUP_ATTEMPTS:-3}"); do
    "$WP_CLI" runs trigger gcp_probe --server "http://127.0.0.1:$port" --token "$token" >/dev/null 2>&1 || true
  done
  printf 'arm %s warmup_discarded %s\n' "$arm" "${WARMUP_ATTEMPTS:-3}" >> "$out/facts.txt"

  # The window opens AFTER the warm-ups, so their cold starts are excluded by
  # the same queued_at filter that excludes the previous arm's attempts, rather
  # than by counting and hoping the counts line up.
  local since
  since="$(python3 -c 'import time;print(time.time())')"

  for i in $(seq 1 "$ATTEMPTS"); do
    # The measurement is NOT the wall time of the trigger call: the trigger
    # returns as soon as the run is accepted, so timing it would measure the API
    # and not the pool. It is reconstructed afterwards from the control plane's
    # own stamps (wp_arm_series).
    "$WP_CLI" runs trigger gcp_probe --server "http://127.0.0.1:$port" --token "$token" >/dev/null 2>&1 || true
    sleep "${ATTEMPT_SPACING:-2}"
  done

  # Let the last attempts actually start before asking when they started. An
  # attempt still queued has no start_date and would be DROPPED, which silently
  # biases every arm toward whatever started fastest.
  sleep "${SETTLE_SECONDS:-30}"
  wp_arm_series "$out" "$arm" "$idx" "$port" "$token" "$since" || true

  # The pods are still worth keeping as evidence, they are just not the series.
  kubectl -n "$STACK_TASK_NS" get pods -l leoflow.io/run-id -o json > "$out/pods-$arm-$idx.json" 2>/dev/null || true
  kubectl -n "$STACK_TASK_NS" delete pods -l leoflow.io/run-id --wait=false >/dev/null 2>&1 || true
}

# wp_arm_series turns one arm's window into its observation series on disk.
#
# The series is run.queued_at -> task.start_date, per attempt, extracted by
# lib/warm_deltas.py. That file carries the argument for why it is this interval
# and not the pod's; the short version is that a warm attempt gets no pod of its
# own, so a pod-derived series times different events in different arms (#1203).
wp_arm_series() { # <out> <arm> <idx> <api port> <token> <since epoch>
  local out="$1" arm="$2" idx="$3" port="$4" token="$5" since="$6"
  local base="http://127.0.0.1:$port" raw="$out/raw-$arm-$idx"
  curl -sS -m 30 -H "Authorization: Bearer $token" \
    "$base/api/v2/dags/gcp_probe/dagRuns?limit=500" > "$raw.runs.json" || {
      exp_warn "arm $arm: could not list runs; this arm has no series"; return 1; }
  : > "$raw.tis.jsonl"
  local id n=0
  for id in $(python3 "$EXP_REPO_ROOT/test/gcp/lib/warm_deltas.py" ids "$since" < "$raw.runs.json"); do
    curl -sS -m 30 -H "Authorization: Bearer $token" \
      "$base/api/v2/dags/gcp_probe/dagRuns/$id/taskInstances" >> "$raw.tis.jsonl" || continue
    printf '\n' >> "$raw.tis.jsonl"
    n=$((n + 1))
  done
  exp_log "arm $arm: $n run(s) in this arm's window"
  # Notes go to the run directory, not just to stderr: "observed 11 of 30" is the
  # kind of fact that explains a percentile six months later.
  python3 "$EXP_REPO_ROOT/test/gcp/lib/warm_deltas.py" deltas "$since" \
    "$raw.runs.json" "$raw.tis.jsonl" \
    > "$out/start-$arm-$idx.series" 2> "$out/notes-$arm-$idx.txt"
  [ -s "$out/notes-$arm-$idx.txt" ] && exp_warn "arm $arm: $(tr '\n' ' ' < "$out/notes-$arm-$idx.txt")"
  return 0
}

# wp_stat prints "<count> <p50> <p95>" for one arm's series, or nothing if the
# file is absent. Kept separate so wp_report never re-reads a series twice with
# two tools that could disagree about what is in it.
wp_stat() { # <series path>
  [ -f "$1" ] || return 1
  local n p50 p95
  n="$(count_of < "$1")"
  [ "${n:-0}" -ge 1 ] || return 1
  p50="$(p_of 50 < "$1")"
  p95="$(p_of 95 < "$1")"
  printf '%s %s %s' "$n" "$p50" "$p95"
}

# wp_report writes the comparison, or refuses.
#
# It refuses on purpose when an arm produced nothing. An empty arm is the
# expected shape of several real failures here (a warm pool that never filled, a
# deploy that 404'd, a token that expired mid-run), and every one of them would
# otherwise render as an arm that was simply very fast. The soak's `{}` is the
# precedent: a verdict built from no observations is indistinguishable from a
# passing one.
wp_report() { # <out dir>
  local out="$1" arm idx=0 usable=0 total=0 line
  : > "$out/arms.tsv"
  for arm in $RUN_ORDER; do
    idx=$((idx + 1))
    local path="$out/start-$arm-$idx.series" n p50 p95
    if line="$(wp_stat "$path")"; then
      read -r n p50 p95 <<EOF
$line
EOF
      printf '%s\t%s\t%s\t%s\t%s\n' "$arm" "$idx" "$n" "$p50" "$p95" >> "$out/arms.tsv"
      total=$((total + n))
      if series_is_usable "$n" "arm $arm (position $idx)"; then usable=$((usable + 1)); fi
    else
      printf '%s\t%s\t0\t\t\n' "$arm" "$idx" >> "$out/arms.tsv"
      exp_warn "arm $arm (position $idx) produced NO observations"
    fi
  done

  # Every arm must have a series. Three arms with one missing is not a partial
  # result, it is a different experiment.
  local arms_with_data
  arms_with_data="$(awk -F'\t' '$3 > 0' "$out/arms.tsv" | wc -l | tr -d ' ')"
  local arms_expected
  arms_expected="$(echo $RUN_ORDER | wc -w | tr -d ' ')"
  if [ "$arms_with_data" -lt "$arms_expected" ]; then
    exp_warn "REFUSING to write a verdict: $arms_with_data of $arms_expected arm positions produced observations."
    exp_warn "A comparison missing an arm is not a partial answer; the raw series are in $out."
    return 1
  fi

  # A and A are the first and last positions, by construction of RUN_ORDER.
  local a1 a2 aprime b
  a1="$(awk -F'\t' '$1=="A"{print $5}' "$out/arms.tsv" | head -1)"
  a2="$(awk -F'\t' '$1=="A"{print $5}' "$out/arms.tsv" | tail -1)"
  aprime="$(awk -F'\t' '$1=="Aprime"{print $5}' "$out/arms.tsv" | head -1)"
  b="$(awk -F'\t' '$1=="B"{print $5}' "$out/arms.tsv" | head -1)"

  local drift feature experienced prereq
  drift="$(drift_verdict "$a1" "$a2")"
  feature="$(ratio_of "$aprime" "$b")"
  experienced="$(ratio_of "$a1" "$b")"
  prereq="$(ratio_of "$a1" "$aprime")"

  {
    echo "# warm-pool A/B"
    echo
    echo "Observation: seconds from a run being accepted (\`queued_at\`) to its task"
    echo "starting (\`start_date\`). Defined identically under every arm; see"
    echo "\`lib/warm_deltas.py\` for why this and not the pod's own interval."
    echo
    echo "| arm | position | settings | n | p50 s | p95 s |"
    echo "|---|---|---|---|---|---|"
    while IFS=$'\t' read -r arm idx n p50 p95; do
      printf '| %s | %s | `%s` | %s | %s | %s |\n' "$arm" "$idx" "$(arm_values "$arm")" "$n" "${p50:--}" "${p95:--}"
    done < "$out/arms.tsv"
    echo
    echo "## Drift: $drift"
    echo
    echo "Arm A was measured first and last (p95 $a1 s then $a2 s). Tolerance"
    echo "${DRIFT_TOLERANCE}x in either direction: a cluster that sped up under the run"
    echo "flatters the last arm exactly as much as one that slowed penalizes it."
    if [ "$drift" = "UNTRUSTWORTHY" ]; then
      echo
      echo "**The cluster moved under the experiment, so the comparisons below are"
      echo "recorded but must not be quoted.** Re-run before concluding anything."
    fi
    echo
    echo "## Comparisons (ratio < 1 means faster)"
    echo
    echo "| comparison | ratio | what it isolates |"
    echo "|---|---|---|"
    echo "| B vs A' | $feature | **warm pools, one variable** |"
    echo "| B vs A | $experienced | what an operator experiences, three variables |"
    echo "| A' vs A | $prereq | the price of the coupled prerequisites |"
    echo
    echo "B vs A' is the answer to \"do warm pools help\". B vs A is the answer to"
    echo "\"is turning them on worth it\", and they differ because the chart refuses"
    echo "warm pools without \`agentTokenTransport=exchange\` and"
    echo "\`secretLivenessMode=enforce\`."
    echo
    echo "Warm-up attempts discarded per arm (a pool is empty on a dag_version's"
    echo "first attempt, which is a cold start and not the steady state the"
    echo "feature is for):"
    echo
    [ -f "$out/facts.txt" ] && sed 's/^/    /' "$out/facts.txt"
  } > "$out/report.md"

  python3 - "$out" "$drift" "$feature" "$experienced" "$prereq" "$total" <<'JSON'
import json, sys, csv
out, drift, feature, experienced, prereq, total = sys.argv[1:7]
arms = []
with open(out + "/arms.tsv") as fh:
    for row in csv.reader(fh, delimiter="\t"):
        if not row:
            continue
        arms.append({"arm": row[0], "position": int(row[1]), "n": int(row[2]),
                     "p50_s": float(row[3]) if row[3] else None,
                     "p95_s": float(row[4]) if row[4] else None})
json.dump({
    "observation": "run.queued_at -> task.start_date, seconds",
    "arms": arms,
    "drift": drift,
    "ratios": {"b_vs_aprime": feature, "b_vs_a": experienced, "aprime_vs_a": prereq},
    "total_observations": int(total),
    "trustworthy": drift == "STABLE",
}, open(out + "/verdict.json", "w"), indent=2)
JSON

  exp_ok "report written to $out/report.md (drift: $drift, B vs A': $feature)"
  [ "$drift" = "STABLE" ] || { exp_warn "drift made this run untrustworthy; the numbers are recorded, not published"; return 1; }
  [ "$usable" -ge "$arms_expected" ] || { exp_warn "at least one arm is below the $MIN_SAMPLES-sample floor; treat its percentile as inconclusive"; return 1; }
  return 0
}

# ---------------------------------------------------------------------- main

while [ $# -gt 0 ]; do
  case "$1" in
    --execute)   EXECUTE=1; shift ;;
    --nodes)     NODES="$2"; shift 2 ;;
    --ttl)       TTL="$2"; shift 2 ;;
    --attempts)  ATTEMPTS="$2"; shift 2 ;;
    --self-test) self_test; exit $? ;;
    -h|--help)   sed -n '2,/^set -euo pipefail/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) exp_die "unknown flag: $1" ;;
  esac
done

cat <<ARMS

  The three arms, and why there are three:

    A       $(arm_values A)
    A'      $(arm_values Aprime)
    B       $(arm_values B)

    B vs A'   isolates warm pools            ONE variable
    B vs A    what an operator experiences   THREE variables
    A' vs A   the price of the prerequisites TWO variables

  Turning warm pools on is not a one-flag change. The chart refuses to render
  without agentTokenTransport=exchange AND secretLivenessMode=enforce
  (helm/dexaflow/templates/deployment.yaml:199-201), and the server enforces the
  same coupling at boot. Any two-arm comparison across that bundle cannot
  attribute a difference to warm pools, which is why arm A' exists.

  Run order: $RUN_ORDER  (arm A twice, to measure drift rather than assume none)

ARMS

if [ "$EXECUTE" != "1" ]; then
  "$HERE/provision.sh" --dry-run --experiment warm-pool-ab --nodes "$NODES" --ttl "$TTL"
  exp_never_ran "warm-pool-ab, $NODES nodes for $TTL, $ATTEMPTS attempts per arm" \
    "not invoked with --execute; this printed the plan and created nothing"
  exit 0
fi

# The refusal that used to sit here is gone, and the reason it gave is why.
#
# It was an unconditional exp_never_ran + exit 1, AFTER the --execute check, so
# --execute did nothing and this runner could not be run at all. Its message said
# "the DAG build-and-push step is not implemented", and that was true when it was
# written and false by the time anyone read it: wp_build_and_push is implemented,
# and the comment above it records the digest it pushed to Artifact Registry.
#
# A guard whose stated reason has stopped being true is worse than no guard. It
# blocks the thing it names while the real limitation goes unstated, and it is
# believed, because it is specific.
#
# What the refusal was protecting against still holds and is stated where it can
# be acted on: everything from the control-plane login onward has never run, so a
# first --execute should be read as a debugging session that may also produce a
# measurement. That is in test/gcp/README.md, next to the run records, and it
# does not need to be enforced by refusing, because the cost of finding out is
# one bounded cluster with a TTL on it.

# And the call the refusal was standing in for.
#
# run_experiment was defined and never invoked: while the unconditional refusal
# was the last statement in the file, nothing ever reached the point of needing
# it, so its absence was invisible. Removing the refusal made the script fall
# off its own end and exit 0 having created nothing, which looks exactly like
# success.
#
# That is the second thing the stale guard was hiding, and the more dangerous
# one: a runner that exits 0 without running is worse than one that refuses,
# because a refusal says so.
#
# And the bare call was still not enough. run_experiment takes the run directory
# as $1 and nothing gives it a default, so under set -u it died with
# "$1: unbound variable" right after printing the arms table: the third shape in
# a row that looks like a runner deciding not to do anything.
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="$EXP_REPO_ROOT/$(exp_run_dir warm-pool-ab "$STAMP")"
mkdir -p "$OUT"
exp_log "raw series and verdict go to $OUT"
run_experiment "$OUT"
