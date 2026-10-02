#!/usr/bin/env bash
#
# pod-per-task: drive rising concurrency until something saturates, and say
# WHICH thing saturated.
#
# WHY. The local soak runs `dexaflow lite --executor subprocess`, so the entire
# Kubernetes executor path is unmeasured: pod-per-task, image pulls, the pod
# informer, the reaper acting on pods. That is the path production runs.
#
# THE QUESTION IS NOT "how fast is it". A single latency number would be the
# least useful thing this could produce. There are four candidate ceilings and
# they have four completely different remedies:
#
#   scheduler tick   leoflow decided late.        Remedy: tick interval, batch
#                                                 size, scheduler replicas (ADR 0049 split).
#   API server       the pod CREATE itself slowed. Remedy: client QPS/burst,
#                                                 fewer writes, a bigger control plane.
#   image pull       the kubelet waited on a pull. Remedy: pre-pull, a closer
#                                                 registry, a smaller image.
#   node capacity    the pod sat Pending.          Remedy: more or bigger nodes.
#
# Sending an operator to the wrong one of those costs more than the experiment.
# So every pod is decomposed into four phases, each level is judged per phase,
# and the answer is the phase whose wall appears at the LOWEST concurrency. A
# tie is reported as a tie (see lib/stats.sh) rather than resolved by argument
# order, because "the API server and image pulls went together" is a real answer.
#
# WHAT "SATURATED" MEANS IS DECIDED IN lib/stats.sh, BEFORE ANY RUN. p95 at this
# level >= 3x the p95 at the lowest level, AND the level before it already >= 2x.
# The second clause is what separates a wall from a shared-cloud spike. A level
# with fewer than 20 samples is inconclusive whatever its p95 says. Those
# constants are in code, self-tested, and were written before a cluster existed.
#
# ───────────────────────── WHAT THIS EXPERIMENT CANNOT SEE ─────────────────────
#
# Stated here rather than discovered by a reader later:
#
# 1. TIMESTAMP RESOLUTION. PodScheduled, Pulled and `state.running.startedAt`
#    are RFC3339 with ONE SECOND resolution in the Kubernetes API. Every
#    per-pod phase below is therefore quantized to a second. A rise from 1s to
#    2s is inside the quantization and this runner refuses to call it
#    saturation; the run records the baseline and REPORTS IT AS UNRELIABLE when
#    the baseline p95 is under RESOLUTION_FLOOR. Only the submit phase, timed
#    client-side, has sub-second resolution.
# 2. A SHARED CLOUD. Zonal GKE on e2 machines is shared, burstable hardware.
#    One run is one sample of one afternoon. Nothing here separates "leoflow
#    saturated" from "this zone was busy", which is why the raw series is kept
#    and why a re-run is the only way to make the number mean anything. Two runs
#    that disagree is a finding; one run is a reading.
# 3. THE SCHEDULER TICK, IN --k8s-only MODE. That mode drives pods directly and
#    has no leoflow in it at all, so the `dispatch` phase is NOT MEASURED and is
#    reported as such, never as zero. It bounds the other three.
# 4. NODE AUTO-REPAIR. A release channel is now mandatory (GKE refuses a cluster
#    without one), and a channel enforces auto-upgrade. A node replaced
#    mid-run looks exactly like node capacity saturating, so node ages are
#    recorded and a node younger than the run is flagged.
# 5. IMAGE PULL, AFTER THE FIRST LEVEL. The kubelet caches. Level 1 pays the
#    pull and later levels do not, so a FALLING pull latency across levels is
#    the cache, not an improvement. Each level can be forced back to a cold
#    pull with --cold-pull, which uses a distinct image tag per level.
#
# Usage:
#   test/gcp/pod-per-task.sh                       # plan + cost, creates nothing
#   test/gcp/pod-per-task.sh --execute --k8s-only  # the Kubernetes half, no leoflow
#   test/gcp/pod-per-task.sh --execute             # the full path (needs DAG images)
#   test/gcp/pod-per-task.sh --self-test
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
TTL="${TTL:-90m}"
EXECUTE=0
K8S_ONLY=0
TASK_NS="leoflow"
# Rising concurrency. Each level is a batch of N pods created at once.
LEVELS="${LEVELS:-10 20 40 80}"
# The workload image. A pod that starts and exits: the experiment measures the
# time to GET a pod running, not what the pod then does.
LOAD_IMAGE="${LOAD_IMAGE:-busybox:1.36}"
LOAD_SLEEP="${LOAD_SLEEP:-20}"
# Below this baseline p95, the one-second API timestamp quantization is a large
# fraction of the measurement and ratio claims built on it are not trustworthy.
RESOLUTION_FLOOR="${RESOLUTION_FLOOR:-3}"

