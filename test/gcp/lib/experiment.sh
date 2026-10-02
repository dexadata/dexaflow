#!/usr/bin/env bash
#
# The harness the three runners share: a run directory, a cluster, and the one
# invariant that matters more than any result they produce.
#
# THE INVARIANT: a runner that exits without tearing down is a defect, and it is
# a defect on EVERY exit path, not just the happy one. A cluster survives a
# crash, a Ctrl-C, a failed assertion, a `set -e` trip in a helper, and an
# unbound variable, so the teardown is installed as a trap on all of them BEFORE
# the cluster exists, and the trap is the only thing that deletes. No runner
# calls teardown.sh itself: a script with two teardown paths has one that is
# never tested.
#
# The node TTL that provision.sh puts on the pool is the backstop under all of
# this. The trap is what keeps a six-hour bill from a six-minute experiment; the
# TTL is what keeps a forever bill from a laptop that slept. They are not
# alternatives and this file relies on both.
#
# Sourced by the runners, never executed, except for --self-test.

# ------------------------------------------------------------------- output

exp_log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
exp_ok()   { printf '\033[1;32m    ok\033[0m %s\n' "$*"; }
exp_warn() { printf '\033[1;33m    !!\033[0m %s\n' "$*" >&2; }
exp_die()  { printf '\033[1;31mFATAL:\033[0m %s\n' "$*" >&2; exit 2; }

# exp_never_ran prints the banner a runner shows when it has been built but not
# executed. warmpool-ab.sh already does this and it is the right shape: a script
# that has never touched a cluster must say so in its own voice, because the
# alternative is a reader assuming the numbers in the repository came from
# somewhere.
exp_never_ran() { # <what> <why>
  cat >&2 <<BANNER

  ┌──────────────────────────────────────────────────────────────────────┐
  │  NEVER EXECUTED                                                      │
  └──────────────────────────────────────────────────────────────────────┘

  $1

  This runner has never been run against a real cluster. Reason on record:

    $2

  Nothing below this line is a measurement. The code exists, its pure parts
  are self-tested, and its cluster-facing parts are UNPROVEN: the defect class
  that already bit this directory three times (a flag on the wrong subcommand,
  a label split on the wrong separator, a cluster refused for want of a release
  channel) is exactly the class that only a real run finds.

  Re-run with --execute to actually provision and run it, and replace this
  banner with what happened.

BANNER
}

# ---------------------------------------------------------------- run dir

# exp_run_dir names a directory for one run's raw series and verdict. The name
# carries the experiment and a timestamp, so two runs never overwrite each
# other and a conclusion can always be traced back to the series it came from.
#
# Under test/gcp/runs/, which is gitignored: these hold timings from a real
# billing cluster on one machine at one moment, and they belong to that run
# rather than to the repository.
exp_run_dir() { # <experiment> <stamp>
  printf 'test/gcp/runs/%s-%s' "$1" "$2"
}

# ------------------------------------------------------------ the teardown

# These are globals on purpose: the trap runs in a context where nothing can be
# passed to it, so what to delete has to be reachable from there.
EXP_CLUSTER=""
EXP_TEARDOWN_DONE=0
EXP_TEARDOWN_SH=""
EXP_TRAP_ARMED_AT=""

# exp_teardown_decision is the whole "what should the trap do" question, pulled
# out as a pure function so every branch is tested without an account.
#
# It is deliberately NOT "delete if the run failed". The bill does not care
# whether the experiment passed.
exp_teardown_decision() { # <cluster name or empty> <already done 0|1> [recorded name]
  if [ "$2" = "1" ]; then echo already-done; return 0; fi
  # A cluster provision.sh created but did not hand back is still a cluster, and
  # it is still billing. provision.sh writes .gcp-experiment-cluster the moment
  # the create returns, BEFORE the TTL step that can fail, precisely so the name
  # survives a failure there. Nothing read it.
  #
  # The cost of not reading it was not a missed cleanup, it was a lie. A run
  # whose TTL step failed printed "the cluster STILL EXISTS, it is billing right
  # now", and then this function said "no cluster was created, so there is
  # nothing to tear down" and that was the LAST line on the screen: reassuring,
  # final, and wrong.
  if [ -z "$1" ] && [ -n "${3:-}" ]; then echo delete-recorded; return 0; fi
  if [ -z "$1" ]; then echo nothing-to-delete; return 0; fi
  echo delete
}

