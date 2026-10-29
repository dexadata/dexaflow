package main

import (
	"path/filepath"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

func TestAdmissionHooksFromConfig(t *testing.T) {
	cfg := &config.ServerConfig{}
	if hooks, err := admissionHooks(cfg); err != nil || hooks != nil {
		t.Fatalf("no webhook: %v, %v; want none", hooks, err)
	}
	cfg.Execution.Admission.Webhook.URL = "https://policy.svc/admit"
	if hooks, err := admissionHooks(cfg); err != nil || len(hooks) != 1 {
		t.Fatalf("webhook: %v, %v; want one hook", hooks, err)
	}
	cfg.Execution.Admission.Webhook.CAFile = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := admissionHooks(cfg); err == nil {
		t.Error("an unreadable CA bundle should stop the boot")
	}
}
