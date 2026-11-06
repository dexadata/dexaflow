package main

import (
	"math"
	"strings"
	"testing"
)

const before = `# TYPE dexaflow_scheduler_schedule_delay_seconds histogram
dexaflow_scheduler_schedule_delay_seconds_bucket{le="60"} 10
dexaflow_scheduler_schedule_delay_seconds_bucket{le="+Inf"} 10
dexaflow_scheduler_schedule_delay_seconds_sum 1
dexaflow_scheduler_schedule_delay_seconds_count 10
# TYPE dexaflow_task_queue_wait_seconds histogram
dexaflow_task_queue_wait_seconds_bucket{tenant="a",le="120"} 100
dexaflow_task_queue_wait_seconds_bucket{tenant="a",le="+Inf"} 100
dexaflow_task_queue_wait_seconds_sum{tenant="a"} 1
dexaflow_task_queue_wait_seconds_count{tenant="a"} 100
# TYPE dexaflow_http_requests_total counter
dexaflow_http_requests_total{method="GET",path="/api",status="200"} 1000
dexaflow_http_requests_total{method="GET",path="/api",status="503"} 0
# TYPE dexaflow_task_attempts_total counter
dexaflow_task_attempts_total{tenant="a",state="success"} 100
# TYPE dexaflow_scheduler_decisions_total counter
dexaflow_scheduler_decisions_total{decision_type="pod_lost"} 0
dexaflow_scheduler_decisions_total{decision_type="pod_lost_gate_skip"} 0
`

// after: 100 more runs, 2 late; 1000 more attempts started, 5 late, the
// tenant b ones in a process that restarted; 10000 more requests, 5 5xx; 1000
// more attempts finished, 1 lost and 50 gate skips, which fail nothing.
const after = `# TYPE dexaflow_scheduler_schedule_delay_seconds histogram
dexaflow_scheduler_schedule_delay_seconds_bucket{le="60"} 108
dexaflow_scheduler_schedule_delay_seconds_bucket{le="+Inf"} 110
dexaflow_scheduler_schedule_delay_seconds_sum 1
dexaflow_scheduler_schedule_delay_seconds_count 110
# TYPE dexaflow_task_queue_wait_seconds histogram
dexaflow_task_queue_wait_seconds_bucket{tenant="a",le="120"} 595
dexaflow_task_queue_wait_seconds_bucket{tenant="a",le="+Inf"} 600
dexaflow_task_queue_wait_seconds_sum{tenant="a"} 1
dexaflow_task_queue_wait_seconds_count{tenant="a"} 600
dexaflow_task_queue_wait_seconds_bucket{tenant="b",le="120"} 500
dexaflow_task_queue_wait_seconds_bucket{tenant="b",le="+Inf"} 500
dexaflow_task_queue_wait_seconds_sum{tenant="b"} 1
dexaflow_task_queue_wait_seconds_count{tenant="b"} 500
# TYPE dexaflow_http_requests_total counter
dexaflow_http_requests_total{method="GET",path="/api",status="200"} 10995
dexaflow_http_requests_total{method="GET",path="/api",status="503"} 5
# TYPE dexaflow_task_attempts_total counter
dexaflow_task_attempts_total{tenant="a",state="success"} 1100
# TYPE dexaflow_scheduler_decisions_total counter
dexaflow_scheduler_decisions_total{decision_type="pod_lost"} 1
dexaflow_scheduler_decisions_total{decision_type="pod_lost_gate_skip"} 50
`

func mustParse(t *testing.T, text string) scrape {
	t.Helper()
	s, err := parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func byName(rows []objective) map[string]objective {
	m := map[string]objective{}
	for _, o := range rows {
		m[o.Name] = o
	}
	return m
}

func TestJudgeCountsGoodAndAllEvents(t *testing.T) {
	rows := byName(judge(increases([]scrape{mustParse(t, before), mustParse(t, after)}), 0.99, 0.99, 0.001, 0.999))
	want := map[string][2]float64{
		"schedule punctuality":     {98, 100},
		"task start":               {995, 1000},
		"API availability":         {9995, 10000},
		"platform-caused failures": {999, 1000},
	}
	for name, gw := range want {
		o := rows[name]
		if o.Good != gw[0] || o.All != gw[1] {
			t.Errorf("%s: good %v all %v, want %v", name, o.Good, o.All, gw)
		}
	}
	if v := rows["schedule punctuality"].Verdict(); v != "BROKEN" {
		t.Errorf("98%% punctual against 99%%: %s", v)
	}
	for _, name := range []string{"task start", "API availability", "platform-caused failures"} {
		if v := rows[name].Verdict(); v != "met" {
			t.Errorf("%s: %s, want met", name, v)
		}
	}
}

// A counter that went down is a restart: its new value is all increase.
func TestIncreasesCountARestartFromZero(t *testing.T) {
	a := mustParse(t, "# TYPE x counter\nx 100\n")
	b := mustParse(t, "# TYPE x counter\nx 150\n")
	c := mustParse(t, "# TYPE x counter\nx 20\n")
	if got := increases([]scrape{a, b, c})["x{}"]; got != 70 {
		t.Errorf("increase = %v, want 50 then 20 after the restart", got)
	}
}

// No event is not measured, never met: a soak whose metrics were not scraped
// must not pass.
func TestNoEventIsNotMeasured(t *testing.T) {
	empty := mustParse(t, "")
	for _, o := range judge(increases([]scrape{empty, empty}), 0.99, 0.99, 0.001, 0.999) {
		if o.Verdict() != "not measured" || !math.IsNaN(o.Ratio()) {
			t.Errorf("%s: %s", o.Name, o.Verdict())
		}
	}
}