# exp_recorded_cluster reads the cluster name provision.sh leaves behind, but
# ONLY if this run is the one that left it.
#
# The fallback exists because a cluster provision.sh created and did not hand
# back is still billing (#1206). The hazard it introduced is the mirror image:
# the marker file outlives the run that wrote it, so a LATER runner that dies
# before provisioning would read a stale name and tear down a cluster that is
# not its own, possibly one somebody else is using right now.
#
# That is not hypothetical. Running the self-test from the repository root while
# a warm-pool experiment was in flight made the "a run that never provisioned
# deletes nothing" case fail, because the trap found the live experiment's name
# on disk. The stub teardown meant no harm was done; a real runner in the same
# shape would have issued a real delete.
#
# So the marker is only trusted when it is NEWER than the moment this run armed
# its trap. A name written before we started belongs to someone else.
#
# EXP_TRAP_ARMED_AT is set by exp_arm_teardown. Without it there is no run to
# compare against and the marker is not trusted at all, which is the safe
# direction: the cost of ignoring a real marker is a cluster that must be
# deleted by hand, and its TTL still expires the nodes. The cost of trusting a
# stale one is deleting someone else's work.
exp_recorded_cluster() {
  local marker=".gcp-experiment-cluster"
  [ -f "$marker" ] || return 0
  [ -n "${EXP_TRAP_ARMED_AT:-}" ] || return 0
  local written
  written="$(date -r "$marker" +%s 2>/dev/null || stat -c %Y "$marker" 2>/dev/null || echo 0)"
  [ "${written:-0}" -ge "${EXP_TRAP_ARMED_AT}" ] || return 0
  cat "$marker" 2>/dev/null || true
}

# exp_trap is installed on EXIT, INT, TERM and ERR. It preserves the original
# exit status: a teardown must never turn a failed experiment green, and it must
# never turn a passing one red just by running.
exp_trap() {
  local rc=$?
  local recorded=""
  recorded="$(exp_recorded_cluster)"
  case "$(exp_teardown_decision "$EXP_CLUSTER" "$EXP_TEARDOWN_DONE" "$recorded")" in
    already-done)     exit "$rc" ;;
    delete-recorded)
      exp_warn "the runner never received a cluster name, but $recorded was recorded as created; tearing THAT down"
      EXP_CLUSTER="$recorded" ;;
    nothing-to-delete)
      # Reached when the runner died before a cluster existed. Saying so is
      # worth a line: silence here is indistinguishable from a trap that did
      # not fire, and the whole point is being able to tell those apart.
      exp_log "no cluster was created, so there is nothing to tear down"
      exit "$rc" ;;
  esac
  EXP_TEARDOWN_DONE=1
  echo
  exp_log "tearing down $EXP_CLUSTER (exit status $rc)"
  if "$EXP_TEARDOWN_SH" "$EXP_CLUSTER"; then
    exp_ok "$EXP_CLUSTER is gone, verified by the teardown"
  else
    # The loudest thing this file can say. A teardown that failed and was
    # reported quietly is the exact failure the budget cannot absorb.
    cat >&2 <<LEFTOVER

  ┌──────────────────────────────────────────────────────────────────────┐
  │  THE CLUSTER MAY STILL BE RUNNING AND BILLING                        │
  └──────────────────────────────────────────────────────────────────────┘

      gcloud container clusters delete $EXP_CLUSTER --zone \${GCP_ZONE:-?} --quiet
      test/gcp/teardown.sh --list
      test/gcp/teardown.sh --leftovers

  Do this now. The node TTL will expire the NODES on its own, but the control
  plane bills until the CLUSTER object is deleted.

LEFTOVER
    # A failed teardown is a failure of the run, whatever the experiment said.
    [ "$rc" = "0" ] && rc=3
  fi
  exit "$rc"
}

