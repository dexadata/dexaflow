#!/usr/bin/env bash
#
# Provision a throwaway GKE cluster for the three experiments a local k3d cannot
# run, and make it impossible to forget.
#
# The cost of these experiments is not the experiment. A four-hour window at the
# default shape is about USD 2; the same cluster left up for a month is about
# USD 360, and ten nodes left for a month is about USD 1,040. Both figures come
# from estimate_usd below, not from prose, so they cannot drift apart from what
# the script prints. That is two orders of magnitude, so every guardrail here is
# aimed at the second number, not the first.
#
# THE GUARDRAIL THAT MATTERS is --max-run-duration on the NODE POOL. It is a
# node-pool flag: `gcloud container clusters create` does not accept it, so the
# TTL is applied by a second call after the cluster exists, and read back before
# the script reports success. If it cannot be applied, the cluster is deleted
# immediately rather than left unbounded.
#
# What the API promises is that the nodes stop existing: NodeConfig.maxRunDuration
# is documented as "the maximum duration for the nodes to exist. If unspecified,
# the nodes can exist indefinitely." Whether GKE then REPLACES an expired node to
# hold the pool at its target size was the open question, and it is now answered
# by observation: it does not. An hour after a 30m TTL expired, the node pool
# still reported initialNodeCount 1 while its managed instance group was at
# size 0 / targetSize 0 and the project had no GCE instances at all. The MIG
# target is taken to zero rather than the node being recreated.
#
# The same observation confirms the limit of the guardrail: that cluster object
# was still RUNNING, and billing the control-plane fee, an hour after its last
# node was gone. The TTL bounds the NODE bill and nothing else, so treat it as
# the backstop it is and not as a substitute for the teardown.
# Everything else below is defense in depth on top of it.
#
# Nothing about the account is in this file. Project, zone and billing come from
# the environment or from `gcloud config`, and the script refuses to guess.
#
# Usage:
#   test/gcp/provision.sh --experiment pod-per-task [--nodes 3] [--ttl 4h]
#   test/gcp/provision.sh --experiment warm-pool-ab
#   test/gcp/provision.sh --experiment netpol          # needs Dataplane V2
#   test/gcp/provision.sh --dry-run --experiment pod-per-task   # print the plan + cost
#   test/gcp/provision.sh --self-test
set -euo pipefail

EXPERIMENT=""
NODES=3
MACHINE="e2-standard-4"
TTL="4h"
DRY_RUN=0
SPOT=0
NAME_PREFIX="leoflow-exp"

# List prices, us-central1, on-demand, USD/hour. They are ESTIMATES used to
# print a number before anything is created, not a contract: confirm against the
# pricing calculator for the region you actually use. They are here so the
# script can refuse a plan that is obviously wrong, and so the operator sees a
# figure before approving rather than after being billed.
# A case rather than an associative array: macOS ships bash 3.2, which has none,
# and a script that only runs on the maintainer's other shell is a script that
# does not run.
machine_hourly() {
  case "$1" in
    e2-standard-2) echo "0.067" ;;
    e2-standard-4) echo "0.134" ;;
    e2-standard-8) echo "0.268" ;;
    *) return 1 ;;
  esac
}
GKE_MGMT_HOURLY="0.10"
# Spot is typically 60-91% off. The low end is used so an estimate errs high.
SPOT_MULTIPLIER="0.40"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m    ok\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m    !!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; exit 2; }

