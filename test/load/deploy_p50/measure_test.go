package main

import (
	"strings"
	"testing"
	"time"
)

func TestSummarizeTakesTheMedianAsP50(t *testing.T) {
	ds := []time.Duration{9 * time.Second, 1 * time.Second, 3 * time.Second, 7 * time.Second}
	got := summarize(ds)
	if got.N != 4 {
		t.Errorf("N = %d, want 4", got.N)
	}
	// The median of an even count is the mean of the two middle values.
	if got.P50 != 5*time.Second {
		t.Errorf("P50 = %s, want 5s", got.P50)
	}
	if got.Max != 9*time.Second {
		t.Errorf("Max = %s, want 9s", got.Max)
	}
	// p95 of 4 values interpolates between the 3rd and the 4th: 7 + 0.85*2.
	if got.P95 != 8700*time.Millisecond {
		t.Errorf("P95 = %s, want 8.7s", got.P95)
	}
	if ds[0] != 9*time.Second {
		t.Errorf("summarize sorted its input in place: %v", ds)
	}
}

func TestSummarizeOfOneAndOfNone(t *testing.T) {
	one := summarize([]time.Duration{2 * time.Second})
	if one.P50 != 2*time.Second || one.P95 != 2*time.Second || one.Max != 2*time.Second {
		t.Errorf("one value: %+v", one)
	}
	if none := summarize(nil); none.N != 0 || none.P50 != 0 {
		t.Errorf("no value: %+v", none)
	}
}

func TestCodeOnlyEditKeepsOneMarkerLine(t *testing.T) {
	src := []byte("import x\n\nx.run()\n")
	first := codeOnlyEdit(src, "a")
	if string(first) != "import x\n\nx.run()\n# deploy_p50: a\n" {
		t.Fatalf("first edit = %q", first)
	}
	second := codeOnlyEdit(first, "b")
	if string(second) != "import x\n\nx.run()\n# deploy_p50: b\n" {
		t.Errorf("second edit = %q, want the marker replaced", second)
	}
	// A source without a final newline still gets the marker on its own line.
	if got := string(codeOnlyEdit([]byte("x.run()"), "c")); got != "x.run()\n# deploy_p50: c\n" {
		t.Errorf("no final newline: %q", got)
	}
}

func TestParseDeployReadsTheRegisteredDAG(t *testing.T) {
	out := []byte(`{"server":"http://s","dags":[{"dag_id":"p50","status":"registered","version":"dev-1","image":"r/p50@sha256:ab","digest":"sha256:ab","created":true,"dag_created":false}]}`)
	got, err := parseDeploy(out)
	if err != nil {
		t.Fatal(err)
	}
	if got.DagID != "p50" || got.Version != "dev-1" || got.Image != "r/p50@sha256:ab" {
		t.Errorf("parsed %+v", got)
	}
}

func TestParseDeployRefusesWhatIsNotANewVersion(t *testing.T) {
	cases := map[string]string{
		"unchanged": `{"dags":[{"dag_id":"p50","status":"unchanged","version":"v"}]}`,
		"failed":    `{"dags":[{"dag_id":"p50","status":"failed","error":"push denied"}]}`,
		"none":      `{"dags":[]}`,
		"two":       `{"dags":[{"dag_id":"a","status":"registered"},{"dag_id":"b","status":"registered"}]}`,
		"not json":  `Deployed p50`,
	}
	for name, out := range cases {
		if _, err := parseDeploy([]byte(out)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	_, err := parseDeploy([]byte(cases["failed"]))
	if err == nil || !strings.Contains(err.Error(), "push denied") {
		t.Errorf("a failed deploy's error does not carry its reason: %v", err)
	}
}

func TestBuilderLine(t *testing.T) {
	stderr := "settings:   x\nbuilder:    code-layer, the registry holds the dependency image r/p50:deps-1\nDeployed p50\n"
	line, ok := builderLine(stderr)
	if !ok || line != "code-layer, the registry holds the dependency image r/p50:deps-1" {
		t.Fatalf("builderLine = %q, %v", line, ok)
	}
	if !codeOnly(line) {
		t.Errorf("%q is a code-only build", line)
	}
	cold := "local-daemon (docker) for the dependency image, then code-layer, no dependency image for this key"
	if codeOnly(cold) {
		t.Errorf("%q builds the dependency image, so it is not code-only", cold)
	}
	if codeOnly("dependency-layer (uv 0.11.32), then code-layer, no dependency image") {
		t.Error("a dependency-layer build is not code-only")
	}
	if _, ok := builderLine("Deployed p50\n"); ok {
		t.Error("found a builder line in output that has none")
	}
}

func TestPullTime(t *testing.T) {
	cases := map[string]time.Duration{
		`Successfully pulled image "r/p50@sha256:ab" in 1.234s (1.234s including waiting). Image size: 52 bytes.`: 1234 * time.Millisecond,
		`Successfully pulled image "r/p50@sha256:ab" in 512ms (2.1s including waiting). Image size: 52 bytes.`:    512 * time.Millisecond,
		`Successfully pulled image "r/p50@sha256:ab" in 1m2.5s (1m2.5s including waiting)`:                        62500 * time.Millisecond,
		`Container image "r/p50@sha256:ab" already present on machine`:                                            0,
	}
	for msg, want := range cases {
		got, ok := pullTime(msg)
		if !ok || got != want {
			t.Errorf("pullTime(%q) = %s, %v; want %s", msg, got, ok, want)
		}
	}
	if _, ok := pullTime(`Pulling image "r/p50@sha256:ab"`); ok {
		t.Error("a pull that has not finished has no pull time")
	}
}

func TestVerdict(t *testing.T) {
	target := 30 * time.Second
	if ok, why := verdict(stats{N: 5, P50: 12 * time.Second}, target, nil); !ok {
		t.Errorf("12s against 30s failed: %s", why)
	}
	if ok, why := verdict(stats{N: 5, P50: 31 * time.Second}, target, nil); ok || !strings.Contains(why, "31s") {
		t.Errorf("31s against 30s: %v %q", ok, why)
	}
	notCode := []string{"deploy 3: dependency-layer (uv), then code-layer, no dependency image"}
	if ok, why := verdict(stats{N: 5, P50: time.Second}, target, notCode); ok || !strings.Contains(why, "deploy 3") {
		t.Errorf("a deploy off the code-layer path passed: %v %q", ok, why)
	}
	if ok, _ := verdict(stats{}, target, nil); ok {
		t.Error("no measured deploy passed")
	}
}

func TestBuilderPathDropsTheReason(t *testing.T) {
	cases := map[string]string{
		"code-layer, the registry holds the dependency image r:deps-1 for this build, so only the DAG code is pushed": "code-layer",
		"dependency-layer (uv 0.11.32), then code-layer, no dependency image r:deps-1 in the registry":                "dependency-layer (uv 0.11.32), then code-layer",
		"local-daemon (docker) for the dependency image, then code-layer, no dependency image":                        "local-daemon (docker) for the dependency image, then code-layer",
		"local-daemon (docker), no reusable image for this content in the registry":                                   "local-daemon (docker)",
		"skip": "skip",
	}
	for line, want := range cases {
		if got := builderPath(line); got != want {
			t.Errorf("builderPath(%q) = %q, want %q", line, got, want)
		}
	}
}
