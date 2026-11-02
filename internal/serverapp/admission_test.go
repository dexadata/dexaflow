package serverapp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/pkg/admission"
)

func TestAdmissionHooksFromConfig(t *testing.T) {
	cfg := &config.ServerConfig{}
	if hooks, err := admissionHooks(cfg, nil); err != nil || hooks != nil {
		t.Fatalf("no webhook: %v, %v; want none", hooks, err)
	}
	cfg.Execution.Admission.Webhook.URL = "https://policy.svc/admit"
	if hooks, err := admissionHooks(cfg, nil); err != nil || len(hooks) != 1 {
		t.Fatalf("webhook: %v, %v; want one hook", hooks, err)
	}
	cfg.Execution.Admission.Webhook.CAFile = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := admissionHooks(cfg, nil); err == nil {
		t.Error("an unreadable CA bundle should stop the boot")
	}
}

func TestAdmissionHooksChainOptionsBeforeTheWebhook(t *testing.T) {
	first := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		return admission.Admit(), nil
	})
	second := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		return admission.Refuse("second"), nil
	})
	cfg := &config.ServerConfig{}
	hooks, err := admissionHooks(cfg, []admission.Hook{first, second})
	if err != nil || len(hooks) != 2 {
		t.Fatalf("options only: %d hooks, %v; want the two registered", len(hooks), err)
	}
	if d, _ := hooks[1].Admit(context.Background(), &admission.Request{}); !refusedOf(d) {
		t.Error("the registered order was not kept")
	}
	cfg.Execution.Admission.Webhook.URL = "https://policy.svc/admit"
	hooks, err = admissionHooks(cfg, []admission.Hook{first})
	if err != nil || len(hooks) != 2 {
		t.Fatalf("options and webhook: %d hooks, %v; want two", len(hooks), err)
	}
	if _, ok := hooks[1].(admission.HookFunc); ok {
		t.Error("the configured webhook should run after the registered hooks")
	}
	if webhookCount(cfg) != 1 || webhookCount(&config.ServerConfig{}) != 0 {
		t.Error("webhookCount should count the configured webhook only")
	}
}

func refusedOf(d admission.Decision) bool {
	_, refused := d.Refused()
	return refused
}
