//go:build !linux

package agent

// hardenWarmProcess is a no-op off Linux. Warm workers only run in Linux pods;
// the agent is built for darwin for local use, where there is no /proc to walk
// and no prctl.
func hardenWarmProcess() error { return nil }

// sweepDescendants is a no-op off Linux; see hardenWarmProcess.
func sweepDescendants() error { return nil }