# The header up to `set -euo pipefail`, rather than a hard-coded line range: a
# range silently stops printing the Usage block the first time the header grows,
# and -h is exactly the moment nobody is watching for that. The self-test checks
# the result still contains the usage lines.
usage() { sed -n '2,/^set -euo pipefail/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }

# ---------------------------------------------------------------- pure helpers

# hours_of turns 90m / 4h / 2d into a decimal number of hours. The TTL is both
# printed in the estimate and handed to --max-run-duration, so one parser keeps
# the number the operator approved and the number Google enforces identical.
hours_of() {
  local num unit
  case "$1" in
    *h) num="${1%h}"; unit=h ;;
    *m) num="${1%m}"; unit=m ;;
    *s) num="${1%s}"; unit=s ;;
    *d) num="${1%d}"; unit=d ;;
    *)  return 1 ;;
  esac
  # A positive decimal and nothing else. awk reads "abch" as 0 and "-2h" as a
  # negative, and both used to sail through: only the UPPER bound was checked, so
  # a mistyped TTL became a zero or negative --max-run-duration handed to Google
  # rather than a refusal here. An unreadable duration is not a duration.
  case "$num" in
    ''|*[!0-9.]*) return 1 ;;
  esac
  awk -v v="$num" 'BEGIN{ exit !(v + 0 > 0) }' || return 1
  case "$unit" in
    h) awk -v v="$num" 'BEGIN{printf "%.4f", v}' ;;
    m) awk -v v="$num" 'BEGIN{printf "%.4f", v/60}' ;;
    s) awk -v v="$num" 'BEGIN{printf "%.4f", v/3600}' ;;
    d) awk -v v="$num" 'BEGIN{printf "%.4f", v*24}' ;;
  esac
}

seconds_of() {
  local h; h="$(hours_of "$1")" || return 1
  awk -v h="$h" 'BEGIN{printf "%d", h*3600}'
}

# estimate_usd is deliberately a function of the inputs alone, so --dry-run and
# the real run cannot print different numbers.
estimate_usd() { # <nodes> <machine> <ttl> <spot 0|1>
  local nodes=$1 machine=$2 ttl=$3 spot=$4 rate hours mult
  rate="$(machine_hourly "$machine")" || return 1
  hours="$(hours_of "$ttl")" || return 1
  mult=1
  [ "$spot" = "1" ] && mult="$SPOT_MULTIPLIER"
  awk -v n="$nodes" -v r="$rate" -v h="$hours" -v m="$mult" -v mgmt="$GKE_MGMT_HOURLY" \
    'BEGIN{printf "%.2f", (n*r*m + mgmt) * h}'
}

# forgotten_usd is the number that actually matters: what a cluster costs if the
# teardown never happens. It is printed next to the experiment cost on purpose,
# because the ratio between them is the whole argument for the TTL.
forgotten_usd() { # <nodes> <machine> <spot 0|1>
  estimate_usd "$1" "$2" "720h" "$3"
}

# ------------------------------------------------------- the gcloud invocations

# The argv is built in a function, and never inline at the call site, so the
# self-test can read it without creating anything. That is the only way to check
# a flag before a bill does, and the first version of this script needed it: it
# passed --max-run-duration to `container clusters create`, where that flag DOES
# NOT EXIST. It is a node-pool flag (`container node-pools create|update`), so
# the create would have failed outright and the guardrail the whole directory is
# built around was never going to be applied at all.
# A RELEASE CHANNEL IS NOW MANDATORY. This create used to pass
# --no-enable-autoupgrade --no-enable-autorepair and no channel at all, which
# GKE refused outright on 2026-09-20:
#
#   code=400 ... not enrolling clusters in a release channel is now only allowed
#   for existing customers. New customers can use a release channel ...
#
# So nothing could be provisioned at all, which is a defect only a real create
# finds: the flags were individually valid and the argv self-test passed.
# `--release-channel regular` replaces BOTH old flags rather than joining them,
# because a channel enforces node auto-upgrade and GKE rejects the pair.
#
# The cost of that is real and worth stating: auto-upgrade and auto-repair are
# now ON, so GKE may recreate a node mid-experiment. Over a window of a few
# hours that is unlikely, but it is a CONFOUND for the saturation run rather
# than a neutral setting, and a node replacement there would look exactly like
# node capacity saturating. The saturation runner records node ages for that
# reason.
# seed_nodes is how many nodes the cluster is CREATED with, which is not how
# many it ends with.
#
# --max-run-duration is a node-pool flag applied after create, and applying it
# RECREATES every node in the pool, one at a time. At ten nodes that took over
# half an hour, outran gcloud's own operation wait, and left a window in which a
# ten-node cluster existed with no expiry on it at all. That window is where
# every guardrail here is weakest, because the node TTL is the only one that
# survives this script being killed.
#
# So the pool is created with ONE node, the TTL goes on while there is one node
# to recreate, and the rest are added afterwards by `clusters resize`. The nine
# that arrive later are born with the expiry already in force.
#
# VERIFIED on a real cluster before this was written, because the whole change
# rests on a claim about inheritance that the pool's own config does not prove.
# leoflow-exp-provision-probe-09201213: created with 1 node, TTL 40m applied,
# resized to 2, then both INSTANCES inspected (not the pool template):
#
#   gke-...-96hm  created 08:17:36 (before the TTL)  maxRunDuration 2400
#   gke-...-zmrw  created 08:23:05 (by the resize)   maxRunDuration 2400
#
# If that had come back empty for the second node this change would have put the
# guardrail on one node out of ten and removed it from the other nine, which is
# categorically worse than the slow roll it replaces (#1207).
seed_nodes() { [ "${NODES:-1}" -gt 1 ] && echo 1 || echo "${NODES:-1}"; }

