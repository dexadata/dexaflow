package cli

// removeMissingDags is the Lite watcher's set-diff step (issue #345). After
// each tick re-resolves the workspace, it computes the set of DAG IDs the
// watcher was tracking ("lastSeen") minus the set still present on disk
// ("current"); for each disappeared DAG it calls deleteDag, which is wired
// in the watcher loop to hit DELETE /api/v2/dags/<id>?deregister=true.
//
// Without this step the watcher is add-only: a DAG removed from disk stays
// registered in the control plane forever (ghost in the UI). With this step,
// `rm -rf ~/dexaflow/foo` is enough to deregister.
//
// Contract:
//   - DAGs in "current" but NOT in "lastSeen" are newly added — not deleted.
//     This protects the first watcher tick of a fresh Lite from trying to
//     wipe everything it just discovered.
//   - DAGs in "lastSeen" but NOT in "current" are the removed set — for each,
//     deleteDag is called.
//   - On deleteDag success the DAG is dropped from the returned newSeen.
//   - On deleteDag failure the DAG STAYS in newSeen (so the next tick retries)
//     and a logf line names the failure. Silent retry would mask a real
//     permission or auth problem.
//
// Pure-function design: no goroutines, no I/O, no time. The wire-up in
// devWatchLoop closes over the actual HTTP DELETE callback.
func removeMissingDags(
	lastSeen map[string]struct{},
	current map[string]struct{},
	deleteDag func(dagID string) error,
	logf func(format string, args ...any),
) map[string]struct{} {
	newSeen := make(map[string]struct{}, len(current))
	for id := range current {
		newSeen[id] = struct{}{}
	}
	for id := range lastSeen {
		if _, still := current[id]; still {
			continue
		}
		if err := deleteDag(id); err != nil {
			logf("✗ failed to remove dag %q from registry: %v", id, err)
			// Keep in seen so the next tick retries — silent give-up would
			// hide a permission or auth problem.
			newSeen[id] = struct{}{}
			continue
		}
		logf("✗ removed dag %q from registry (folder gone)", id)
	}
	return newSeen
}
