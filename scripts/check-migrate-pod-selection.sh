#!/usr/bin/env bash
# The migration hook pod must not be selectable by any control-plane Service or
# PodDisruptionBudget, in any deployment mode (#1055).
#
# Selection in Kubernetes is a SUBSET relation: a Service sends traffic to every
# pod whose labels contain all of the selector's key/value pairs. Extra labels on
# the pod do not exclude it. `leoflow.roleSelectorLabels` for the default "all"
# role is exactly `leoflow.selectorLabels` (ADR 0049 keeps it that way so the
# Deployment's immutable selector survives an upgrade from a pre-split install),
# so the migrate Job's pod template carrying that same set made the pod an
# endpoint of the control-plane Service. A Job pod has no readinessProbe, so it
# is Ready the instant its container starts: on `helm upgrade`, where the Service
# already exists while the pre-upgrade hook runs, the hook pod was an endpoint of
# the control plane and a healthy member of its PodDisruptionBudget. `helm install`
# was unaffected — no Service exists yet.
#
# The disruption half was the measured loss (a percentage-valued budget fails
# outright with "jobs.batch does not implement the scale subresource"). The
# traffic half is latent rather than active, and only by accident: every Service
# port here uses a NAMED targetPort and the hook pod declares no containerPort, so
# the endpoint lands in a slice with `ports: null` and kube-proxy programs nothing.
# One numeric targetPort removes that, and consumers that resolve ports themselves
# never had it. Both are reasons to fix the selection, not to rely on the accident.
#
# Why this is a script and not a helm-unittest case: the invariant lives BETWEEN
# two rendered documents, and helm-unittest asserts one document at a time. Both
# maps were individually "correct" for ~246 cases while their relationship was
# wrong. This renders the chart and compares the real maps, so a future edit to
# EITHER side — the Job's pod labels or any control-plane selector — is caught by
# the same gate. helm/dexaflow/tests/migrate_pod_selection_test.yaml pins the
# shapes; this asserts the property.
#
# Usage: scripts/check-migrate-pod-selection.sh [--self-test]
#        scripts/check-migrate-pod-selection.sh <rendered.yaml> [<mode-label>]
set -euo pipefail

CHART="helm/dexaflow"
# The chart refuses to render without these; they are fixtures, not credentials.
BASE_VALUES=(
	--set 'database.url=postgres://leoflow:p@db:5432/leoflow?sslmode=disable'
	--set 'redis.url=redis://r:6379/0'
	--set 'auth.jwtSecret=migrate-pod-selection-check-fixture'
)

# compare <rendered-manifest-file> <mode-label>
# Exits non-zero when any Service / PodDisruptionBudget selector in the file is a
# subset of any Job pod template's labels.
compare() {
	python3 - "$1" "$2" <<'PY'
import sys

try:
	import yaml
except ImportError:
	sys.exit(
		"FAIL: PyYAML is required to compare the rendered label maps. "
		"Install it (python3 -m pip install --user pyyaml) — this gate deliberately "
		"does not skip, because a selection bug that renders clean is invisible "
		"everywhere else."
	)

path, mode = sys.argv[1], sys.argv[2]
with open(path) as fh:
	docs = [d for d in yaml.safe_load_all(fh) if isinstance(d, dict)]

jobs = [d for d in docs if d.get("kind") == "Job"]
if not jobs:
	sys.exit(f"FAIL: {mode}: no Job rendered — the migrate hook is what this gate exists to check, so an empty set is a failure, not a pass.")

# (kind, name, selector-map) for everything that selects control-plane pods.
selectors = []
for d in docs:
	kind = d.get("kind")
	name = (d.get("metadata") or {}).get("name", "<unnamed>")
	spec = d.get("spec") or {}
	if kind == "Service":
		selectors.append((kind, name, spec.get("selector") or {}))
	elif kind == "PodDisruptionBudget":
		sel = spec.get("selector") or {}
		if sel.get("matchExpressions"):
			sys.exit(f"FAIL: {mode}: PodDisruptionBudget/{name} uses matchExpressions, which this gate does not evaluate — teach it the expression semantics rather than leave the case unchecked.")
		selectors.append((kind, name, sel.get("matchLabels") or {}))
if not selectors:
	sys.exit(f"FAIL: {mode}: nothing selects control-plane pods in this render — the comparison had no left-hand side, which is a broken gate, not a pass.")

problems = []
compared = 0
for job in jobs:
	jname = (job.get("metadata") or {}).get("name", "<unnamed>")
	pod = ((job.get("spec") or {}).get("template") or {}).get("metadata") or {}
	labels = pod.get("labels") or {}
	if not labels:
		sys.exit(f"FAIL: {mode}: Job/{jname} pod template has no labels at all — that is unselectable by accident, not by design; give it its own label set.")
	for kind, name, sel in selectors:
		if not sel:
			# An empty selector means opposite things per kind, and skipping both
			# was the hole: for a Service it is correct (no selector, so the
			# endpoints controller does nothing), but a PodDisruptionBudget with
			# `selector: {}` matches EVERY pod in the namespace — including this
			# one. Skipping it meant the gate could report OK having performed
			# zero comparisons, which is the "passes while measuring nothing"
			# shape this repo has shipped twice.
			if kind == "PodDisruptionBudget":
				problems.append(
					f"  {kind}/{name} has an empty selector, which matches every pod in the namespace, so it selects Job/{jname}"
				)
				compared += 1
			continue
		compared += 1
		if all(labels.get(k) == v for k, v in sel.items()):
			problems.append(
				f"  {kind}/{name} selector {sel} is a SUBSET of Job/{jname} pod labels {labels}"
			)

if compared == 0:
	sys.exit(f"FAIL: {mode}: zero selector/pod comparisons were performed, so an OK here would mean nothing was measured.")

if problems:
	sys.exit(
		f"FAIL: {mode}: the migrate hook pod is selected by the control plane:\n"
		+ "\n".join(problems)
		+ "\n  A selector matches every pod whose labels CONTAIN it, so adding a label to the"
		"\n  pod does not exclude it — the pod must differ on a key the selector carries."
		"\n  On `helm upgrade` this puts a container with no listener into the Service's"
		"\n  EndpointSlice for the whole migration window (#1055)."
	)

print(f"OK:   {mode}: no Service/PDB selector is a subset of the migrate pod's labels")
PY
}