build_create_args() { # <cluster>
  CREATE_ARGS=(container clusters create "$1"
    --project "$PROJECT" --zone "$ZONE"
    --num-nodes "$(seed_nodes)" --machine-type "$MACHINE"
    --release-channel regular
    --labels "purpose=leoflow-experiment,experiment=$EXPERIMENT,expires-after=$TTL")
  if [ "$SPOT" = "1" ]; then
    CREATE_ARGS+=(--spot)
  fi
  if [ "$EXPERIMENT" = "netpol" ]; then
    # #1089's rows are unprovable on a CNI that accepts NetworkPolicy and ignores
    # it, which is exactly what k3d does. Dataplane V2 is the whole reason this
    # experiment needs a cloud cluster at all.
    CREATE_ARGS+=(--enable-dataplane-v2)
  fi
  return 0
}

# DEFAULT_POOL is the node pool `clusters create` makes for us. The TTL is put on
# it in a second call because that is where the flag lives.
DEFAULT_POOL="default-pool"

build_ttl_args() { # <cluster>
  TTL_ARGS=(container node-pools update "$DEFAULT_POOL"
    --cluster "$1" --project "$PROJECT" --zone "$ZONE"
    --max-run-duration "${TTL_SECONDS}s" --quiet)
  return 0
}

build_ttl_check_args() { # <cluster>
  TTL_CHECK_ARGS=(container node-pools describe "$DEFAULT_POOL"
    --cluster "$1" --project "$PROJECT" --zone "$ZONE"
    --format "value(config.maxRunDuration)")
  return 0
}

# ---------------------------------------------------------------- args

while [ $# -gt 0 ]; do
  case "$1" in
    --experiment) EXPERIMENT="$2"; shift 2 ;;
    --nodes)      NODES="$2"; shift 2 ;;
    --machine)    MACHINE="$2"; shift 2 ;;
    --ttl)        TTL="$2"; shift 2 ;;
    --spot)       SPOT=1; shift ;;
    --dry-run)    DRY_RUN=1; shift ;;
    --self-test)  self_test_requested=1; shift ;;
    -h|--help)    usage; exit 0 ;;
    *) die "unknown flag: $1" ;;
  esac
done

# has_flag reports whether an argv carries a flag, matching the WHOLE word so
# --max-run-duration can never be found inside --max-run-duration-something.
has_flag() { # <flag> <argv...>
  local want="$1" a; shift
  for a in "$@"; do
    [ "$a" = "$want" ] && return 0
    case "$a" in "$want"=*) return 0 ;; esac
  done
  return 1
}