# exp_arm_teardown installs the trap. Called BEFORE provisioning, so the window
# in which a cluster exists and no trap covers it is empty.
exp_arm_teardown() { # <path to teardown.sh>
  EXP_TEARDOWN_SH="$1"
  [ -x "$EXP_TEARDOWN_SH" ] || exp_die "teardown.sh not executable at $EXP_TEARDOWN_SH; refusing to create anything that nothing can delete"
  # The instant this run started caring. A cluster marker older than this was
  # written by somebody else, and exp_recorded_cluster will not touch it.
  EXP_TRAP_ARMED_AT="$(date +%s)"
  trap exp_trap EXIT INT TERM
}

# exp_adopt_cluster tells the armed trap which cluster to delete. Separate from
# provisioning so the name is recorded the instant it is known.
exp_adopt_cluster() { # <cluster>
  [ -n "${EXP_TEARDOWN_SH:-}" ] || exp_die "exp_adopt_cluster called before exp_arm_teardown; the cluster would not be covered by the trap"
  EXP_CLUSTER="$1"
  exp_ok "teardown armed for $EXP_CLUSTER"
}

# ------------------------------------------------------------- provisioning

# exp_provision runs provision.sh and reads the cluster name back out of the
# file it writes. It does NOT create clusters by any other route: the budget
# guardrails, the node TTL and the cost plan all live in that script, and a
# second path to creating a cluster is a second path with none of them.
exp_provision() { # <provision.sh> <experiment> <nodes> <ttl> [extra args...]
  local sh="$1" experiment="$2" nodes="$3" ttl="$4"; shift 4
  local marker="${EXP_REPO_ROOT:-.}/.gcp-experiment-cluster"
  rm -f "$marker"
  exp_log "provisioning via $sh (the only path that prices and TTLs a cluster)"
  "$sh" --experiment "$experiment" --nodes "$nodes" --ttl "$ttl" "$@" \
    || exp_die "provision.sh failed; nothing was created"
  [ -f "$marker" ] || exp_die "provision.sh reported success but wrote no $marker, so the trap has no name to delete. Run test/gcp/teardown.sh --list NOW."
  local name; name="$(cat "$marker")"
  # A name that gcloud would read as a flag is not a name. This is a real shape:
  # a stale marker in another worktree currently holds "--09191945", and
  # `gcloud container clusters describe --09191945` is an unknown-flag error,
  # which teardown reads as "the cluster does not exist" and reports as gone.
  exp_valid_cluster_name "$name" || exp_die "provision.sh wrote an unusable cluster name: '$name'. Run test/gcp/teardown.sh --list and delete by hand."
  exp_adopt_cluster "$name"
}

# exp_valid_cluster_name is the guard for the above. GKE names are lowercase
# alphanumeric and hyphens, starting with a letter: anything starting with a
# hyphen is an argument to gcloud, not a value.
exp_valid_cluster_name() { # <name>
  case "$1" in
    ""|-*) return 1 ;;
  esac
  case "$1" in
    *[!a-z0-9-]*) return 1 ;;
  esac
  return 0
}

# ------------------------------------------------------------- preflight

# exp_require refuses to start a run that cannot finish. Every one of these is a
# thing that, missing, produces a half-run experiment on a cluster that is
# already billing.
exp_require() { # <binary...>
  local b missing=""
  for b in "$@"; do
    command -v "$b" >/dev/null 2>&1 || missing="$missing $b"
  done
  [ -z "$missing" ] || exp_die "missing required tool(s):$missing. Install them BEFORE provisioning: a missing binary found after the cluster is up is billed time."
}

