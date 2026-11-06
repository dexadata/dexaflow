#!/usr/bin/env bash
# Render the chart's PrometheusRule and test it with promtool (#2298).
#
# helm-unittest pins what the template renders; it cannot say whether a rule
# fires. This renders templates/prometheusrule.yaml in namespace "data", takes
# spec (the plain Prometheus rule groups) into rules.yaml beside the promtool
# tests under helm/dexaflow/tests/promtool/, then runs `promtool check rules`
# and `promtool test rules` on them: every alert has a case that fires and one
# that does not.
#
# Needs helm, promtool and python3 with PyYAML. Run by the lint job of
# .github/workflows/helm-ci.yaml.
#
# Usage: scripts/prometheus-rules-test.sh
set -euo pipefail

CHART="helm/dexaflow"
TESTS="$CHART/tests/promtool"

for tool in helm promtool python3; do
	command -v "$tool" >/dev/null || { echo "FAIL: $tool is required" >&2; exit 1; }
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The chart refuses to render without these; they are fixtures, not credentials.
helm template dexaflow "$CHART" --namespace data \
	--set metrics.prometheusRule.enabled=true \
	--set 'database.url=postgres://leoflow:p@db:5432/leoflow?sslmode=disable' \
	--set 'redis.url=redis://r:6379/0' \
	--set 'auth.jwtSecret=prometheus-rules-test-fixture' \
	--show-only templates/prometheusrule.yaml >"$work/rendered.yaml"

python3 - "$work/rendered.yaml" "$work/rules.yaml" <<'PY'
import sys

try:
	import yaml
except ImportError:
	sys.exit("FAIL: PyYAML is required (python3 -m pip install --user pyyaml)")

with open(sys.argv[1]) as f:
	doc = yaml.safe_load(f)
if not doc or doc.get("kind") != "PrometheusRule":
	sys.exit("FAIL: templates/prometheusrule.yaml did not render a PrometheusRule")
with open(sys.argv[2], "w") as f:
	yaml.safe_dump(doc["spec"], f, sort_keys=False)
PY

cp "$TESTS"/*.promtool.yaml "$work/"
promtool check rules "$work/rules.yaml"

# A rule without a test passes promtool silently, so every alert must be named
# by at least one alert_rule_test.
python3 - "$work" <<'PY'
import glob, os, sys
import yaml

work = sys.argv[1]
with open(os.path.join(work, "rules.yaml")) as f:
	alerts = {r["alert"] for g in yaml.safe_load(f)["groups"] for r in g["rules"] if "alert" in r}
tested = set()
for path in glob.glob(os.path.join(work, "*.promtool.yaml")):
	with open(path) as f:
		for t in yaml.safe_load(f)["tests"]:
			tested |= {a["alertname"] for a in t.get("alert_rule_test", [])}
missing = sorted(alerts - tested)
if missing:
	sys.exit("FAIL: alerts without a promtool test: " + ", ".join(missing))
print(f"ok: {len(alerts)} alerts, each with a promtool test")
PY

(cd "$work" && promtool test rules ./*.promtool.yaml)