# flags_known_to_gcloud checks every flag in an argv against that subcommand's
# own --help. This is the check that was missing: a flag on the wrong subcommand
# looks exactly like a flag on the right one until the API refuses it, and the
# refusal arrives at the moment you are trying to create something.
#
# Skipped, loudly, when gcloud is not installed, because a gate that needs a
# cloud SDK on CI is a gate that quietly stops running.
flags_known_to_gcloud() { # <subcommand words as one string> <argv...>
  local sub="$1" help a flagname; shift
  if ! command -v gcloud >/dev/null 2>&1; then
    echo "  ..   skipped: no gcloud on PATH, so '$sub' flags were not checked against its help"
    return 0
  fi
  # TERM=dumb because gcloud styles its help whenever TERM names a capable
  # terminal, even into a pipe: the flag then reads ESC[1m--release-channel,
  # the "m" before it fails the word boundary below, and a documented flag is
  # reported as missing. That stopped a release cut run from a macOS Terminal.
  # shellcheck disable=SC2086 # $sub is several words (a gcloud subcommand path)
  help="$(TERM=dumb gcloud $sub --help 2>/dev/null)" || true
  if [ -z "$help" ]; then
    echo "  ..   skipped: 'gcloud $sub --help' produced nothing"
    return 0
  fi
  local bad=0
  for a in "$@"; do
    case "$a" in
      --*) flagname="${a%%=*}" ;;
      *) continue ;;
    esac
    # A here-string and not a pipe into grep -q: with `set -o pipefail`, grep -q
    # exits on the first match, printf dies of SIGPIPE, and the pipeline reports
    # 141 for a flag that IS documented. Every flag whose first mention is early
    # in the help then reads as missing.
    #
    # The pattern is bounded on both sides so --disk cannot be satisfied by
    # --disk-size, which is the same whole-token rule the teardown's label guard
    # follows.
    if grep -qE -- "(^|[^A-Za-z0-9-])${flagname}([^A-Za-z0-9-]|$)" <<<"$help"; then
      continue
    fi
    echo "  FAIL gcloud $sub does not accept $flagname (checked against $(gcloud version 2>/dev/null | head -1))"
    bad=1
  done
  [ "$bad" = "0" ] && { echo "  ok   every flag passed to 'gcloud $sub' exists in its help"; return 0; }
  return 1
}

argv_self_test() {
  local PROJECT=stub-project ZONE=stub-zone TTL_SECONDS=14400
  build_create_args "leoflow-exp-netpol-01011200"
  build_ttl_args "leoflow-exp-netpol-01011200"
  build_ttl_check_args "leoflow-exp-netpol-01011200"

  # The defect this whole section exists for. --max-run-duration belongs to
  # `container node-pools`, and `container clusters create` rejects it, so a
  # create carrying it fails and nothing is ever provisioned with a TTL.
  if has_flag --max-run-duration "${CREATE_ARGS[@]}"; then
    echo "  FAIL the cluster create carries --max-run-duration, which that command does not accept"
    fail=1
  else
    echo "  ok   the cluster create does not carry --max-run-duration (a node-pool flag)"
  fi
  if has_flag --max-run-duration "${TTL_ARGS[@]}"; then
    echo "  ok   the node-pool update carries the TTL"
  else
    echo "  FAIL nothing applies the node TTL, which is the one guardrail Google enforces for us"
    fail=1
  fi
  case " ${TTL_ARGS[*]} " in
    *" 14400s "*) echo "  ok   the TTL handed to Google is the one the estimate was printed for" ;;
    *) echo "  FAIL the node-pool update does not carry the computed TTL: ${TTL_ARGS[*]}"; fail=1 ;;
  esac
  # The claim in README.md is that a fixed pool cannot grow into a bill. That is
  # true only while nothing turns autoscaling on.
  if has_flag --enable-autoscaling "${CREATE_ARGS[@]}"; then
    echo "  FAIL the create enables autoscaling, so the pool can grow into a bill"
    fail=1
  else
    echo "  ok   autoscaling is never enabled, so the node count is the one that was priced"
  fi

  # The defect a real create found on 2026-09-20: GKE refuses a cluster with no
  # release channel for new customers, so an argv without one provisions
  # NOTHING. flags_known_to_gcloud cannot catch it, because every flag in the
  # old argv existed; what was wrong was a flag that was ABSENT.
  if has_flag --release-channel "${CREATE_ARGS[@]}"; then
    echo "  ok   the create names a release channel, which GKE now requires"
  else
    echo "  FAIL the create names no release channel; GKE refuses that with a 400 and nothing is provisioned"
    fail=1
  fi
  # A channel enforces node auto-upgrade, so GKE rejects the create if the old
  # opt-out flags are still present alongside it.
  if has_flag --no-enable-autoupgrade "${CREATE_ARGS[@]}"; then
    echo "  FAIL the create combines --no-enable-autoupgrade with a release channel, which GKE rejects"
    fail=1
  else
    echo "  ok   the create does not fight the channel over auto-upgrade"
  fi

  flags_known_to_gcloud "container clusters create" "${CREATE_ARGS[@]}" || fail=1
  flags_known_to_gcloud "container node-pools update" "${TTL_ARGS[@]}" || fail=1
  flags_known_to_gcloud "container node-pools describe" "${TTL_CHECK_ARGS[@]}" || fail=1
}