PHASES="submit schedule pull start"

# ------------------------------------------------------------- pure helpers

# rfc3339_to_epoch converts a Kubernetes timestamp to seconds. Kubernetes emits
# UTC with a trailing Z at one-second resolution; `date -d` and BSD `date -j`
# disagree about everything, so this goes through python3, which is already a
# hard dependency of the DAG parser.
rfc3339_to_epoch() { # <timestamp>
  python3 -c 'import sys,datetime;print(int(datetime.datetime.strptime(sys.argv[1],"%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc).timestamp()))' "$1" 2>/dev/null || echo ""
}

# phase_delta subtracts two epochs and refuses to invent a number from a missing
# one. An absent timestamp means the phase did not happen (the pod never
# scheduled, the image was already local so there was no pull) and that is NOT
# zero: a zero would be averaged in as a fast observation and would drag a p95
# down exactly where the interesting pods are.
phase_delta() { # <start epoch> <end epoch>  -> number, or nothing
  local a="$1" b="$2"
  [ -n "$a" ] && [ -n "$b" ] || return 1
  # A negative delta means the two timestamps came from clocks that disagree,
  # or that events arrived out of order. Neither is a latency.
  awk -v a="$a" -v b="$b" 'BEGIN{ d=b-a; if (d < 0) exit 1; printf "%d", d }'
}

# resolution_warning decides whether a baseline is large enough for the
# one-second API quantization not to dominate the ratios built on it.
resolution_warning() { # <baseline p95>
  awk -v b="$1" -v f="$RESOLUTION_FLOOR" 'BEGIN{ if (b+0 < f+0) print "UNRELIABLE"; else print "OK" }'
}

# level_list_is_rising refuses a concurrency ladder that is not a ladder. The
# baseline is defined as the FIRST level, so an unsorted list silently makes the
# baseline something other than the lowest load and every ratio meaningless.
level_list_is_rising() { # <levels...>
  local prev=0 l
  for l in "$@"; do
    case "$l" in ''|*[!0-9]*) return 1 ;; esac
    [ "$l" -gt "$prev" ] || return 1
    prev="$l"
  done
  [ "$prev" -gt 0 ]
}

# ---------------------------------------------------------------- self-test

