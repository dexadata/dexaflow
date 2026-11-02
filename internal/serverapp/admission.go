package serverapp

import (
	"log/slog"
	"net/url"

	"github.com/dexadata/dexaflow/internal/admissionwebhook"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/pkg/admission"
)

// admissionHooks builds the dispatch admission chain (ADR 0100): the hooks a
// pkg/server caller registered, in their order, then the webhook
// execution.admission configures. None of either returns nil. An error (a CA
// bundle or token file that cannot be read) stops the boot.
func admissionHooks(cfg *config.ServerConfig, extra []admission.Hook) ([]admission.Hook, error) {
	hooks := append([]admission.Hook(nil), extra...)
	a := cfg.Execution.Admission
	if !a.Enabled() {
		if len(hooks) == 0 {
			return nil, nil
		}
		return hooks, nil
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
	return append(hooks, wh), nil
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
	logger.Info("dispatch admission hooks installed", "hooks", len(hooks), "from_options", len(hooks)-webhookCount(cfg), "webhook", endpointForLog(cfg.Execution.Admission.Webhook.URL),
		"timeout", cfg.Execution.Admission.Timeout, "fail_open", cfg.Execution.Admission.FailOpen)
}

// webhookCount is 1 when execution.admission configures the webhook.
func webhookCount(cfg *config.ServerConfig) int {
	if cfg.Execution.Admission.Enabled() {
		return 1
	}
	return 0
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