self_test() {
  local fail=0
  local _eq; _eq() { [ "$1" = "$2" ] && { echo "  ok   $3"; return 0; }; echo "  FAIL $3: '$1' != '$2'"; fail=1; }

  _eq "$(hours_of 4h)"   "4.0000"  "4h parses"
  _eq "$(hours_of 90m)"  "1.5000"  "90m parses"
  _eq "$(hours_of 2d)"   "48.0000" "2d parses"
  _eq "$(seconds_of 4h)" "14400"   "4h in seconds, which is what Google is given"
  hours_of 4 >/dev/null 2>&1 && { echo "  FAIL a unitless TTL is accepted"; fail=1; } || echo "  ok   a unitless TTL is refused rather than guessed"

  # 3 x 0.134 + 0.10 = 0.502/h, over 4h = 2.008
  _eq "$(estimate_usd 3 e2-standard-4 4h 0)" "2.01" "the estimate is the arithmetic, not a vibe"
  _eq "$(estimate_usd 0 e2-standard-4 4h 0)" "0.40" "zero nodes still bills the control plane"
  estimate_usd 3 e2-nonexistent 4h 0 >/dev/null 2>&1 && { echo "  FAIL an unknown machine type is priced"; fail=1; } || echo "  ok   an unknown machine type refuses to be priced"

  # The ratio the TTL exists for: a month of the same cluster.
  four_h="$(estimate_usd 3 e2-standard-4 4h 0)"
  month="$(forgotten_usd 3 e2-standard-4 0)"
  awk -v a="$four_h" -v b="$month" 'BEGIN{exit !(b > a*100)}' \
    && echo "  ok   a forgotten cluster is 100x the experiment, which is why the TTL is not optional" \
    || { echo "  FAIL the forgotten-cluster figure is not dominating; the estimate is wrong"; fail=1; }

  _eq "$(estimate_usd 3 e2-standard-4 4h 1)" "1.04" "spot discounts the nodes and not the control plane, which is most of a short window"

  hours_of -2h >/dev/null 2>&1 && { echo "  FAIL a negative TTL is accepted"; fail=1; } || echo "  ok   a negative TTL is refused rather than sent to Google"
  hours_of 0h >/dev/null 2>&1 && { echo "  FAIL a zero TTL is accepted"; fail=1; } || echo "  ok   a zero TTL is refused"
  hours_of abch >/dev/null 2>&1 && { echo "  FAIL a non-numeric TTL is accepted as 0"; fail=1; } || echo "  ok   a non-numeric TTL is refused rather than silently read as zero"

  argv_self_test

  # gcloud styles its help when TERM names a capable terminal, even into a
  # pipe, and a styled flag fails the word boundary. A fake gcloud that does
  # the same proves the check reads the plain help whatever TERM the caller
  # has; without TERM=dumb this reports --release-channel as missing.
  local fake; fake="$(mktemp -d)"
  cat >"$fake/gcloud" <<'FAKE'
#!/usr/bin/env bash
[ "$1" = version ] && { echo "Google Cloud SDK 0.0.0 (fake)"; exit 0; }
if [ "${TERM:-dumb}" = dumb ]; then echo "  --release-channel=CHANNEL"; else printf '  \033[1m--release-channel\033[m=CHANNEL\n'; fi
FAKE
  chmod +x "$fake/gcloud"
  PATH="$fake:$PATH" TERM=xterm-256color flags_known_to_gcloud "container clusters create" --release-channel regular >/dev/null \
    && echo "  ok   a styled help (TERM=xterm-256color) still documents --release-channel" \
    || { echo "  FAIL the flag check read styled help and missed a documented flag"; fail=1; }
  rm -rf "$fake"

  # seed_nodes decides how many nodes exist while the pool still has no expiry,
  # so it sizes the only window a killed script cannot protect. Cases rather
  # than an eyeball, because the two obvious wrong spellings are both silent:
  # seeding with $NODES restores the ten-node roll this change exists to remove,
  # and seeding with 1 unconditionally turns a one-node run into a resize to
  # itself.
  local saved_nodes="$NODES"
  NODES=10; [ "$(seed_nodes)" = "1" ] \
    && echo "  ok   a ten-node run is created with one node, so the TTL rolls one node" \
    || { echo "  FAIL a ten-node run does not seed with one node"; fail=1; }
  NODES=1; [ "$(seed_nodes)" = "1" ] \
    && echo "  ok   a one-node run seeds with one and needs no resize" \
    || { echo "  FAIL a one-node run does not seed with one"; fail=1; }
  NODES=2; [ "$(seed_nodes)" = "1" ] \
    && echo "  ok   two nodes still seeds with one" \
    || { echo "  FAIL two nodes does not seed with one"; fail=1; }
  NODES=3; [ "$(seed_nodes)" != "3" ] \
    && echo "  ok   the seed is never the target, so the TTL is never rolled across a full pool" \
    || { echo "  FAIL the seed equals the target; the slow roll is back"; fail=1; }
  NODES="$saved_nodes"

  case "$(usage)" in
    *"--experiment pod-per-task"*"--self-test"*) echo "  ok   -h still prints the usage block" ;;
    *) echo "  FAIL -h no longer prints the usage block; the header extraction has drifted"; fail=1 ;;
  esac

  [ "$fail" = "0" ] && { echo "provision self-test: ok"; return 0; }
  return 1
}

