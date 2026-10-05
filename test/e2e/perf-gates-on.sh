#!/usr/bin/env bash
# Asserts that the performance gates e2e-gates.yaml turns on really took effect
# in the running control plane. Every one of them ships off by default, and a
# gate the environment names but the process never read looks exactly like a
# gate that is on, so a leg that only exports the variables proves nothing
# (performance audit, finding M-09: the production wiring of every gate was
# executed by no test and no CI job). This runs against the LIVE process, as the
# extra-checks hook of lite-login.sh and e2e.sh (LEOFLOW_E2E_EXTRA_CHECKS), and
# fails the leg when a gate did not apply.
#
# Inputs, through the environment (the hooks pass the first three):
#   BASE            the HTTP API base URL, e.g. http://127.0.0.1:18099
#   TOKEN           a bearer token that may read task instances
#   METRICS         the metrics listener base URL, e.g. http://127.0.0.1:19098
#   SERVER_LOG      the control plane's log; the boot echoes are asserted when set
#   PERF_GATES_LEG  lite (default) or k3d; k3d also asserts what only a cluster
#                   can show (the s3 sink, settled-run pod collection)
#   DAG_ID, RUN_ID  a DAG and run for the grid route; e2e.sh passes real ones,
#                   and a pair that does not exist is served as an empty grid
#   TASK_NAMESPACE  k3d: the task pods' namespace (default leoflow)
#
# What is asserted, and which gate each line proves:
#   /metrics has dexaflow_ families and no leoflow_ twin ....... observability.metrics.drop_legacy_names
#   the grid route answers ETag, "private, no-cache", then 304 .. ui.etag_revalidation
#   boot echo "buffered dispatch enabled", buffer 64, workers 4 . scheduler.dispatch.buffer_size, .workers
#   boot echo "scheduler uses a dedicated database pool", 4 .... database.scheduler_max_conns
#   k3d: boot echo "s3 object-store backend enabled", segmented  logs.backend, logs.sink.layout
#   k3d: no finished task pod survives the next maintenance sweep executor.collect_settled_run_pods
# database.statement_timeout_ms and logs.tail.publish leave no trace a client of
# the process can see; e2e-gates.yaml asserts them from the Postgres and Redis
# side once the leg is over.
set -euo pipefail

: "${BASE:?BASE is required (the HTTP API base URL)}"
: "${TOKEN:?TOKEN is required (a bearer token)}"
: "${METRICS:?METRICS is required (the metrics listener base URL)}"
LEG="${PERF_GATES_LEG:-lite}"
DAG="${DAG_ID:-nodag}"
RUN="${RUN_ID:-norun}"
NS="${TASK_NAMESPACE:-leoflow}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1" >&2; exit 1; }
case "$LEG" in
  lite|k3d) ;;
  *) fail "PERF_GATES_LEG must be lite or k3d (got '${LEG}')" ;;
esac

echo "==> perf gates: /metrics serves each family once, under its dexaflow_ name (drop_legacy_names)"
curl -fsS "${METRICS}/metrics" > "$TMP/metrics" || fail "GET ${METRICS}/metrics failed"
grep -qE '^dexaflow_[a-z0-9_]+' "$TMP/metrics" || fail "no dexaflow_ family on /metrics"
if grep -qE '^(# (HELP|TYPE) )?leoflow_' "$TMP/metrics"; then
  fail "a leoflow_ twin is still served, so observability.metrics.drop_legacy_names did not apply, for example:
$(grep -E '^(# (HELP|TYPE) )?leoflow_' "$TMP/metrics" | head -3)"
fi
pass "/metrics: $(grep -cE '^# TYPE dexaflow_' "$TMP/metrics") dexaflow_ families, no leoflow_ twin"