# exp_require_env resolves the two account settings every runner needs, by the
# SAME rule provision.sh uses, and refuses in the preflight if neither source
# has them.
#
# It exists because the two disagreed. provision.sh falls back to
# `gcloud config get-value project` when GCP_PROJECT is unset, and documents
# that; the runners wrote ${GCP_PROJECT:?}, which does not. So a run with only
# GCP_ZONE set provisioned a ten-node cluster perfectly, applied the TTL, read
# it back, armed the teardown, and then died on the very next line with
# "GCP_PROJECT: parameter null or not set".
#
# Fifty minutes and about two dollars to discover a variable was unset. The
# check costs a second and now happens next to exp_require, whose own comment
# already said the principle: a missing prerequisite found after the cluster is
# up is billed time.
#
# It EXPORTS, so everything downstream sees the resolved values rather than each
# caller re-deriving them and finding a third answer.
exp_require_env() {
  : "${GCP_PROJECT:=$(gcloud config get-value project 2>/dev/null || true)}"
  : "${GCP_ZONE:=$(gcloud config get-value compute/zone 2>/dev/null || true)}"
  case "${GCP_PROJECT:-}" in
    ""|"(unset)") exp_die "set GCP_PROJECT or 'gcloud config set project'. Checked BEFORE provisioning: this used to surface after a cluster was already billing." ;;
  esac
  case "${GCP_ZONE:-}" in
    ""|"(unset)") exp_die "set GCP_ZONE or 'gcloud config set compute/zone'. Checked BEFORE provisioning." ;;
  esac
  export GCP_PROJECT GCP_ZONE
}

# exp_kube_ready waits for the cluster's API to answer and for every node to be
# Ready. Nothing else in a runner is meaningful until this passes, and a runner
# that starts measuring against a half-ready node pool measures the node pool.
exp_kube_ready() { # <expected node count> <timeout seconds>
  local want="$1" timeout="$2" deadline ready
  deadline=$(( $(date +%s) + timeout ))
  exp_log "waiting for $want node(s) to be Ready (timeout ${timeout}s)"
  while [ "$(date +%s)" -lt "$deadline" ]; do
    ready="$(kubectl get nodes --no-headers 2>/dev/null | awk '$2=="Ready"' | wc -l | tr -d ' ')"
    if [ "${ready:-0}" -ge "$want" ]; then
      exp_ok "$ready node(s) Ready"
      return 0
    fi
    sleep 5
  done
  exp_die "only ${ready:-0} of $want node(s) Ready after ${timeout}s; refusing to measure a cluster that is not up"
}

# ---------------------------------------------------------------- self-test

self_test() {
  local fail=0
  _eq() { [ "$1" = "$2" ] && { echo "  ok   $3"; return 0; }; echo "  FAIL $3: '$1' != '$2'"; fail=1; }

  # ------------------------------------------- the teardown decision
  # The branch that matters: a cluster exists and the run is over, for any
  # reason whatsoever.
  _eq "$(exp_teardown_decision leoflow-exp-netpol-01011200 0)" "delete" "a cluster that exists is deleted"
  _eq "$(exp_teardown_decision "" 0)" "nothing-to-delete" "no cluster means nothing to delete, and it is said out loud"
  _eq "$(exp_teardown_decision leoflow-exp-netpol-01011200 1)" "already-done" "a second trap firing does not delete twice"
  _eq "$(exp_teardown_decision "" 1)" "already-done" "already-done wins over an empty name"
  # The case a real run hit. provision.sh created a ten-node cluster, failed at
  # the TTL step, and exited without handing the name back. The trap said "no
  # cluster was created" as the last line on the screen while the cluster was
  # billing. The recorded name is the third argument precisely so that cannot
  # happen again.
  _eq "$(exp_teardown_decision "" 0 leoflow-exp-pod-per-task-09201112)" "delete-recorded" \
      "a cluster the runner never received, but that provision.sh recorded, is still torn down"
  _eq "$(exp_teardown_decision "" 0 "")" "nothing-to-delete" \
      "an empty recorded name is still nothing to delete"
  _eq "$(exp_teardown_decision leoflow-exp-a 0 leoflow-exp-b)" "delete" \
      "a name the runner DOES have wins over the recorded one"
  _eq "$(exp_teardown_decision "" 1 leoflow-exp-pod-per-task-09201112)" "already-done" \
      "already-done still wins, so a recorded name cannot cause a second delete"

  # exp_require_env, driven with a stub gcloud so no account is touched. The
  # case that matters is the one that cost fifty minutes and about two dollars:
  # only GCP_ZONE exported, provision.sh falling back to gcloud config for the
  # project, and the runner insisting on ${GCP_PROJECT:?} one line after the
  # cluster came up.
  local envtmp; envtmp="$(mktemp -d)"
  cat > "$envtmp/gcloud" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  *"config get-value project"*)      printf '%s\n' "${STUB_PROJECT-}" ;;
  *"config get-value compute/zone"*) printf '%s\n' "${STUB_ZONE-}" ;;
