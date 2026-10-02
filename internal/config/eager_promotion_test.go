package config

import (
	"os"
	"path/filepath"
	"testing"
)

// scheduler.eager_promotion is an ADR 0062 feature gate: off unless an operator
// turns it on, through the config file or either environment prefix.
func TestSchedulerEagerPromotionGate(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Scheduler.EagerPromotion {
		t.Fatal("scheduler.eager_promotion must default to false")
	}

	for _, env := range []string{"DEXAFLOW_SCHEDULER_EAGER_PROMOTION", "LEOFLOW_SCHEDULER_EAGER_PROMOTION"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "true")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer() error = %v", err)
			}
			if !c.Scheduler.EagerPromotion {
				t.Errorf("%s=true should enable scheduler.eager_promotion", env)
			}
		})
	}

	t.Run("config file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dexaflow.yaml")
		if err := os.WriteFile(path, []byte("scheduler:\n  eager_promotion: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := LoadServer(path, nil)
		if err != nil {
			t.Fatalf("LoadServer() error = %v", err)
		}
		if !c.Scheduler.EagerPromotion {
			t.Error("scheduler.eager_promotion: true in the config file should enable the gate")
		}
	})
}