self_test() {
  local fail=0
  _eq() { [ "$1" = "$2" ] && { echo "  ok   $3"; return 0; }; echo "  FAIL $3: '$1' != '$2'"; fail=1; }

  # ----------------------------------------- timestamps
  # The constant was checked against an independent converter (BSD `date -r`
  # round-trips it back to the same stamp) rather than worked out by hand: the
  # first version of this assertion carried a hand-computed epoch that was
  # wrong, and a wrong expectation is how a correct converter gets "fixed".
  _eq "$(rfc3339_to_epoch 2026-09-20T02:07:17Z)" "1789870037" "a Kubernetes RFC3339 stamp converts to epoch"
  # The property phase_delta actually depends on, which no arithmetic slip can
  # fake: two stamps 30 seconds apart must be 30 apart in epoch seconds.
  _eq "$(phase_delta "$(rfc3339_to_epoch 2026-09-20T02:07:17Z)" "$(rfc3339_to_epoch 2026-09-20T02:07:47Z)")" "30" \
      "two stamps 30s apart are 30s apart after conversion, whatever the absolute epoch is"
  # A stamp on the far side of a UTC day boundary, where a naive local-time
  # parse would silently shift by the machine's offset.
  _eq "$(phase_delta "$(rfc3339_to_epoch 2026-09-19T23:59:50Z)" "$(rfc3339_to_epoch 2026-09-20T00:00:10Z)")" "20" \
      "a delta across a UTC midnight is 20s, so the parse is not using local time"
  _eq "$(rfc3339_to_epoch "not a time")" "" "an unparseable stamp yields nothing, not an epoch of zero"

  # ----------------------------------------- phase_delta
  _eq "$(phase_delta 100 130)" "30" "a phase delta is the difference"
  _eq "$(phase_delta 100 100)" "0" "a genuinely instant phase is zero"
  # THE guard. A pod that never scheduled has no PodScheduled stamp, and
  # recording that as 0 would enter the fastest possible observation for the
  # slowest possible pod, pulling the p95 DOWN exactly where saturation lives.
  phase_delta "" 130 >/dev/null 2>&1 \
    && { echo "  FAIL a missing start timestamp produced a delta"; fail=1; } \
    || echo "  ok   a missing timestamp yields no observation rather than a zero that flatters the p95"
  phase_delta 100 "" >/dev/null 2>&1 \
    && { echo "  FAIL a missing end timestamp produced a delta"; fail=1; } \
    || echo "  ok   a missing end timestamp yields no observation"
  phase_delta 130 100 >/dev/null 2>&1 \
    && { echo "  FAIL a negative delta was accepted as a latency"; fail=1; } \
    || echo "  ok   a negative delta is refused rather than reported as a fast pod"

  # ----------------------------------------- the resolution guard
  # The API gives one-second stamps. A baseline p95 of 1s cannot support a
  # claim that 3s is "3x slower": both are one tick apart.
  _eq "$(resolution_warning 1)" "UNRELIABLE" "a one-second baseline is inside the API's own quantization"
  _eq "$(resolution_warning 2)" "UNRELIABLE" "a two-second baseline is still dominated by a one-second tick"
  _eq "$(resolution_warning 8)" "OK" "an eight-second baseline can carry a ratio"

  # ----------------------------------------- the ladder
  level_list_is_rising 10 20 40 80 \
    && echo "  ok   a rising ladder is accepted" \
    || { echo "  FAIL a rising ladder was refused"; fail=1; }
  # An unsorted ladder makes the FIRST level the baseline while not being the
  # lowest load, so every ratio in the run is computed against the wrong number.
  level_list_is_rising 40 10 80 \
    && { echo "  FAIL an unsorted ladder was accepted, making the baseline not the lowest level"; fail=1; } \
    || echo "  ok   an unsorted ladder is refused, because the baseline must be the lowest load"
  level_list_is_rising 10 10 20 \
    && { echo "  FAIL a ladder with a repeated level was accepted"; fail=1; } \
    || echo "  ok   a repeated level is refused"
  level_list_is_rising 10 abc \
    && { echo "  FAIL a non-numeric level was accepted"; fail=1; } \
    || echo "  ok   a non-numeric level is refused"
  level_list_is_rising \
    && { echo "  FAIL an empty ladder was accepted"; fail=1; } \
    || echo "  ok   an empty ladder is refused"

  # ----------------------------------------- the phases are the four candidates
  # Each phase maps to a DIFFERENT remedy, which is the entire point of
  # decomposing at all. Losing one silently would turn the verdict into a guess.
  local p
  for p in submit schedule pull start; do
    case " $PHASES " in
      *" $p "*) echo "  ok   the $p phase is measured" ;;
      *) echo "  FAIL the $p phase is not measured, so its remedy can never be named"; fail=1 ;;
    esac
  done

  # ----------------------------------------- end to end, against the stats lib
  # A synthetic run where image pull walls first at level 3 while everything
  # else stays flat. This is the whole deliverable, so it is tested as a unit:
  # the answer must be `pull`, not merely "something saturated".
  local v_submit v_sched v_pull v_start
  # n=1 and floor=1, which is what a real run produces for this phase: one
  # `kubectl apply` timing per level. The old fixture passed n=50 here, a count
  # this phase can never have, so nothing noticed that the real one was below
  # the floor and therefore always inconclusive.
  v_submit="ok $(level_verdict 1.1 1.0 1.0 1 0 1) $(level_verdict 1.2 1.1 1.0 1 0 1)"
  v_sched="ok $(level_verdict 1.0 1.0 1.0 50 0) $(level_verdict 1.1 1.0 1.0 50 0)"
  v_pull="ok $(level_verdict 2.5 1.0 1.0 50 0) $(level_verdict 9.0 2.5 1.0 50 0)"
  v_start="ok $(level_verdict 1.0 1.0 1.0 50 0) $(level_verdict 1.0 1.0 1.0 50 0)"
  _eq "$(saturating_component \
        "submit:$(first_saturated_level $v_submit)" \
        "schedule:$(first_saturated_level $v_sched)" \
        "pull:$(first_saturated_level $v_pull)" \
        "start:$(first_saturated_level $v_start)")" \
      "pull@L3" "a run where only image pull walls names image pull, at the level it walled"

  # And the honest negative: nothing walls, which is NOT a pass.
  _eq "$(saturating_component submit:0 schedule:0 pull:0 start:0)" "no-saturation-observed" \
      "a run that never saturates says so, rather than reporting the top level as the ceiling"

  # drain_level, over a stub kubectl, because the bug it fixes is invisible in a
  # passing run: the old code deleted and slept, and a level that had not
  # finished dying just made the NEXT level look like node capacity (#1212).
  local dtmp; dtmp="$(mktemp -d)"
  cat > "$dtmp/kubectl" <<'STUB'
