package main

import (
	"net/url"
	"strings"
	"testing"
)

// Every profile names only the DAG and runs of its target, escaped, and the
// grid summaries ask for each shown run.
func TestProfilesNameTheirTarget(t *testing.T) {
	tg := target{dag: "my dag", run: "manual__2026-10-23T04:00:00", runIDs: []string{"r2", "r1"}}
	for _, name := range profileNames() {
		reqs, err := profileRequests(name, tg)
		if err != nil || len(reqs) == 0 {
			t.Fatalf("%s: %v, %d requests", name, err, len(reqs))
		}
		for _, r := range reqs {
			if !strings.HasPrefix(r, "/") || strings.Contains(r, " ") {
				t.Errorf("%s: request %q is not an escaped absolute path", name, r)
			}
			if !strings.Contains(r, "my%20dag") {
				t.Errorf("%s: request %q does not name the DAG", name, r)
			}
			if strings.Contains(r, "/ti_summaries/") {
				u, err := url.Parse(r)
				if err != nil || strings.Join(u.Query()["run_ids"], ",") != "r2,r1" {
					t.Errorf("%s: summaries request %q does not list the shown runs", name, r)
				}
			}
		}
	}
	if _, err := profileRequests("nope", tg); err == nil {
		t.Error("an unknown profile was accepted")
	}
}

func TestParseInts(t *testing.T) {
	got, err := parseInts(" 1, 10 ,50,")
	if err != nil || len(got) != 3 || got[0] != 1 || got[1] != 10 || got[2] != 50 {
		t.Fatalf("parseInts = %v, %v", got, err)
	}
	for _, bad := range []string{"", "0", "-1", "a", ","} {
		if _, err := parseInts(bad); err == nil {
			t.Errorf("parseInts(%q) accepted", bad)
		}
	}
}

func TestPercentile(t *testing.T) {
	s := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if p := percentile(s, 0.5); p != 5 {
		t.Errorf("p50 = %v, want 5", p)
	}
	if p := percentile(s, 0.99); p != 9 {
		t.Errorf("p99 = %v, want 9", p)
	}
	if p := percentile(nil, 0.5); p != 0 {
		t.Errorf("p50 of nothing = %v", p)
	}
}
