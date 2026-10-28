package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

// markerPrefix starts the line codeOnlyEdit writes into dag.py.
const markerPrefix = "# deploy_p50: "

// stats summarizes a set of durations.
type stats struct {
	N   int
	P50 time.Duration
	P95 time.Duration
	Max time.Duration
}

// summarize returns the count, median, 95th percentile and maximum of ds,
// interpolating between the two nearest ranks (the median of an even count
// is the mean of its two middle values). It does not reorder ds.
func summarize(ds []time.Duration) stats {
	if len(ds) == 0 {
		return stats{}
	}
	sorted := slices.Clone(ds)
	slices.Sort(sorted)
	return stats{N: len(sorted), P50: quantile(sorted, 0.50), P95: quantile(sorted, 0.95), Max: sorted[len(sorted)-1]}
}

// quantile is the q quantile of the sorted, non-empty ds, linearly
// interpolated between the ranks around q*(n-1).
func quantile(sorted []time.Duration, q float64) time.Duration {
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	frac := pos - float64(lo)
	return sorted[lo] + time.Duration(math.Round(frac*float64(sorted[hi]-sorted[lo])))
}

// codeOnlyEdit returns src with one marker comment naming marker as its last
// line, replacing the marker a previous edit wrote. A comment changes the
// DAG code, and so the content label and the code layer, and nothing the
// dependency image is keyed on.
func codeOnlyEdit(src []byte, marker string) []byte {
	lines := strings.SplitAfter(string(src), "\n")
	var b strings.Builder
	for _, l := range lines {
		if strings.HasPrefix(l, markerPrefix) {
			continue
		}
		b.WriteString(l)
	}
	out := b.String()
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out + markerPrefix + marker + "\n")
}

// deployOutcome is the part of one DAG's entry in `dexaflow deploy --output
// json` the harness reads.
type deployOutcome struct {
	DagID   string `json:"dag_id"`
	Status  string `json:"status"`
	Version string `json:"version"`
	Image   string `json:"image"`
	Digest  string `json:"digest"`
	Error   string `json:"error"`
}

// parseDeploy reads the JSON document `dexaflow deploy --output json` prints
// and returns its only DAG, which must have registered a new version: a
// deploy that registered nothing measures nothing.
func parseDeploy(stdout []byte) (deployOutcome, error) {
	var doc struct {
		DAGs []deployOutcome `json:"dags"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &doc); err != nil {
		return deployOutcome{}, fmt.Errorf("reading the deploy report: %w", err)
	}
	if len(doc.DAGs) != 1 {
		return deployOutcome{}, fmt.Errorf("the deploy report lists %d DAGs, want 1", len(doc.DAGs))
	}
	d := doc.DAGs[0]
	if d.Status != "registered" {
		detail := ""
		if d.Error != "" {
			detail = ": " + d.Error
		}
		return d, fmt.Errorf("deploy of %s ended %q, not registered%s", d.DagID, d.Status, detail)
	}
	return d, nil
}

// builderLine returns what follows "builder:" in the deploy's human output:
// the builder path and why it was chosen.
func builderLine(stderr string) (string, bool) {
	for _, l := range strings.Split(stderr, "\n") {
		if rest, ok := strings.CutPrefix(l, "builder:"); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// codeOnly reports whether a builder line describes a build that reused the
// dependency image and appended the code layer only (ADR 0077 decision 3):
// the path a code-only deploy is meant to take.
func codeOnly(line string) bool {
	return strings.HasPrefix(line, "code-layer, ")
}

// builderPath is the builder a builder line names, without the reason it
// gives: up to the first ", ", or the second when the builder is two steps
// ("dependency-layer (uv 0.11.32), then code-layer").
func builderPath(line string) string {
	first, rest, ok := strings.Cut(line, ", ")
	if !ok {
		return line
	}
	if then, _, _ := strings.Cut(rest, ", "); strings.HasPrefix(then, "then ") {
		return first + ", " + then
	}
	return first
}

// pulledRe reads the duration out of the kubelet's Pulled event message.
var pulledRe = regexp.MustCompile(`pulled image "[^"]*" in ((?:[0-9.]+(?:ns|us|µs|ms|s|m|h))+)`)

// pullTime returns how long the kubelet took to pull an image, from the
// message of its Pulled event: zero when the image was already on the node.
func pullTime(msg string) (time.Duration, bool) {
	if strings.Contains(msg, "already present on machine") {
		return 0, true
	}
	m := pulledRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	d, err := time.ParseDuration(m[1])
	if err != nil {
		return 0, false
	}
	return d, true
}

// errNoDeploy is the verdict when nothing was measured.
var errNoDeploy = errors.New("no deploy was measured")

// verdict decides the measurement: the p50 of the code-only deploys is under
// target, and every measured deploy took the code-layer path (offPath lists
// the ones that did not).
func verdict(st stats, target time.Duration, offPath []string) (ok bool, why string) {
	switch {
	case st.N == 0:
		return false, errNoDeploy.Error()
	case len(offPath) > 0:
		return false, "not every measured deploy was code-only: " + strings.Join(offPath, "; ")
	case st.P50 >= target:
		return false, fmt.Sprintf("p50 %s is not under the %s target", st.P50.Round(10*time.Millisecond), target)
	}
	return true, fmt.Sprintf("p50 %s is under the %s target", st.P50.Round(10*time.Millisecond), target)
}