echo "==> perf gates: the grid route lets the browser revalidate (ui.etag_revalidation)"
GRID="${BASE}/ui/grid/ti_summaries/${DAG}?run_ids=${RUN}"
code="$(curl -sS -o /dev/null -D "$TMP/grid.raw" -w '%{http_code}' -H "Authorization: Bearer ${TOKEN}" "$GRID")"
[ "$code" = "200" ] || fail "GET ${GRID} returned ${code} (want 200)"
tr -d '\r' < "$TMP/grid.raw" > "$TMP/grid.headers"
etag="$(awk 'tolower($1) == "etag:" {print $2}' "$TMP/grid.headers")"
[ -n "$etag" ] || fail "the grid response carries no ETag"
cc="$(awk 'tolower($1) == "cache-control:" {sub(/^[^:]*: */, ""); print}' "$TMP/grid.headers")"
[ "$cc" = "private, no-cache" ] \
  || fail "Cache-Control is '${cc}' (want 'private, no-cache'): ui.etag_revalidation did not apply"
grep -qiE '^vary: .*authorization' "$TMP/grid.headers" || fail "the grid response carries no Vary: Authorization"
code="$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${TOKEN}" -H "If-None-Match: ${etag}" "$GRID")"
[ "$code" = "304" ] || fail "a conditional GET with If-None-Match: ${etag} returned ${code} (want 304)"
pass "grid route: ETag ${etag}, Cache-Control private, no-cache, conditional GET answered 304"

if [ -n "${SERVER_LOG:-}" ]; then
  echo "==> perf gates: the boot echoes in ${SERVER_LOG}"
  [ -r "$SERVER_LOG" ] || fail "SERVER_LOG=${SERVER_LOG} is not readable"
  # The server logs each of these only when the gate applied; the patterns
  # accept the JSON and the text log formats.
  echoed() { # <regex> <gate that did not apply>
    grep -qE "$1" "$SERVER_LOG" || fail "no log line matches /$1/: $2 did not apply"
  }
  echoed 'buffered dispatch enabled.*buffer_size["=: ]+64\b.*workers["=: ]+4\b' "scheduler.dispatch.buffer_size=64 with workers=4"
  echoed 'scheduler uses a dedicated database pool.*max_conns["=: ]+4\b' "database.scheduler_max_conns=4"
  echoes="buffered dispatch (64/4), dedicated scheduler pool (4)"
  if [ "$LEG" = "k3d" ]; then
    echoed 'task logs: s3 object-store backend enabled.*layout["=: ]+"?segmented' "logs.backend=s3 with logs.sink.layout=segmented"
    echoes="${echoes}, s3 sink in the segmented layout"
  fi
  pass "boot echoes: ${echoes}"
fi

if [ "$LEG" = "k3d" ]; then
  echo "==> perf gates: a settled run's finished pods go in the next sweep (collect_settled_run_pods)"
  finished_pods() {
    kubectl get pods -n "$NS" --field-selector=status.phase=Succeeded -o name 2>/dev/null
    kubectl get pods -n "$NS" --field-selector=status.phase=Failed -o name 2>/dev/null
  }
  # Without the gate a finished pod stays for the reconciler's ten minute grace
  # (podGCGracePeriod), and the last run of e2e.sh settled seconds ago, so its
  # pods are still here unless the collection runs (a poke pod of a reschedule
  # sensor is collected at once either way, so it never counts). The maintenance
  # cycle runs every 30 s; allow a few of them. The pods seen at the start are
  # printed so the log shows what the sweep had to collect; none at all is
  # still a pass, since a sweep can land between the last run settling and
  # this check, and under the default a finished pod is always here.
  echo "  finished pods when the check started: $(finished_pods | awk NF | tr '\n' ' ')"
  deadline=$(( $(date +%s) + 180 ))
  while :; do
    left="$(finished_pods | awk NF)"
    [ -z "$left" ] && break
    if [ "$(date +%s)" -gt "$deadline" ]; then
      fail "finished task pods are still present 180 s after their runs settled, so executor.collect_settled_run_pods did not apply:
${left}"
    fi
    sleep 5
  done
  pass "no finished task pod is left in namespace ${NS}"
fi

echo "  every performance gate this leg turns on took effect at runtime (${LEG})"