esac
STUB
  chmod +x "$envtmp/gcloud"

  _env_case() { # <name> <want rc> <project env> <zone env> <stub project> <stub zone>
    local name="$1" want="$2" rc
    ( export PATH="$envtmp:$PATH" STUB_PROJECT="$5" STUB_ZONE="$6"
      if [ -n "$3" ]; then export GCP_PROJECT="$3"; else unset GCP_PROJECT; fi
      if [ -n "$4" ]; then export GCP_ZONE="$4"; else unset GCP_ZONE; fi
      exp_require_env ) >/dev/null 2>&1 && rc=0 || rc=1
    [ "$rc" = "$want" ] && echo "  ok   $name" || { echo "  FAIL $name (rc=$rc want=$want)"; fail=1; }
  }

  _env_case "both set in the environment is accepted"               0 p z "" ""
  _env_case "neither set anywhere is refused before provisioning"   1 "" "" "" ""
  _env_case "the project alone, with no gcloud default, is refused" 1 p "" "" ""
  _env_case "the zone alone, with no gcloud default, is refused"    1 "" z "" ""
  # THE case. provision.sh always accepted this shape; the runners did not, and
  # found out one line after the cluster was up and billing.
  _env_case "the zone exported and the project from gcloud config is accepted" 0 "" z proj ""
  _env_case "both coming from gcloud config is accepted"            0 "" "" proj zn
  # gcloud prints the literal string "(unset)" rather than nothing, and a
  # cluster provisioned into a project named "(unset)" is a very bad afternoon.
  _env_case "gcloud answering (unset) is refused, not used as a project name" 1 "" z "(unset)" ""

  # Every runner has to call it, or the check protects whichever ones remembered.
  local r missing=""
  for r in pod-per-task netpol warm-pool-ab; do
    grep -q 'exp_require_env' "$EXP_SELFTEST_DIR/$r.sh" 2>/dev/null || missing="$missing $r"
  done
  [ -z "$missing" ] \
    && echo "  ok   every runner checks its environment in the preflight" \
    || { echo "  FAIL these runners never check their environment:$missing"; fail=1; }

  rm -rf "$envtmp"

  # ------------------------------------------- cluster name validation
  # The live defect this guards. A stale marker really does hold this value.
  exp_valid_cluster_name "--09191945" \
    && { echo "  FAIL a name gcloud would read as a flag was accepted"; fail=1; } \
    || echo "  ok   a name starting with a hyphen is refused, not handed to gcloud as a flag"
  exp_valid_cluster_name "" \
    && { echo "  FAIL an empty cluster name was accepted"; fail=1; } \
    || echo "  ok   an empty cluster name is refused"
  exp_valid_cluster_name "leoflow-exp-netpol-09192248" \
    && echo "  ok   a real cluster name is accepted" \
    || { echo "  FAIL a real cluster name was refused"; fail=1; }
  exp_valid_cluster_name "leoflow exp" \
    && { echo "  FAIL a name with a space was accepted"; fail=1; } \
    || echo "  ok   a name with a space is refused rather than word-split into two arguments"
  exp_valid_cluster_name "Dexaflow-Exp" \
    && { echo "  FAIL an uppercase name was accepted"; fail=1; } \
    || echo "  ok   an uppercase name is refused, as GKE would refuse it"

  # ------------------------------------------- run dir
  _eq "$(exp_run_dir pod-per-task 20260919T2300Z)" "test/gcp/runs/pod-per-task-20260919T2300Z" "a run directory names its experiment and its moment"

  # ------------------------------------------- the trap, end to end
  # The unit cases above prove the decision. This proves the WIRING, which is
  # where a teardown defect actually lives: a trap that is installed but never
  # fires, or fires and calls nothing, looks exactly like a correct one until a
  # cluster survives a run.
  #
  # A stub teardown.sh records that it was called and with what. The subshell
  # exits non-zero from the middle of a "run", which is the case that matters:
  # the experiment blew up and the cluster must still go away.
  local tmp out; tmp="$(mktemp -d)"
  cat > "$tmp/teardown.sh" <<'STUB'