# render <mode-label> <extra helm --set args...>
render_and_compare() {
	local mode="$1"
	shift
	local rendered
	rendered="$(mktemp)"
	# Rendered to a FILE, and helm's exit status read on its own line: piping
	# helm into python would report python's status and a failed render would
	# read as a pass.
	if ! helm template leoflow-selection-check "$CHART" "${BASE_VALUES[@]}" "$@" >"$rendered" 2>"$rendered.err"; then
		echo "FAIL: $mode: helm template failed" >&2
		cat "$rendered.err" >&2
		rm -f "$rendered" "$rendered.err"
		return 1
	fi
	local rc=0
	compare "$rendered" "$mode" || rc=$?
	rm -f "$rendered" "$rendered.err"
	return "$rc"
}

check() {
	command -v helm >/dev/null 2>&1 || {
		echo "FAIL: helm is not installed; this gate renders the chart and cannot be skipped silently" >&2
		return 2
	}
	[ -f "$CHART/Chart.yaml" ] || {
		echo "FAIL: $CHART not found; run from the repo root" >&2
		return 2
	}
	local fail=0
	# Every mode the chart renders a control-plane Service in. Split and the
	# multi-replica shapes need the logs PVC off: the chart refuses more than one
	# mounter on a ReadWriteOnce volume.
	local -a ha=(--set logs.persistence.enabled=false --set logs.sink.provider=s3 --set logs.sink.bucket=leoflow-logs)
	render_and_compare "default (non-split, replicaCount=1)" || fail=1
	render_and_compare "replicaCount=3" --set replicaCount=3 "${ha[@]}" || fail=1
	render_and_compare "autoscaling (HPA floor 2)" --set autoscaling.enabled=true "${ha[@]}" || fail=1
	render_and_compare "split mode (api + scheduler)" --set split.enabled=true "${ha[@]}" || fail=1
	# The PodDisruptionBudget is tri-state and can be forced on at one replica;
	# that is the shape where a budget covers the hook pod on a single-replica
	# install, so render it too.
	render_and_compare "podDisruptionBudget forced on at one replica" --set podDisruptionBudget.enabled=true || fail=1
	return "$fail"
}

self_test() {
	local fail=0 tmp
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN

	_case() { # <name> <want-exit> <want-substr> <manifest>
		local name=$1 want=$2 substr=$3 manifest=$4 out rc
		printf '%s' "$manifest" >"$tmp/m.yaml"
		out=$(compare "$tmp/m.yaml" "self-test" 2>&1) && rc=0 || rc=$?
		if [ "$rc" -ne "$want" ]; then
			echo "self-test FAIL: $name — exit $rc, wanted $want"
			echo "  $out"
			fail=1
			return
		fi
		case "$out" in
		*"$substr"*) ;;
		*)
			echo "self-test FAIL: $name — output lacks \"$substr\""
			echo "  $out"
			fail=1
			;;
		esac
	}

	# The shape #1055 shipped: identical maps.
	_case "identical maps are caught" 1 "is a SUBSET of" '
kind: Service
metadata: {name: leoflow}
spec:
  selector: {app.kubernetes.io/name: leoflow, app.kubernetes.io/instance: rel}
---
kind: Job
metadata: {name: leoflow-migrate}
spec:
  template:
    metadata:
      labels: {app.kubernetes.io/name: leoflow, app.kubernetes.io/instance: rel}
