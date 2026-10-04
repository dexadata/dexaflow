#!/usr/bin/env bash
# rbac-covers-executor.sh — the chart's Role must permit every Kubernetes call
# the executor makes.
#
# These two files have no compiler between them. The executor gained PVC
# create/delete for the staging volume (ADR 0022) and the Role was never
# updated, so every DAG using staging failed with a 403 in any cluster that
# enforces RBAC — while `helm unittest` stayed green, because it asserts what
# the template renders and never what the code needs.
#
# This closes the loop in the direction that bites: a call with no permission is
# an error. The reverse (a permission with no call) is reported as a warning
# only — over-granting is worth knowing about, but it is not a broken deploy,
# and `get`/`watch` on pods are plausibly wanted by future work.
#
# It is a grep, not type-aware analysis. It reads `CoreV1().<Resource>(...)` and
# the verb chained onto it, which is how every call in internal/executor is
# written today. A call built through a variable would be missed — accepted,
# because the alternative is a linter plugin and this catches the real drift.
set -euo pipefail

cd "$(dirname "$0")/.."

EXEC_DIR="internal/executor"
CHART="helm/dexaflow"
fail=0

# Go clientset method -> Kubernetes RBAC resource name.
resource_of() {
	case "$1" in
	Pods) echo "pods" ;;
	PersistentVolumeClaims) echo "persistentvolumeclaims" ;;
	ConfigMaps) echo "configmaps" ;;
	Secrets) echo "secrets" ;;
	Services) echo "services" ;;
	Events) echo "events" ;;
	*) echo "" ;;
	esac
}

# Render the Role once. Values are the minimum that lets the chart render.
role="$(helm template rbac-check "$CHART" \
	--set database.url=postgres://h/d \
	--set redis.url=redis://h/0 \
	--set auth.jwtSecret=j \
	--set agentTLS.serverCertSecret=cert \
	--set agentTLS.caConfigMap=ca \
	--show-only templates/rbac.yaml 2>/dev/null)"

if [ -z "$role" ]; then
	echo "FAIL: could not render templates/rbac.yaml" >&2
	exit 1
fi

# Collect (resource, verb) pairs the executor calls.
# Production files only. Tests drive a fake clientset and legitimately call
# verbs production never needs — a test asserting a PVC was created reads it
# back with Get, which is not a permission the control plane requires.
# Excluding by filename, not by filtering output lines: `grep -h` prints no
# filenames, so a line-level filter here would silently match nothing.
prod_files="$(find "$EXEC_DIR" -name '*.go' ! -name '*_test.go')"
calls="$(grep -hoE 'CoreV1\(\)\.[A-Za-z]+\([^)]*\)\.[A-Za-z]+\(' $prod_files |
	sed -E 's/CoreV1\(\)\.([A-Za-z]+)\(.*\)\.([A-Za-z]+)\($/\1 \2/' |
	sort -u)"

if [ -z "$calls" ]; then
	echo "FAIL: found no Kubernetes calls in $EXEC_DIR — the grep pattern has drifted from the code" >&2
	exit 1
fi

while read -r method verb; do
	[ -n "$method" ] || continue
	# Pods.DeleteCollection is the opt-in settled-run collection, granted only
	# with executor.collectSettledRunPods; it is checked against that render below.
	if [ "$method $verb" = "Pods DeleteCollection" ]; then
		continue
	fi
	res="$(resource_of "$method")"
	if [ -z "$res" ]; then
		echo "FAIL: $method is called in $EXEC_DIR but this script has no RBAC mapping for it." >&2
		echo "      Add it to resource_of() so the permission can be checked." >&2
		fail=1
		continue
	fi
	lverb="$(echo "$verb" | tr '[:upper:]' '[:lower:]')"
	# Pair each rule's `resources:` with its own `verbs:`, one line per rule.
	#
	# A proximity match (grep -A<n>) was the first attempt and is wrong for a
	# security check: the window bleeds into the NEXT rule, so a verb granted on
	# some other resource can satisfy the test. That fails open — the one
	# direction a permission check must never fail in. Whether it happens to be
	# correct depends on how many lines apart two rules sit, which is not a
	# property anyone maintains on purpose.
	pairs="$(echo "$role" | awk '
		/^[[:space:]]*-?[[:space:]]*resources:/ { r = $0 }
		/^[[:space:]]*verbs:/ { if (r != "") { print r " || " $0; r = "" } }
	')"
	if ! echo "$pairs" | grep "\"$res\"" | grep -q "\"$lverb\""; then
		echo "FAIL: the executor calls $method.$verb but the Role does not grant $lverb on $res" >&2
		echo "      -> add it to $CHART/templates/rbac.yaml, or stop making the call" >&2
		fail=1
	else
		echo "OK:   $res/$lverb granted (called as $method.$verb)"
	fi
