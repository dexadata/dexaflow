package main

import (
	"fmt"
	"net/url"
	"sort"
)

// target is the DAG and run a profile's requests name. runIDs are the DAG's
// newest runs, newest first: the grid asks for the task summaries of the runs
// it shows.
type target struct {
	dag    string
	run    string
	runIDs []string
}

// profiles are the requests the embedded UI (Airflow 3.3.2) sends once per
// auto-refresh interval on a page, in steady state, after the page has loaded.
// They were recorded from the SPA in a headless browser against an api role
// process (see README.md, Experiment 4), so they hold only what repeats every
// interval: the page load requests and the ones that refresh on a longer
// period are left out.
var profiles = map[string]func(t target) []string{
	// The DAG page (grid) of a DAG with a run in progress: every query
	// whose data holds an active run refetches each interval.
	"dag": func(t target) []string {
		d := url.PathEscape(t.dag)
		return []string{
			"/ui/grid/runs/" + d + "?offset=0&limit=11&order_by=-run_after",
			"/ui/grid/runs/" + d + "?limit=10&order_by=-run_after",
			"/ui/grid/runs/" + d + "?limit=1&order_by=-run_after",
			"/ui/grid/structure/" + d + "?include_upstream=false&include_downstream=false&limit=10&order_by=-run_after",
			"/ui/grid/ti_summaries/" + d + "?" + runIDsQuery(t.runIDs),
			"/ui/dags/" + d + "/latest_run",
			"/api/v2/dags/" + d + "/details",
			"/api/v2/dags/" + d + "/dagRuns?limit=1&state=running&state=queued",
			"/api/v2/dags/" + d + "/dagRuns/~/hitlDetails?" + hitlQuery,
		}
	},
	// The page of one run in progress, with its task instance table.
	"run": func(t target) []string {
		d, r := url.PathEscape(t.dag), url.PathEscape(t.run)
		return []string{
			"/ui/grid/runs/" + d + "?offset=0&limit=11&order_by=-run_after",
			"/ui/grid/runs/" + d + "?limit=10&order_by=-run_after",
			"/ui/grid/runs/" + d + "?limit=1&order_by=-run_after",
			"/ui/grid/structure/" + d + "?include_upstream=false&include_downstream=false&limit=10&order_by=-run_after",
			"/ui/grid/ti_summaries/" + d + "?" + runIDsQuery(t.runIDs),
			"/api/v2/dags/" + d + "/dagRuns/" + r,
			"/api/v2/dags/" + d + "/dagRuns/" + r + "/taskInstances?cursor=&limit=50&order_by=-id",
			"/api/v2/dags/" + d + "/dagRuns?limit=1&state=running&state=queued",
			"/api/v2/dags/" + d + "/dagRuns/" + r + "/hitlDetails?" + hitlQuery,
		}
	},
	// The DAG page of a DAG with no run in progress: only the latest run
	// is polled, to notice a new one.
	"idle": func(t target) []string {
		return []string{"/ui/dags/" + url.PathEscape(t.dag) + "/latest_run"}
	},
}

const hitlQuery = "order_by=dag_id&order_by=run_after&order_by=created_at&order_by=task_display_name&state=deferred&state=awaiting_input&response_received=false"

func runIDsQuery(ids []string) string {
	q := url.Values{"run_ids": ids}
	return q.Encode()
}

// profileRequests returns the requests of the named profile.
func profileRequests(name string, t target) ([]string, error) {
	p, ok := profiles[name]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q (have %v)", name, profileNames())
	}
	return p(t), nil
}

func profileNames() []string {
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
