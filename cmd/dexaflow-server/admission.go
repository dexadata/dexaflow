package main

import (
	"log/slog"
	"net/url"

	"github.com/dexadata/dexaflow/internal/admissionwebhook"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/pkg/admission"
)

// admissionHooks builds the dispatch admission hooks execution.admission
// configures (ADR 0100), in chain order. None configured returns nil. An
// error (a CA bundle or token file that cannot be read) stops the boot.
func admissionHooks(cfg *config.ServerConfig) ([]admission.Hook, error) {
	a := cfg.Execution.Admission
	if !a.Enabled() {
		return nil, nil
	}
	wh, err := admissionwebhook.New(admissionwebhook.Config{
		URL:       a.Webhook.URL,
		CAFile:    a.Webhook.CAFile,
		TokenFile: a.Webhook.TokenFile,
		// The webhook may move a task onto another service account only
		// when the allowlist bounds which ones (ADR 0100 §8).
		AllowServiceAccount: len(cfg.Executor.Defaults.ExecutionAllowlist.ServiceAccounts) > 0,
	})
	if err != nil {
		return nil, err
	}
	return []admission.Hook{wh}, nil
}

// applyAdmission installs the hooks on a dispatcher. No hooks leave it as it
// was.
func applyAdmission(d *dispatch.Dispatcher, hooks []admission.Hook, cfg *config.ServerConfig, metrics *observability.Metrics, logger *slog.Logger) {
	if len(hooks) == 0 {
		return
	}
	opts := dispatch.AdmissionOptions{Timeout: cfg.Execution.Admission.Timeout, FailOpen: cfg.Execution.Admission.FailOpen}
	if metrics != nil {
		opts.Recorder = metrics
	}
	d.SetAdmission(hooks, opts)
	logger.Info("dispatch admission hooks installed", "hooks", len(hooks), "webhook", endpointForLog(cfg.Execution.Admission.Webhook.URL),
		"timeout", cfg.Execution.Admission.Timeout, "fail_open", cfg.Execution.Admission.FailOpen)
}

// endpointForLog drops the query and fragment of the webhook URL, which may
// carry a secret.
func endpointForLog(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.Path
}
