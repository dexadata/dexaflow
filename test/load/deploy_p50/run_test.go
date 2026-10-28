package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCLI writes a stand-in for the dexaflow binary: `init DIR` scaffolds a
// project as the real one does, and `deploy` logs its arguments and the
// marker line of dag.py, then reports a registered version on stdout and
// the builder line on stderr: the dependency-layer path on the first deploy
// and the code-layer path after it, as a cold registry gives.
func fakeCLI(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	bin = filepath.Join(dir, "dexaflow")
	script := `#!/bin/sh
set -e
log="` + log + `"
case "$1" in
init)
  mkdir -p "$2"
  printf 'from airflow.sdk import DAG\n' > "$2/dag.py"
  printf 'dag_id: scaffold\ndependencies: []\n' > "$2/dexaflow.yaml"
  ;;
deploy)
  n=$(grep -c '^deploy' "$log" 2>/dev/null || true)
  echo "$*|$(grep '^# deploy_p50' "$2/dag.py" || true)" >> "$log"
  if [ "${n:-0}" -eq 0 ]; then
    echo "builder:    dependency-layer (uv 0.11.32), then code-layer, no dependency image for this key" >&2
  else
    echo "builder:    code-layer, the registry holds the dependency image reg/p50:deps-1" >&2
  fi
  printf '{"dags":[{"dag_id":"p50","status":"registered","version":"v%s","image":"reg/p50@sha256:%s"}]}\n' "$n" "$n"
  ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { //nolint:gosec // an executable test fixture
		t.Fatal(err)
	}
	return bin, log
}

// The measurement scaffolds the project with the CLI, writes its
// dexaflow.yaml, discards the cold first deploy, and times n deploys that
// each change only dag.py, passing the deploy flags through.
func TestMeasureDrivesTheCLIWithCodeOnlyChanges(t *testing.T) {
	bin, log := fakeCLI(t)
	work := t.TempDir()
	var out bytes.Buffer
	cfg := config{cli: bin, workDir: work, dagID: "p50", registry: "reg.example/team", platform: "linux/amd64",
		python: "3.12", dependencies: []string{"requests==2.32.3"}, n: 3, target: 30 * time.Second,
		cliConfig: "/tmp/cli.yaml", deployArgs: []string{"--active"}, stamp: "s1"}
	var served int64
	// Each read moves the count on by 7, so each deploy reads 7 requests.
	cfg.requests = func() int64 { served += 7; return served }
	rep, err := measure(context.Background(), cfg, &out, nil)
	if err != nil {
		t.Fatalf("measure: %v\n%s", err, out.String())
	}
	yaml, err := os.ReadFile(filepath.Join(work, "p50", "dexaflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dag_id: p50", "url: reg.example/team", "image_name: p50", "- linux/amd64", `python_version: "3.12"`, "- requests==2.32.3"} {
		if !strings.Contains(string(yaml), want) {
			t.Errorf("dexaflow.yaml lacks %q:\n%s", want, yaml)
		}
	}
	if strings.Contains(string(yaml), "min_idle_workers") {
		t.Errorf("min_idle_workers declared without --warm:\n%s", yaml)
	}
	raw, err := os.ReadFile(log) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(calls) != 4 {
		t.Fatalf("%d deploys, want the cold one and 3 measured:\n%s", len(calls), raw)
	}
	markers := map[string]bool{}
	for _, c := range calls {
		args, marker, _ := strings.Cut(c, "|")
		want := "deploy " + filepath.Join(work, "p50") + " --yes --output json --config /tmp/cli.yaml --active"
		if args != want {
			t.Errorf("deploy args = %q, want %q", args, want)
		}
		if marker == "" || markers[marker] {
			t.Errorf("deploy did not see a new code-only change: %q", marker)
		}
		markers[marker] = true
	}
	if rep.cold.builder == "" || codeOnly(rep.cold.builder) {
		t.Errorf("cold deploy = %+v, want its dependency-layer builder line", rep.cold)
	}
	if len(rep.deploys) != 3 {
		t.Fatalf("measured %d deploys, want 3", len(rep.deploys))
	}
	for i, d := range rep.deploys {
		if !codeOnly(d.builder) || d.outcome.Version != "v"+string(rune('1'+i)) || d.wall <= 0 || d.requests != 7 {
			t.Errorf("deploy %d = %+v", i+1, d)
		}
	}
	if ok, why := rep.verdict(cfg); !ok {
		t.Errorf("verdict failed: %s", why)
	}
	if !strings.Contains(out.String(), "| 3 |") {
		t.Errorf("the table does not list the third deploy:\n%s", out.String())
	}
}

// With --warm, the project declares a warm floor so the new version is
// prewarmed (ADR 0082), and a deploy that fails stops the measurement with
// its reason.
func TestMeasureWarmDeclaresMinIdleWorkers(t *testing.T) {
	bin, log := fakeCLI(t)
	work := t.TempDir()
	cfg := config{cli: bin, workDir: work, dagID: "p50", registry: "reg", platform: "linux/amd64", python: "3.12",
		n: 1, target: time.Minute, warm: true, stamp: "s2"}
	watch := func(context.Context, string, time.Time) (warmReading, error) {
		return warmReading{Pod: "w", Seen: time.Second, Ready: 2 * time.Second}, nil
	}
	rep, err := measure(context.Background(), cfg, &bytes.Buffer{}, watch)
	if err != nil {
		t.Fatal(err)
	}
	yaml, _ := os.ReadFile(filepath.Join(work, "p50", "dexaflow.yaml")) //nolint:gosec // the test's own file
	if !strings.Contains(string(yaml), "min_idle_workers: 1") {
		t.Errorf("--warm does not declare min_idle_workers:\n%s", yaml)
	}
	if len(rep.deploys) != 1 || rep.deploys[0].warm == nil || rep.deploys[0].warm.Ready != 2*time.Second {
		t.Errorf("warm reading not recorded: %+v", rep.deploys)
	}
	raw, _ := os.ReadFile(log) //nolint:gosec // the test's own file
	for _, c := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		// Only an unpaused DAG is prewarmed (ADR 0082).
		if args, _, _ := strings.Cut(c, "|"); !strings.HasSuffix(args, " --active") {
			t.Errorf("a --warm deploy does not leave the DAG active: %q", args)
		}
	}
}

// The flags that cannot work together are refused before anything runs.
func TestCheckFlags(t *testing.T) {
	ok := flags{cfg: config{n: 1, registry: "reg"}}
	if err := checkFlags(ok); err != nil {
		t.Errorf("valid flags refused: %v", err)
	}
	cases := map[string]flags{
		"no deploy":              {cfg: config{n: 0, registry: "reg"}},
		"nowhere to push":        {cfg: config{n: 1}},
		"warm on the loopback":   {cfg: config{n: 1, warm: true}, localRegistry: true},
		"two registries at once": {cfg: config{n: 1, registry: "reg"}, localRegistry: true},
	}
	for name, f := range cases {
		if err := checkFlags(f); err == nil {
			t.Errorf("%s: not refused", name)
		}
	}
	if err := checkFlags(flags{cfg: config{n: 1, project: "p"}}); err != nil {
		t.Errorf("a project with its own registry refused: %v", err)
	}
}
