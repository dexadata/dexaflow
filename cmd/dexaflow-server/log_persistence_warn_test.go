package main

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

func TestLogPersistenceWarnings(t *testing.T) {
	t.Run("durable disk produces no warnings", func(t *testing.T) {
		w := logPersistenceWarnings(config.LogsSection{Backend: "disk", PersistenceEnabled: true})
		if len(w) != 0 {
			t.Errorf("durable disk warned: %+v", w)
		}
	})

	t.Run("object storage without disk persistence produces no warnings", func(t *testing.T) {
		for _, b := range []string{"s3", "gcs"} {
			w := logPersistenceWarnings(config.LogsSection{Backend: b, PersistenceEnabled: false})
			if len(w) != 0 {
				t.Errorf("%s backend warned: %+v", b, w)
			}
		}
	})

	t.Run("empty backend produces no warnings", func(t *testing.T) {
		w := logPersistenceWarnings(config.LogsSection{})
		if len(w) != 0 {
			t.Errorf("empty backend warned: %+v", w)
		}
	})

	t.Run("ephemeral disk warns with key and value", func(t *testing.T) {
		w := logPersistenceWarnings(config.LogsSection{Backend: "disk", PersistenceEnabled: false})
		if len(w) != 1 {
			t.Fatalf("ephemeral disk produced %d warnings, want 1", len(w))
		}
		if w[0].Key != "logs.persistence_enabled" {
			t.Errorf("key = %q, want logs.persistence_enabled", w[0].Key)
		}
		if w[0].Value != "false" {
			t.Errorf("value = %q, want false", w[0].Value)
		}
		for _, want := range []string{"logs.persistence_enabled", "ephemeral", "restart"} {
			if !strings.Contains(w[0].Msg, want) {
				t.Errorf("warning message %q does not contain %q", w[0].Msg, want)
			}
		}
	})
}