[ "${self_test_requested:-0}" = "1" ] && { self_test; exit $?; }

# ---------------------------------------------------------------- validation

case "$EXPERIMENT" in
  pod-per-task|warm-pool-ab|netpol) ;;
  # provision-probe is not an experiment about leoflow. It is for questions
  # about THIS directory's own mechanism, which can only be answered on a real
  # cluster: whether resize preserves the node TTL (#1207), whether a flag does
  # what its help says, whether an expired node is replaced. Four of this
  # directory's defects were found only by paying for a cluster, so having a
  # sanctioned, labelled, TTL'd way to ask a small one is cheaper than the
  # alternative, which is someone running gcloud by hand with none of the
  # guardrails.
  provision-probe) ;;
  "") die "--experiment is required (pod-per-task | warm-pool-ab | netpol | provision-probe)" ;;
  *)  die "unknown experiment: $EXPERIMENT" ;;
esac

# A ceiling on the ceiling. The maintainer authorized up to ten nodes; this
# refuses eleven rather than trusting a typo, because the failure mode of a
# fat-fingered --nodes is a bill, not an error.
[ "$NODES" -ge 1 ] 2>/dev/null || die "--nodes must be a positive integer"
[ "$NODES" -le 10 ] || die "--nodes $NODES exceeds the authorized ceiling of 10. Raising it is a conversation, not a flag."

TTL_SECONDS="$(seconds_of "$TTL")" || die "--ttl must carry a unit (90m, 4h, 2d), so it cannot be misread"
[ "$TTL_SECONDS" -le 86400 ] || die "--ttl $TTL exceeds 24h. These are bounded experiments; a longer one is a decision to take deliberately."
machine_hourly "$MACHINE" >/dev/null 2>&1 || die "no price known for $MACHINE, so the estimate would be a guess. Add it to machine_hourly() or pick a priced type."

PROJECT="${GCP_PROJECT:-$(gcloud config get-value project 2>/dev/null || true)}"
ZONE="${GCP_ZONE:-$(gcloud config get-value compute/zone 2>/dev/null || true)}"
[ -n "$PROJECT" ] && [ "$PROJECT" != "(unset)" ] || die "set GCP_PROJECT or 'gcloud config set project'. This script never guesses which account it is spending."
[ -n "$ZONE" ] && [ "$ZONE" != "(unset)" ] || die "set GCP_ZONE or 'gcloud config set compute/zone'. Zonal on purpose: a regional control plane multiplies the nodes across zones."