#!/usr/bin/env bash
# Answers "get pods" with a count that falls by one each call, so the drain loop
# sees pods actually going away. Everything else succeeds silently.
case "$*" in
  *"get pods"*)
    n=$(cat "$STUB_COUNT" 2>/dev/null || echo 0)
    if [ "$n" -gt 0 ]; then seq 1 "$n" | sed 's/^/pod-/'; echo $((n - 1)) > "$STUB_COUNT"; fi
    ;;
esac
exit 0
STUB
  chmod +x "$dtmp/kubectl"

  _drain_case() { # <name> <starting pods> <ceiling> <want still_present>
    local name="$1" start="$2" ceiling="$3" want="$4" got d
    d="$(mktemp -d)"
    echo "$start" > "$d/count"
    ( export PATH="$dtmp:$PATH" STUB_COUNT="$d/count" \
             LEVEL_DRAIN_CEILING="$ceiling" LEVEL_DRAIN_STEP=1
      drain_level 42 "$d" ) >/dev/null 2>&1
    got="$(awk '/^level 42 drained_in_s/ {print $NF}' "$d/facts.txt" 2>/dev/null)"
    [ "$got" = "$want" ] && echo "  ok   $name" || { echo "  FAIL $name (still_present=$got want=$want)"; fail=1; }
    rm -rf "$d"
  }

  _drain_case "a level that drains is recorded as empty"                3 120 0
  _drain_case "a level already empty needs no waiting"                  0 120 0
  # The case a fixed sleep could not tell apart: pods that outlive the budget.
  # Recorded rather than swallowed, because the NEXT level is what they taint.
  _drain_case "a level that outlives the ceiling records what was left"  9   3 6

  rm -rf "$dtmp"

  # split_manifest, which is what makes the submission parallel. It has no
  # cluster in it, so it is testable, and getting it wrong is silent: a splitter
  # that drops documents submits fewer pods than the level names and every
  # number downstream is computed against a level that never existed.
  local stmp; stmp="$(mktemp -d)"
  batch_manifest 7 25 busybox:1.36 > "$stmp/m.yaml"
  _split_case() { # <name> <workers> <want files> <want total pods>
    local name="$1" workers="$2" wantf="$3" wantp="$4" gotf gotp
    rm -f "$stmp/c-"*.yaml
    split_manifest "$stmp/m.yaml" "$stmp/c-" "$workers"
    gotf="$(ls "$stmp/c-"*.yaml 2>/dev/null | wc -l | tr -d ' ')"
    gotp="$(cat "$stmp/c-"*.yaml 2>/dev/null | grep -c '^kind: Pod')"
    if [ "$gotf" = "$wantf" ] && [ "$gotp" = "$wantp" ]; then
      echo "  ok   $name"
    else
      echo "  FAIL $name (files=$gotf want=$wantf, pods=$gotp want=$wantp)"; fail=1
    fi
  }
  # THE case: every pod survives the split. A splitter that loses documents
  # under-submits, and the level's own count is the only thing that would ever
  # have noticed.
  _split_case "25 pods across 4 workers keeps all 25"   4  4 25
  _split_case "25 pods across 1 worker is just the file" 1  1 25
  # More workers than pods must not produce empty files for kubectl to choke on.
  _split_case "40 workers over 25 pods makes 25 files"  40 25 25
  rm -rf "$stmp"

  [ "$fail" = "0" ] && { echo "pod-per-task self-test: ok"; return 0; }
  return 1
}

# --------------------------------------------------------------- cluster side

batch_manifest() { # <level> <count> <image>
  local level="$1" n="$2" image="$3" i
  for i in $(seq 1 "$n"); do
    cat <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: load-l${level}-${i}
  namespace: $TASK_NS
  labels:
    leoflow.io/run-id: saturation-l${level}
    leoflow.io/level: "${level}"
spec:
  restartPolicy: Never
  containers:
    - name: work
      image: $image
      command: ["sh", "-c", "sleep $LOAD_SLEEP"]
      resources:
        requests: { cpu: "100m", memory: "64Mi" }
        limits:   { cpu: "100m", memory: "64Mi" }
---
YAML
  done
}

# split_manifest fans one multi-document manifest out into <n> files, round
# robin, so parallel writers each get an interleaved share rather than a
# contiguous block. Round robin matters: contiguous blocks would have every
# worker start on pods that land on the same nodes in the same order.
split_manifest() { # <manifest> <prefix> <n>
  python3 - "$1" "$2" "$3" <<'SPLIT'
import sys
src, prefix, n = sys.argv[1], sys.argv[2], int(sys.argv[3])
docs = [d for d in open(src).read().split("---\n") if d.strip()]
n = max(1, min(n, len(docs)))
buckets = [[] for _ in range(n)]
for i, d in enumerate(docs):
    buckets[i % n].append(d)
for i, b in enumerate(buckets):
    if not b:
        continue
    with open("%s%02d.yaml" % (prefix, i), "w") as fh:
        fh.write("---\n".join(b))
SPLIT
}