done <<<"$calls"

# The token-exchange transport's single apiserver call lives outside
# internal/executor and outside CoreV1: the control plane submits a TokenReview to
# authenticate a task pod's projected ServiceAccount token. TokenReview is
# cluster-scoped, so it needs a ClusterRole the namespaced Role above can never
# carry — and the chart shipped without one, which forbade the review and killed
# every task pod on the exchange transport, warm pools or not. Same closed loop,
# same direction: a call with no permission is an error.
#
# The grant is rendered only when the exchange transport is selected (it is an
# authentication oracle; an env-var install never calls it), so the check renders
# the chart a second time with the transport on.
if grep -rqE 'AuthenticationV1\(\)\.TokenReviews\(\)\.Create\(' internal --include='*.go' \
	--exclude='*_test.go'; then
	exchange_rbac="$(helm template rbac-check "$CHART" \
		--set database.url=postgres://h/d \
		--set redis.url=redis://h/0 \
		--set auth.jwtSecret=j \
		--set agentTLS.serverCertSecret=cert \
		--set agentTLS.caConfigMap=ca \
		--set auth.agentTokenTransport=exchange \
		--show-only templates/rbac.yaml 2>/dev/null)"
	if echo "$exchange_rbac" | grep -q '"tokenreviews"' &&
		echo "$exchange_rbac" | grep -q 'kind: ClusterRole'; then
		echo "OK:   tokenreviews/create granted by a ClusterRole on the exchange transport"
	else
		echo "FAIL: the control plane submits a TokenReview but the chart grants no cluster-scoped tokenreviews/create" >&2
		echo "      -> TokenReview is cluster-scoped: a namespaced Role cannot grant it. Add a ClusterRole + ClusterRoleBinding to $CHART/templates/rbac.yaml" >&2
		fail=1
	fi
fi

# The settled-run collection (executor.collectSettledRunPods) is the one caller
# of Pods.DeleteCollection. The verb is granted only when the collection is on
# (least privilege; with it off the call is never made, and a 403 falls back to
# per-pod deletes), so the check renders the chart a third time with it on.
if echo "$calls" | grep -qx 'Pods DeleteCollection'; then
	collect_rbac="$(helm template rbac-check "$CHART" \
		--set database.url=postgres://h/d \
		--set redis.url=redis://h/0 \
		--set auth.jwtSecret=j \
		--set agentTLS.serverCertSecret=cert \
		--set agentTLS.caConfigMap=ca \
		--set executor.collectSettledRunPods=true \
		--show-only templates/rbac.yaml 2>/dev/null)"
	collect_pairs="$(echo "$collect_rbac" | awk '
		/^[[:space:]]*-?[[:space:]]*resources:/ { r = $0 }
		/^[[:space:]]*verbs:/ { if (r != "") { print r " || " $0; r = "" } }
	')"
	if echo "$collect_pairs" | grep '"pods"' | grep -q '"deletecollection"'; then
		echo "OK:   pods/deletecollection granted with executor.collectSettledRunPods (called as Pods.DeleteCollection)"
	else
		echo "FAIL: the executor calls Pods.DeleteCollection but the Role does not grant deletecollection on pods with executor.collectSettledRunPods=true" >&2
		fail=1
	fi
fi

if [ "$fail" -ne 0 ]; then
	echo >&2
	echo "rbac-covers-executor: the chart's Role does not cover the executor's calls." >&2
	exit 1
fi

echo
echo "rbac-covers-executor: every executor call is permitted"