CLUSTER="${NAME_PREFIX}-${EXPERIMENT}-$(date +%m%d%H%M)"
COST="$(estimate_usd "$NODES" "$MACHINE" "$TTL" "$SPOT")"
FORGOTTEN="$(forgotten_usd "$NODES" "$MACHINE" "$SPOT")"

cat <<PLAN

  experiment     $EXPERIMENT
  cluster        $CLUSTER  (zonal, $ZONE)
  nodes          $NODES x $MACHINE$([ "$SPOT" = "1" ] && echo "  (spot)")
  node TTL       $TTL, put on the node pool after create and read back
  autoscaling    off; a fixed pool cannot grow into a bill

  estimated cost of this window     ~USD $COST
  the same cluster left for a month ~USD $FORGOTTEN

  Prices are us-central1 list estimates, not a quote. The second number is why
  the node TTL exists: if every other safeguard fails, the nodes still expire.

  The estimate covers nodes and the cluster fee, and nothing else. It does NOT
  include boot disks (GKE gives each node 100 GB of pd-balanced by default, about
  USD 0.014 an hour per node), egress, load balancers, or any persistent disk an
  experiment leaves behind. Deleting the cluster does not delete a PD that
  outlived its PVC, and leoflow's staging volumes are exactly that shape.
  Run test/gcp/teardown.sh --leftovers after a run.

PLAN

if [ "$SPOT" = "1" ]; then
  warn "Spot nodes can be preempted, and a preemption looks exactly like the infra failure these experiments measure."
  warn "For a resilience run that is a confound, not a saving. Drop --spot unless you are only smoke-testing the scripts."
fi

[ "$DRY_RUN" = "1" ] && { log "dry run: nothing created"; exit 0; }

# ---------------------------------------------------------------- create

if [ "$EXPERIMENT" = "netpol" ]; then
  log "netpol: Dataplane V2 on, because a CNI that does not enforce makes the assertion pass vacuously"
fi

build_create_args "$CLUSTER"
build_ttl_args "$CLUSTER"
build_ttl_check_args "$CLUSTER"

# wait_for_pool_operation waits out the node-pool operation gcloud stopped
# waiting for, and reports whether it ended OK.
#
# gcloud's non-zero exit on `node-pools update` conflates "the update failed"
# with "I stopped waiting". They need opposite responses: the first means delete
# the cluster, the second means wait. Distinguishing them is the difference
# between a clean run and a ten-node cluster left billing because the script
# believed its own error message.
#
# The read-back after this is still what decides. This only buys the time for
# there to be something to read.
wait_for_pool_operation() { # <cluster> -> 0 if the operation ended DONE
  local c="$1" op status
  op="$(gcloud container operations list --project "$PROJECT" --zone "$ZONE" \
          --filter="targetLink~$c AND status=RUNNING" --format='value(name)' 2>/dev/null | head -1)"
  if [ -z "$op" ]; then
    # Nothing running. gcloud's failure was therefore a real one, not a wait.
    return 1
  fi
  log "operation $op is still running; waiting for it rather than assuming it failed"
  gcloud container operations wait "$op" --project "$PROJECT" --zone "$ZONE" >/dev/null 2>&1 || true
  status="$(gcloud container operations describe "$op" --project "$PROJECT" --zone "$ZONE" \
              --format='value(status)' 2>/dev/null)"
  [ "$status" = "DONE" ]
}

log "creating $CLUSTER in $ZONE (project $PROJECT)"
gcloud "${CREATE_ARGS[@]}" || die "cluster create failed; nothing to tear down"

# Recorded before anything else can go wrong, so the teardown can find it by
# name even if the next step is what fails.
printf '%s\n' "$CLUSTER" > .gcp-experiment-cluster