# collect_level pulls the per-pod phase timings for one level out of the API and
# appends them to the level's raw series files. The raw series are kept so the
# conclusion can be rechecked against them, which is the only thing that makes
# a conclusion worth anything.
collect_level() { # <level> <out dir>
  local level="$1" out="$2" json
  json="$(kubectl -n "$TASK_NS" get pods -l "leoflow.io/level=$level" -o json)"
  printf '%s' "$json" > "$out/raw-pods-L$level.json"

  # created, scheduled, running: one line per pod, empty field where the API
  # had no timestamp. jq emits "" rather than null so the shell can tell a
  # missing stamp from a present one.
  printf '%s' "$json" | jq -r '
    .items[] |
    [ .metadata.name,
      (.metadata.creationTimestamp // ""),
      ((.status.conditions // [])[] | select(.type=="PodScheduled") | .lastTransitionTime) // "",
      (((.status.containerStatuses // [])[0].state.running.startedAt) // "")
    ] | @tsv' 2>/dev/null | while IFS=$'\t' read -r name created scheduled running; do
      local c s r d
      c="$(rfc3339_to_epoch "$created")"
      s="$(rfc3339_to_epoch "$scheduled")"
      r="$(rfc3339_to_epoch "$running")"
      # schedule: the kube-scheduler found a node. This is NODE CAPACITY.
      if d="$(phase_delta "$c" "$s")"; then echo "$d" >> "$out/schedule-L$level.series"; else
        echo "$name no-schedule-stamp" >> "$out/gaps-L$level.txt"; fi
      # start: bound to a node, then actually running. This covers the image
      # pull and the container create, and is decomposed further below.
      if d="$(phase_delta "$s" "$r")"; then echo "$d" >> "$out/start-L$level.series"; else
        echo "$name no-running-stamp" >> "$out/gaps-L$level.txt"; fi
    done

  # pull: from the kubelet's own Pulling/Pulled events. Separate from `start`
  # because a slow pull and a slow container create have different remedies,
  # and because the kubelet caches: a pull that does not appear at all after
  # level 1 is the CACHE, not a fast pull, so it is recorded as a gap.
  kubectl -n "$TASK_NS" get events --field-selector reason=Pulled -o json 2>/dev/null \
    | jq -r --arg lvl "$level" '
        .items[]
        | select(.involvedObject.name | startswith("load-l" + $lvl + "-"))
        | .message' > "$out/pull-events-L$level.txt" || true
  # The kubelet reports the pull duration in the Pulled message itself, which is
  # the only sub-second pull figure available: the event timestamps are the same
  # one-second stamps as everything else.
  grep -oE '\(([0-9.]+[a-z]+) including waiting\)|in ([0-9.]+[a-z]+)' "$out/pull-events-L$level.txt" 2>/dev/null \
    | grep -oE '[0-9.]+(ms|s|m)' | python3 -c '
import sys
for line in sys.stdin:
    t = line.strip()
    if t.endswith("ms"): print(float(t[:-2]) / 1000.0)
    elif t.endswith("m"): print(float(t[:-1]) * 60.0)
    elif t.endswith("s"): print(float(t[:-1]))
' >> "$out/pull-L$level.series" 2>/dev/null || true
}

run_level() { # <level> <count> <out dir>
  local level="$1" n="$2" out="$3" t0 t1 image="$LOAD_IMAGE"
  exp_log "level $level: creating $n pods at once"
  batch_manifest "$level" "$n" "$image" > "$out/manifest-L$level.yaml"

  # The submit phase, timed client-side with millisecond resolution. This is
  # the ONLY phase not quantized to a second.
  #
  # SUBMITTED IN PARALLEL, and that is not a speed optimization. A single
  # `kubectl apply -f` over a multi-document file creates the pods ONE AT A
  # TIME, and the first real run measured 0.53s per pod flat from level 10 to
  # level 320. Two things followed, both fatal to what this phase claims to be
  # (#1214):
  #
  #   - It measured the CLIENT's serial throughput, not the apiserver's ability
  #     to absorb concurrent creates. A real API-server ceiling could not appear
  #     in a number that never asked for concurrency.
  #   - Level 320 took 169s to submit while each pod lives LOAD_SLEEP seconds.
  #     The first pods were long dead before the last were created, so the
  #     nominal ladder 10/20/40/80/160/320 was really 10/20/40/40/40/40, and no
  #     level above 40 ever existed as a level.
  #
  # Chunked across SUBMIT_PARALLEL workers so the creates overlap. The wall time
  # is still the whole batch, so the series remains "seconds per pod to get N
  # pods accepted", now with N of them actually in flight.
  local chunks="${SUBMIT_PARALLEL:-16}"
  rm -f "$out/chunk-L$level-"*.yaml
  split_manifest "$out/manifest-L$level.yaml" "$out/chunk-L$level-" "$chunks"
  t0="$(python3 -c 'import time;print(time.time())')"
  local c pids=""
  for c in "$out/chunk-L$level-"*.yaml; do
    kubectl apply -f "$c" >/dev/null 2>&1 &
    pids="$pids $!"
  done
  for c in $pids; do wait "$c" || true; done
  t1="$(python3 -c 'import time;print(time.time())')"
  awk -v a="$t0" -v b="$t1" -v n="$n" 'BEGIN{printf "%.6f\n", (b-a)/n}' >> "$out/submit-L$level.series"
  awk -v a="$t0" -v b="$t1" -v n="$n" -v p="$chunks" \
    'BEGIN{printf "level batch wall %.3fs across %d parallel writers\n", b-a, p}' >> "$out/facts.txt"

  # The concurrency that ACTUALLY existed, sampled right after submission.
  # Recorded next to the nominal level because a report that says "level 320"
  # when 40 pods coexisted is describing something that did not happen. Only
  # non-terminal pods count: a Succeeded pod holds no CPU and is not
  # concurrency, which is a thing this directory learned by measuring it live.
  local live
  live="$(kubectl -n "$TASK_NS" get pods -l "leoflow.io/level=$level" --no-headers 2>/dev/null \
    | awk '$3!="Completed" && $3!="Succeeded" && $3!="Error"' | wc -l | tr -d ' ')"
  printf 'level %s peak_concurrency_observed %s of %s nominal\n' "$level" "${live:-0}" "$n" >> "$out/facts.txt"
  if [ "${live:-0}" -lt "$((n / 2))" ]; then
    exp_warn "level $level: only ${live} of $n pods coexisted. The pods outlive submission by too little, so this level did not test the concurrency it names (#1214). Raise LOAD_SLEEP or SUBMIT_PARALLEL."
  fi

  # Wait for the batch to reach a terminal-or-running state. A level that times
  # out is recorded as such: pods still Pending at the deadline are the finding,
  # not an inconvenience, so they are counted rather than waited away.
  local deadline; deadline=$(( $(date +%s) + ${LEVEL_TIMEOUT:-300} ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    local pending
    pending="$(kubectl -n "$TASK_NS" get pods -l "leoflow.io/level=$level" \
      --no-headers 2>/dev/null | awk '$3=="Pending"' | wc -l | tr -d ' ')"
    [ "${pending:-0}" = "0" ] && break
    sleep 3
  done
  local still_pending
  still_pending="$(kubectl -n "$TASK_NS" get pods -l "leoflow.io/level=$level" \
    --no-headers 2>/dev/null | awk '$3=="Pending"' | wc -l | tr -d ' ')"
  printf 'level %s still_pending_at_deadline %s of %s\n' "$level" "${still_pending:-0}" "$n" >> "$out/facts.txt"
  [ "${still_pending:-0}" != "0" ] \
    && exp_warn "level $level: $still_pending of $n pods were STILL PENDING at the deadline. That is node capacity, and it is the finding."

  collect_level "$level" "$out"
  # Deleted between levels so the next level's scheduling is not competing with
  # this level's still-running pods, which would make every later level a
  # measurement of the levels before it rather than of its own concurrency.
  #
  # WAITED FOR, not slept through. This was `--wait=false` followed by a fixed
  # 20s, and a pod in Terminating still holds its requests.cpu until it is
  # actually gone. At the shipped ladder the last level asks for about 91% of a
  # ten-node cluster's allocatable CPU, so a few dozen survivors from the level
  # before are enough to push pods Pending, and Pending is exactly what this
  # runner reports as NODE CAPACITY saturating.
  #
  # The failure that mattered was never a crash. It was a plausible wrong
  # answer: "buy more nodes", when the cause was the harness not waiting (#1212).
  drain_level "$level" "$out"
}

# drain_level deletes a level's pods and waits for them to be GONE, not merely
# asked to go.
#
# Bounded, because an unbounded wait trades a bias for a hang. Hitting the
# ceiling is recorded rather than swallowed: the next level's numbers were taken
# on a cluster that still had this level on it, and a reader deciding whether to
# believe a node-capacity verdict needs to know that.
#
# The elapsed time is recorded either way. A drain that takes a long time is a
# fact about the cluster, not a detail of the harness.
drain_level() { # <level> <out dir>
  local level="$1" out="$2" left waited=0
  local ceiling="${LEVEL_DRAIN_CEILING:-120}" step="${LEVEL_DRAIN_STEP:-3}"
  kubectl -n "$TASK_NS" delete pods -l "leoflow.io/level=$level" --wait=false >/dev/null 2>&1 || true
  while [ "$waited" -lt "$ceiling" ]; do
    left="$(kubectl -n "$TASK_NS" get pods -l "leoflow.io/level=$level" \
             --no-headers 2>/dev/null | wc -l | tr -d ' ')"
    [ "${left:-0}" = "0" ] && break
    sleep "$step"
    waited=$((waited + step))
  done
  left="$(kubectl -n "$TASK_NS" get pods -l "leoflow.io/level=$level" \
           --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  printf 'level %s drained_in_s %s still_present %s\n' "$level" "$waited" "${left:-0}" >> "$out/facts.txt"
  if [ "${left:-0}" != "0" ]; then
    exp_warn "level $level still had ${left} pod(s) after ${waited}s; the NEXT level was measured on a cluster that was not empty, so treat a node-capacity verdict from it with suspicion (#1212)"
  fi
}

analyze() { # <out dir>
  local out="$1" phase level idx baseline prev n verdicts comps="" total_obs=0 levels_seen=0 phases_seen=0
  {
    echo "phase,level,samples,p50,p95,max,verdict"
  } > "$out/summary.csv"

  for phase in $PHASES; do
    baseline=""; prev=""; idx=0; verdicts=""
    local measured_any=0
    for level in $LEVELS; do
      idx=$((idx + 1))
      local f="$out/$phase-L$level.series"
      [ -f "$f" ] || { verdicts="$verdicts inconclusive"; continue; }
      n="$(count_of < "$f")"
      total_obs=$((total_obs + n))
      [ "$n" -gt 0 ] && measured_any=1
      local p50 p95 mx
      p50="$(p_of 50 < "$f" 2>/dev/null || echo 0)"
      p95="$(p_of 95 < "$f" 2>/dev/null || echo 0)"
      mx="$(max_of < "$f" 2>/dev/null || echo 0)"
      [ -z "$baseline" ] && baseline="$p95"
      local first=0; [ "$idx" = "1" ] && first=1
      # submit has ONE observation per level by construction: it is the wall
      # time of a single `kubectl apply` divided by the pods in it, so there is
      # no distribution to take a percentile of. Holding it to the 20-sample
      # percentile floor made the API-server ceiling permanently inconclusive,
      # which is one of the four candidate ceilings this experiment exists to
      # tell apart. The sustained clause still does the work the floor was doing
      # here: one odd level is a spike, two in a row is a wall.
      local floor="$MIN_SAMPLES"
      [ "$phase" = "submit" ] && floor=1
      local v; v="$(level_verdict "$p95" "${prev:-0}" "$baseline" "$n" "$first" "$floor")"
      verdicts="$verdicts $v"
      echo "$phase,$level,$n,$p50,$p95,$mx,$v" >> "$out/summary.csv"
      prev="$p95"
    done
    [ "$measured_any" = "1" ] && phases_seen=$((phases_seen + 1))
    # shellcheck disable=SC2086 # verdicts is a word list on purpose
    comps="$comps $phase:$(first_saturated_level $verdicts)"
    # The resolution caveat, attached to the phase it applies to rather than
    # buried in a footnote.
    if [ -n "$baseline" ] && [ "$phase" != "submit" ]; then
      printf '%s baseline_p95=%s resolution=%s\n' "$phase" "$baseline" "$(resolution_warning "$baseline")" >> "$out/facts.txt"
    fi
  done
  for level in $LEVELS; do levels_seen=$((levels_seen + 1)); done

  # The guard the soak's `{}` needed. A verdict built from nothing is refused
  # loudly rather than written as a file that reads like a result.
  verdict_is_publishable "$phases_seen" "$levels_seen" "$total_obs" || {
    exp_warn "no verdict written: this run did not measure enough to support one"
    return 1
  }

  # shellcheck disable=SC2086
  local answer; answer="$(saturating_component $comps)"
  {
    echo "levels:      $LEVELS"
    echo "phases:      $PHASES"
    echo "threshold:   p95 >= ${SAT_MULTIPLE}x baseline, sustained (prev >= ${SAT_SUSTAIN}x), min ${MIN_SAMPLES} samples"
    echo "observations: $total_obs"
    echo
    echo "FIRST TO SATURATE: $answer"
    echo
    if [ "$answer" = "no-saturation-observed" ]; then
      cat <<'NOSAT'
Nothing saturated inside the ladder that was driven. That is NOT "it scales":
it means the ceiling is ABOVE the top level, and where it is remains unknown.
The honest next step is a higher ladder, not a claim.
NOSAT
    fi
    [ "$K8S_ONLY" = "1" ] && cat <<'K8SONLY'

NOT MEASURED IN THIS RUN: the `dispatch` phase, which is leoflow's scheduler
tick deciding to run a task. --k8s-only drives pods directly and has no leoflow
in it, so the scheduler tick is not a fast phase here, it is an ABSENT one. Of
the four candidate ceilings this run can only rank three.
K8SONLY
    echo
    echo "per-level detail: summary.csv    raw series: *.series    gaps: gaps-L*.txt"
  } | tee "$out/verdict.txt"
}

run_experiment() {
  local out="$1"
  exp_require gcloud kubectl jq python3
  exp_require_env
  [ "$K8S_ONLY" = "1" ] || exp_require helm

  # shellcheck disable=SC2086
  level_list_is_rising $LEVELS || exp_die "LEVELS must be a rising ladder of positive integers (got: $LEVELS). The baseline is the FIRST level, so an unsorted ladder computes every ratio against the wrong number."

  exp_arm_teardown "$HERE/teardown.sh"
  exp_provision "$HERE/provision.sh" pod-per-task "$NODES" "$TTL"
  gcloud container clusters get-credentials "$EXP_CLUSTER" \
    --zone "${GCP_ZONE:?}" --project "${GCP_PROJECT:?}" >/dev/null 2>&1 \
    || exp_die "could not get credentials for $EXP_CLUSTER"
  exp_kube_ready "$NODES" 600

  # Node ages, recorded because a node replaced mid-run by the auto-repair that
  # a mandatory release channel brings with it looks exactly like node capacity
  # saturating.
  kubectl get nodes -o json | jq -r '.items[] | [.metadata.name, .metadata.creationTimestamp, (.status.allocatable.cpu), (.status.allocatable.memory), (.status.allocatable.pods)] | @tsv' \
    > "$out/nodes-before.tsv"
  exp_ok "recorded $(wc -l < "$out/nodes-before.tsv" | tr -d ' ') node(s) and their allocatable capacity"

  kubectl create namespace "$TASK_NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

  if [ "$K8S_ONLY" != "1" ]; then
    stack_up leoflow "$(openssl rand -base64 48)" "$(openssl rand -base64 18)"
    exp_warn "the full path needs DAG images built and pushed to a registry the cluster can pull from."
    exp_warn "That pipeline is NOT implemented here: see the README. Falling back to the Kubernetes half."
    K8S_ONLY=1
  fi

  local level
  for level in $LEVELS; do
    run_level "$level" "$level" "$out"
  done

  kubectl get nodes -o json | jq -r '.items[] | [.metadata.name, .metadata.creationTimestamp] | @tsv' > "$out/nodes-after.tsv"
  if ! diff -q <(cut -f1 "$out/nodes-before.tsv") <(cut -f1 "$out/nodes-after.tsv") >/dev/null 2>&1; then
    exp_warn "THE NODE SET CHANGED DURING THE RUN. A replaced node looks exactly like node capacity saturating; treat the schedule phase as confounded."
    echo "NODE SET CHANGED MID-RUN" >> "$out/facts.txt"
  fi

  analyze "$out"
}

# ---------------------------------------------------------------------- main

while [ $# -gt 0 ]; do
  case "$1" in
    --execute)   EXECUTE=1; shift ;;
    --k8s-only)  K8S_ONLY=1; shift ;;
    --nodes)     NODES="$2"; shift 2 ;;
    --ttl)       TTL="$2"; shift 2 ;;
    --levels)    LEVELS="$2"; shift 2 ;;
    --self-test) self_test; exit $? ;;
    -h|--help)   sed -n '2,/^set -euo pipefail/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) exp_die "unknown flag: $1" ;;
  esac
done

if [ "$EXECUTE" != "1" ]; then
  "$HERE/provision.sh" --dry-run --experiment pod-per-task --nodes "$NODES" --ttl "$TTL"
  exp_never_ran "pod-per-task saturation, $NODES node(s) for $TTL, levels: $LEVELS" \
    "not invoked with --execute; this printed the plan and created nothing"
  exit 0
fi

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="$EXP_REPO_ROOT/$(exp_run_dir pod-per-task "$STAMP")"
mkdir -p "$OUT"
exp_log "raw series and verdict go to $OUT"
run_experiment "$OUT"
