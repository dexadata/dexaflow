package main

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/executor"
)

type stubSettledRuns struct{}

func (stubSettledRuns) SettledRuns(context.Context, []executor.RunRef) (map[executor.RunRef]bool, error) {
	return map[executor.RunRef]bool{}, nil
}

// The reconciler gets a settled-run checker only when the operator turned the
// collection on; off (the default) it gets none and keeps today's age-based GC.
func TestSettledRunCollectionWiring(t *testing.T) {
	if got := settledRunCollection(config.ExecutorSection{}, stubSettledRuns{}); got != nil {
		t.Fatalf("collection off returned %v, want nil", got)
	}
	if got := settledRunCollection(config.ExecutorSection{CollectSettledRunPods: true}, stubSettledRuns{}); got == nil {
		t.Fatal("collection on returned nil")
	}
}
