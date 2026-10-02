package domain

// DagStats holds the home dashboard's DAG counters: the number of active DAGs
// and how many have a latest run in each state.
type DagStats struct {
	Active  int
	Failed  int
	Running int
	Queued  int
}

// HistoricalMetrics holds run- and task-instance counts grouped by state over a
// time window, keyed by the Dexaflow state name (e.g. "success", "up_for_retry").
type HistoricalMetrics struct {
	RunStates map[string]int
	TIStates  map[string]int
}
