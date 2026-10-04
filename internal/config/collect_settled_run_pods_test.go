package config

import "testing"

// Collecting a settled run's pods at settle time is opt-in: by default finished
// pods stay for the grace period, as before.
func TestCollectSettledRunPodsDefaultsOff(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Executor.CollectSettledRunPods {
		t.Fatal("executor.collect_settled_run_pods defaults to true, want false")
	}
}

// Both the current and the legacy env prefix reach the key.
func TestCollectSettledRunPodsEnvBindsBothPrefixes(t *testing.T) {
	for _, name := range []string{"DEXAFLOW_EXECUTOR_COLLECT_SETTLED_RUN_PODS", "LEOFLOW_EXECUTOR_COLLECT_SETTLED_RUN_PODS"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "true")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if !c.Executor.CollectSettledRunPods {
				t.Fatalf("%s=true did not turn the collection on", name)
			}
		})
	}
}