#!/usr/bin/env bash
echo "$1" >> "$STUB_TD_CALLS"
[ "${STUB_TD_FAIL:-0}" = "1" ] && exit 1
echo "    ok $1 deleted and verified gone"
exit 0
STUB
  chmod +x "$tmp/teardown.sh"

  # Case 1: the experiment fails mid-run. The cluster must still be deleted.
  : > "$tmp/calls1"
  out="$(
    STUB_TD_CALLS="$tmp/calls1" bash -c '
      set -euo pipefail
      source "$1/lib/experiment.sh"
      exp_arm_teardown "$2/teardown.sh"
      exp_adopt_cluster leoflow-exp-doomed-01011200
      false   # the experiment trips set -e, exactly as a failed assertion would
      echo "UNREACHABLE"
    ' _ "$EXP_SELFTEST_DIR" "$tmp" 2>&1
  )" && rc=0 || rc=$?
  if [ "${rc:-0}" != "0" ] && grep -q "leoflow-exp-doomed-01011200" "$tmp/calls1"; then
    echo "  ok   a run that dies mid-experiment still tears its cluster down"
  else
    echo "  FAIL the trap did not delete after a mid-run failure (rc=$rc, calls: $(cat "$tmp/calls1"))"; fail=1
  fi

  # Case 2: the experiment succeeds. The cluster must be deleted anyway, and
  # the run must stay green. A teardown that reddens a passing run gets
  # disabled by the next person in a hurry.
  : > "$tmp/calls2"
  out="$(
    STUB_TD_CALLS="$tmp/calls2" bash -c '
      set -euo pipefail
      source "$1/lib/experiment.sh"
      exp_arm_teardown "$2/teardown.sh"
      exp_adopt_cluster leoflow-exp-happy-01011200
      echo "experiment done"
    ' _ "$EXP_SELFTEST_DIR" "$tmp" 2>&1
  )" && rc=0 || rc=$?
  if [ "${rc:-0}" = "0" ] && grep -q "leoflow-exp-happy-01011200" "$tmp/calls2"; then
    echo "  ok   a run that succeeds still tears down, and stays green"
  else
    echo "  FAIL a successful run did not tear down cleanly (rc=$rc, out: $out)"; fail=1
  fi

  # Case 3: the teardown itself fails. The run must go RED even though the
  # experiment passed, and must print the manual command. This is the one that
  # protects the budget: a teardown failure that exits 0 is a silent bill.
  : > "$tmp/calls3"
  out="$(
    STUB_TD_CALLS="$tmp/calls3" STUB_TD_FAIL=1 bash -c '
      set -euo pipefail
      source "$1/lib/experiment.sh"
      exp_arm_teardown "$2/teardown.sh"
      exp_adopt_cluster leoflow-exp-stuck-01011200
      echo "experiment done"
    ' _ "$EXP_SELFTEST_DIR" "$tmp" 2>&1
  )" && rc=0 || rc=$?
  if [ "${rc:-0}" != "0" ] && printf '%s' "$out" | grep -q "STILL BE RUNNING AND BILLING"; then
    echo "  ok   a failed teardown turns a passing run red and prints the manual delete"
  else
    echo "  FAIL a failed teardown was not fatal (rc=$rc, out: $out)"; fail=1
  fi

  # Case 3b: a marker from an EARLIER run must not be acted on.
  #
  # This is the hazard the recorded-name fallback introduced. The file outlives
  # the run that wrote it, so a later runner that dies before provisioning would
  # read a stale name and delete a cluster that is not its own. Found for real:
  # running this suite from the repository root while a warm-pool experiment was
  # in flight made case 4 below fail, because the trap picked up the live
  # cluster's name off the disk.
  #
  # Hermetic on purpose. It runs in its own directory with its own marker, so
  # the suite no longer depends on what is or is not happening in the checkout.
  : > "$tmp/calls3b"
  mkdir -p "$tmp/stale"
  echo "leoflow-exp-somebody-elses-cluster" > "$tmp/stale/.gcp-experiment-cluster"
  # Backdated well before the trap could be armed.
  touch -t 202001010000 "$tmp/stale/.gcp-experiment-cluster"
  out="$(
    STUB_TD_CALLS="$tmp/calls3b" bash -c '
      set -euo pipefail
      cd "$3"
      source "$1/lib/experiment.sh"
      exp_arm_teardown "$2/teardown.sh"
      exit 7
    ' _ "$EXP_SELFTEST_DIR" "$tmp" "$tmp/stale" 2>&1
  )" && rc=0 || rc=$?
  if [ "${rc:-0}" = "7" ] && [ ! -s "$tmp/calls3b" ]; then
    echo "  ok   a cluster marker left by an EARLIER run is not deleted by this one"
  else
    echo "  FAIL the trap deleted a cluster it did not create (rc=$rc, calls: $(cat "$tmp/calls3b"))"; fail=1
  fi

  # Case 3c: a marker written AFTER the trap was armed is ours, and is acted on.
  # Without this, "ignore stale markers" could be implemented as "ignore all
  # markers" and the #1206 fallback would be silently dead.
  : > "$tmp/calls3c"
  mkdir -p "$tmp/fresh"
  out="$(
    STUB_TD_CALLS="$tmp/calls3c" bash -c '
      set -euo pipefail
      cd "$3"
      source "$1/lib/experiment.sh"
      exp_arm_teardown "$2/teardown.sh"
      sleep 1
      echo "leoflow-exp-ours-09201200" > .gcp-experiment-cluster
      exit 7
    ' _ "$EXP_SELFTEST_DIR" "$tmp" "$tmp/fresh" 2>&1
  )" && rc=0 || rc=$?
  if grep -q 'leoflow-exp-ours-09201200' "$tmp/calls3c" 2>/dev/null; then
    echo "  ok   a cluster this run recorded is still torn down"
  else
    echo "  FAIL the fallback is dead: a cluster this run created was not deleted (calls: $(cat "$tmp/calls3c"))"; fail=1
  fi

  # Case 4: nothing was ever created. The trap must not invent a delete.
  : > "$tmp/calls4"
  out="$(
    STUB_TD_CALLS="$tmp/calls4" bash -c '
      set -euo pipefail
      source "$1/lib/experiment.sh"
      exp_arm_teardown "$2/teardown.sh"
      echo "died before provisioning"
      exit 7
    ' _ "$EXP_SELFTEST_DIR" "$tmp" 2>&1
  )" && rc=0 || rc=$?
  if [ "${rc:-0}" = "7" ] && [ ! -s "$tmp/calls4" ]; then
    echo "  ok   a run that never provisioned deletes nothing, and keeps its own exit status"
  else
    echo "  FAIL the trap acted without a cluster (rc=$rc, calls: $(cat "$tmp/calls4"))"; fail=1
  fi

  # Case 5: adopting a cluster without arming the trap first is refused. That
  # ordering is the only window in which a live cluster is uncovered.
  out="$(
    bash -c '
      set -euo pipefail
      source "$1/lib/experiment.sh"
      exp_adopt_cluster leoflow-exp-uncovered-01011200
    ' _ "$EXP_SELFTEST_DIR" 2>&1
  )" && rc=0 || rc=$?
  if [ "${rc:-0}" != "0" ] && printf '%s' "$out" | grep -q "before exp_arm_teardown"; then
    echo "  ok   adopting a cluster before the trap is armed is refused"
  else
    echo "  FAIL a cluster could be adopted with no trap covering it (rc=$rc, out: $out)"; fail=1
  fi

  rm -rf "$tmp"
  [ "$fail" = "0" ] && { echo "experiment self-test: ok"; return 0; }
  return 1
}

# EXP_SELFTEST_DIR lets the subshell cases above re-source this file by path
# without assuming a working directory.
EXP_SELFTEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Only when EXECUTED, never when sourced. A sourced file inherits the caller's
# positional parameters, so without this guard `netpol.sh --self-test` would run
# this library's self-test and exit before the runner's own ever defined itself.
if [ "${BASH_SOURCE[0]}" = "$0" ] && [ "${1:-}" = "--self-test" ]; then
  self_test; exit $?
fi
return 0 2>/dev/null || true