# The TTL is a SECOND call because --max-run-duration is a node-pool flag;
# `clusters create` rejects it outright. A cluster whose nodes have no expiry is
# the thing this directory exists to prevent, so if the TTL cannot be applied and
# read back, the cluster goes away now rather than becoming a bill nobody is
# watching.
log "applying the $TTL node TTL to $DEFAULT_POOL (nodes may be recreated to pick it up)"
if ! gcloud "${TTL_ARGS[@]}"; then
  # A non-zero exit here is not the same as a failure, and treating it as one
  # cost a ten-node cluster. Applying maxRunDuration RECREATES every node, one
  # at a time; on ten nodes that outran gcloud's own operation wait, which
  # returned non-zero with "is still running" at 9 of 10 nodes and NODES_FAILED
  # zero. Nothing had failed. The script concluded the TTL could not be applied,
  # tried to delete, and the delete bounced off the very operation that was
  # still succeeding.
  #
  # So: find the operation and wait for IT, rather than for gcloud's patience.
  # Only if that also fails is the cluster unbounded and worth deleting.
  warn "the TTL call returned non-zero; checking whether its operation is merely still running"
  if wait_for_pool_operation "$CLUSTER"; then
    ok "the node-pool operation completed; the TTL is read back below, which is what decides"
  else
    warn "the node TTL could not be applied; deleting the cluster rather than leaving it unbounded"
    "$(dirname "$0")/teardown.sh" "$CLUSTER" || die "DELETE $CLUSTER BY HAND NOW: gcloud container clusters delete $CLUSTER --zone $ZONE"
    die "node TTL not applied; the cluster was deleted"
  fi
fi

APPLIED_TTL="$(gcloud "${TTL_CHECK_ARGS[@]}" 2>/dev/null || true)"
if [ -z "$APPLIED_TTL" ]; then
  warn "the node pool reports no maxRunDuration, so nothing would expire on its own"
  "$(dirname "$0")/teardown.sh" "$CLUSTER" || die "DELETE $CLUSTER BY HAND NOW: gcloud container clusters delete $CLUSTER --zone $ZONE"
  die "node TTL not readable back; the cluster was deleted"
fi

ok "the node pool reports maxRunDuration=$APPLIED_TTL"

# The rest of the nodes, added only now that the expiry is in force. A node
# created here is born with it (see seed_nodes for the evidence), so the window
# in which an unbounded node exists is the seed node's recreation and nothing
# more.
if [ "$(seed_nodes)" != "$NODES" ]; then
  log "scaling $DEFAULT_POOL from $(seed_nodes) to $NODES, with the TTL already in force"
  if ! gcloud container clusters resize "$CLUSTER" --project "$PROJECT" --zone "$ZONE" \
         --num-nodes "$NODES" --quiet; then
    warn "the resize failed; deleting rather than leaving a cluster nobody asked for"
    "$(dirname "$0")/teardown.sh" "$CLUSTER" || die "DELETE $CLUSTER BY HAND NOW: gcloud container clusters delete $CLUSTER --zone $ZONE"
    die "could not scale to $NODES nodes; the cluster was deleted"
  fi

  # Verify the INSTANCES, not the pool's template. The pool saying
  # maxRunDuration and a running instance actually carrying an expiry are two
  # different claims, and this directory has been bitten four times by exactly
  # that gap between what a command accepts and what it does. If a node came up
  # without the expiry, the guardrail is missing on that node and the cluster
  # goes now.
  unbounded="$(gcloud compute instances list --project "$PROJECT" \
      --filter="name~gke-$CLUSTER AND zone:($ZONE)" \
      --format='value(name,scheduling.maxRunDuration.seconds)' 2>/dev/null \
    | awk -F'\t' '$2 == "" { print $1 }')"
  if [ -n "$unbounded" ]; then
    warn "these nodes carry NO expiry after the scale-up, so the TTL does not cover them:"
    printf '    %s\n' $unbounded >&2
    "$(dirname "$0")/teardown.sh" "$CLUSTER" || die "DELETE $CLUSTER BY HAND NOW: gcloud container clusters delete $CLUSTER --zone $ZONE"
    die "nodes without an expiry after scale-up; the cluster was deleted"
  fi
  ok "$NODES nodes up, every one of them carrying the expiry"
fi
cat <<NEXT

  Delete it the moment you are done, do not wait for the TTL:

      test/gcp/teardown.sh $CLUSTER

  The node TTL removes the expensive part. The control plane keeps billing at
  about USD $GKE_MGMT_HOURLY an hour until the CLUSTER is deleted, so the TTL is
  the safety net and the teardown is still the job.

NEXT
