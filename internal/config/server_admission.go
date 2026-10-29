package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/dexadata/dexaflow/internal/admissionwebhook"
)

// maxAdmissionTimeout bounds execution.admission.timeout: a hook runs on the
// scheduler tick when dispatch is synchronous, so a long timeout stalls it.
const maxAdmissionTimeout = 5 * time.Second

// AdmissionSection is execution.admission (ADR 0100).
type AdmissionSection struct {
	// Timeout bounds each hook call. Default 500ms (zero means the default),
	// at most 5s. Bind via
	// DEXAFLOW_EXECUTION_ADMISSION_TIMEOUT.
	Timeout time.Duration `mapstructure:"timeout"`
	// FailOpen admits an attempt unchanged when a hook errors, times out or is
	// skipped after repeated failures, instead of holding it. Default false.
	// Bind via DEXAFLOW_EXECUTION_ADMISSION_FAIL_OPEN.
	FailOpen bool `mapstructure:"fail_open"`
	// Webhook asks an operator's endpoint about every attempt.
	Webhook AdmissionWebhookSection `mapstructure:"webhook"`
}

// AdmissionWebhookSection is execution.admission.webhook (ADR 0100 §8).
type AdmissionWebhookSection struct {
	// URL turns the webhook on. https, unless the host is loopback. Bind via
	// DEXAFLOW_EXECUTION_ADMISSION_WEBHOOK_URL.
	URL string `mapstructure:"url"`
	// CAFile is a PEM bundle the endpoint's certificate must chain to;
	// empty uses the system roots. Bind via
	// DEXAFLOW_EXECUTION_ADMISSION_WEBHOOK_CA_FILE.
	CAFile string `mapstructure:"ca_file"`
	// TokenFile holds a bearer token sent with each request, read on every
	// call. Bind via DEXAFLOW_EXECUTION_ADMISSION_WEBHOOK_TOKEN_FILE.
	TokenFile string `mapstructure:"token_file"`
}

// Enabled reports whether any admission hook is configured.
func (a AdmissionSection) Enabled() bool { return a.Webhook.URL != "" }

func (a AdmissionSection) validate() error {
	if a.Timeout < 0 || a.Timeout > maxAdmissionTimeout {
		return fmt.Errorf("execution.admission.timeout must be at most %s (got %s): a hook runs on the scheduler tick", maxAdmissionTimeout, a.Timeout)
	}
	w := a.Webhook
	if w.URL == "" {
		if w.CAFile != "" || w.TokenFile != "" {
			return errors.New("execution.admission.webhook.ca_file and token_file need execution.admission.webhook.url")
		}
		return nil
	}
	return admissionwebhook.ValidateURL(w.URL)
}