'

	# The fix proposed on the issue — add a distinguishing label and keep the
	# shared set — does NOT work, and this gate has to say so. A selector matches
	# a SUPERSET. If this case ever passes, the comparison has been weakened into
	# equality and the gate no longer means anything.
	_case "a merely-extra label does not save it" 1 "is a SUBSET of" '
kind: Service
metadata: {name: leoflow}
spec:
  selector: {app.kubernetes.io/name: leoflow, app.kubernetes.io/instance: rel}
---
kind: Job
metadata: {name: leoflow-migrate}
spec:
  template:
    metadata:
      labels:
        app.kubernetes.io/name: leoflow
        app.kubernetes.io/instance: rel
        app.kubernetes.io/component: migrate
'

	# Differing on a key the selector carries is what actually excludes the pod.
	_case "differing on a selector key passes" 0 "no Service/PDB selector is a subset" '
kind: Service
metadata: {name: leoflow}
spec:
  selector: {app.kubernetes.io/name: leoflow, app.kubernetes.io/instance: rel}
---
kind: Job
metadata: {name: leoflow-migrate}
spec:
  template:
    metadata:
      labels:
        app.kubernetes.io/name: leoflow-migrate
        app.kubernetes.io/instance: rel
        app.kubernetes.io/component: migrate
'

	# A PodDisruptionBudget selects pods the same way; a budget over a hook pod
	# refuses evictions on behalf of a container that serves nothing.
	_case "a PodDisruptionBudget selector is checked too" 1 "PodDisruptionBudget/leoflow" '
kind: PodDisruptionBudget
metadata: {name: leoflow}
spec:
  selector:
    matchLabels: {app.kubernetes.io/name: leoflow, app.kubernetes.io/instance: rel}
---
kind: Job
metadata: {name: leoflow-migrate}
spec:
  template:
    metadata:
      labels: {app.kubernetes.io/name: leoflow, app.kubernetes.io/instance: rel}
'

	# A different role's selector must not raise a false positive: split-mode
	# api/scheduler selectors carry a component the Job never has.
	_case "a component-scoped selector is not a false positive" 0 "no Service/PDB selector is a subset" '
kind: Service
metadata: {name: leoflow-api}
spec:
  selector:
    app.kubernetes.io/name: leoflow
    app.kubernetes.io/instance: rel
    app.kubernetes.io/component: api
---
kind: Job
metadata: {name: leoflow-migrate}
spec:
  template:
    metadata:
      labels:
        app.kubernetes.io/name: leoflow-migrate
        app.kubernetes.io/instance: rel
        app.kubernetes.io/component: migrate
'

	# The three ways this gate could pass by measuring nothing. Each must FAIL
	# loudly instead: an empty filter output is not a pass.
	_case "no Job at all is a failure, not a pass" 1 "no Job rendered" '
kind: Service
metadata: {name: leoflow}
spec:
  selector: {app.kubernetes.io/name: leoflow}
'
	_case "nothing selecting is a failure, not a pass" 1 "no left-hand side" '
kind: Job
metadata: {name: leoflow-migrate}
spec:
  template:
    metadata:
      labels: {app.kubernetes.io/name: leoflow-migrate}
'
	_case "an unlabelled hook pod is a failure, not a pass" 1 "no labels at all" '
kind: Service
metadata: {name: leoflow}
spec:
  selector: {app.kubernetes.io/name: leoflow}
---
kind: Job
metadata: {name: leoflow-migrate}
spec:
  template:
    metadata: {}
'

	# An empty selector means opposite things per kind, and skipping both was a
	# hole: for a Service it is correct, but a PodDisruptionBudget with
	# `selector: {}` matches EVERY pod in the namespace. Skipping it let the
	# gate report OK having compared nothing — the "passes while measuring
	# nothing" shape this repo has shipped twice.
	_case "an empty PDB selector matches everything, and is caught" 1 "matches every pod" '
kind: PodDisruptionBudget
metadata: {name: leoflow}
spec:
  selector: {}
---
kind: Job
metadata: {name: leoflow-migrate}
spec:
  template:
    metadata:
      labels: {app.kubernetes.io/name: leoflow-migrate}
'

	# A Service with no selector is legitimately skipped — but then nothing was
	# compared, and an OK would mean nothing was measured.
	_case "zero comparisons is a failure, not a pass" 1 "zero selector/pod comparisons" '
kind: Service
metadata: {name: leoflow}
spec:
  selector: {}
---
kind: Job
metadata: {name: leoflow-migrate}
spec:
  template:
    metadata:
      labels: {app.kubernetes.io/name: leoflow-migrate}
'

	if [ "$fail" -eq 0 ]; then
		echo "check-migrate-pod-selection self-test: ok"
		return 0
	fi
	return 1
}

[ "${1:-}" = "--self-test" ] && {
	self_test
	exit $?
}

cd "$(dirname "$0")/.."
if [ $# -ge 1 ]; then
	compare "$1" "${2:-$1}"
	exit $?
fi
check
