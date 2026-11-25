package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func at(minute int, sec float64) time.Time {
	base := time.Date(2026, 11, 29, 12, 0, 0, 0, time.UTC)
	return base.Add(time.Duration(minute)*time.Minute + time.Duration(sec*float64(time.Second)))
}

func startedRun(slot int, create, start float64) dagRun {
	s := at(slot, start)
	return dagRun{RunAfter: at(slot, 0), QueuedAt: at(slot, create), StartDate: &s, RunType: "scheduled"}
}

func TestInWindowKeepsScheduledRunsOfTheWindow(t *testing.T) {
	manual := startedRun(1, 0, 0)
	manual.RunType = "manual"
	runs := []dagRun{startedRun(0, 0, 0), startedRun(1, 0, 0), startedRun(2, 0, 0), manual}
	got := inWindow(runs, at(1, 0), at(2, 0))
	if len(got) != 1 || !got[0].RunAfter.Equal(at(1, 0)) {
		t.Fatalf("inWindow = %+v, want only the scheduled slot at minute 1", got)
	}
}

func TestPercentileIsNearestRank(t *testing.T) {
	vals := make([]float64, 100)
	for i := range vals {
		vals[i] = float64(i + 1)
	}
	if p := percentile(vals, 0.99); p != 99 {
		t.Fatalf("p99 of 1..100 = %v, want 99", p)
	}
	if p := percentile(vals, 0.50); p != 50 {
		t.Fatalf("p50 of 1..100 = %v, want 50", p)
	}
	if p := percentile([]float64{7}, 0.99); p != 7 {
		t.Fatalf("p99 of one value = %v, want it", p)
	}
	if p := percentile(nil, 0.99); p != 0 {
		t.Fatalf("p99 of nothing = %v, want 0", p)
	}
}

func TestJudge(t *testing.T) {
	base := summarize("baseline", []dagRun{startedRun(0, 0.4, 1.0), startedRun(1, 0.5, 1.2)})
	cases := map[string]struct {
		noise []dagRun
		pass  bool
	}{
		"unchanged":            {[]dagRun{startedRun(20, 0.5, 1.1), startedRun(21, 0.6, 1.3)}, true},
		"start moved":          {[]dagRun{startedRun(20, 0.5, 1.1), startedRun(21, 0.6, 4.0)}, false},
		"creation moved":       {[]dagRun{startedRun(20, 3.0, 3.5), startedRun(21, 0.6, 1.3)}, false},
		"no run in the window": {nil, false},
		"a run never started":  {[]dagRun{startedRun(20, 0.5, 1.1), {RunAfter: at(21, 0), QueuedAt: at(21, 0.5), RunType: "scheduled"}}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			v := judge(base, summarize("noise", c.noise), 2*time.Second)
			if v.pass != c.pass {
				t.Fatalf("judge = %+v, want pass %v", v, c.pass)
			}
		})
	}
}

func TestScheduledRunsPagesAndSendsTheToken(t *testing.T) {
	const total = 1200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer quiet" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("run_type") != "scheduled" {
			t.Errorf("query %q does not filter scheduled runs", r.URL.RawQuery)
		}
		var off, lim int
		_, _ = fmt.Sscan(r.URL.Query().Get("offset"), &off)
		_, _ = fmt.Sscan(r.URL.Query().Get("limit"), &lim)
		n := min(lim, total-off)
		runs := make([]dagRun, max(n, 0))
		for i := range runs {
			runs[i] = startedRun(off+i, 0, 0)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"dag_runs": runs, "total_entries": total})
	}))
	defer srv.Close()
	c := &client{base: srv.URL, token: "quiet", http: srv.Client()}
	runs, err := c.scheduledRuns(context.Background(), "quiet_cron")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != total {
		t.Fatalf("got %d runs, want %d", len(runs), total)
	}
}

func TestParseFlagsNeedsBothTokens(t *testing.T) {
	t.Setenv("QUIET_TOKEN", "q")
	t.Setenv("NOISY_TOKEN", "")
	if _, err := parseFlags([]string{"--url", "http://x"}); err == nil {
		t.Fatal("parseFlags accepted a missing NOISY_TOKEN")
	}
	t.Setenv("NOISY_TOKEN", "n")
	o, err := parseFlags([]string{"--url", "http://x", "--quiet-dags", "a, b,"})
	if err != nil {
		t.Fatal(err)
	}
	if len(o.quietDags) != 2 || o.quietDags[1] != "b" {
		t.Fatalf("quiet dags = %q, want [a b]", o.quietDags)
	}
}
