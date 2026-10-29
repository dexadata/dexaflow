package config

import (
	"testing"
	"time"
)

func TestAdmissionSectionValidate(t *testing.T) {
	for name, c := range map[string]struct {
		a  AdmissionSection
		ok bool
	}{
		"default":             {AdmissionSection{Timeout: 500 * time.Millisecond}, true},
		"zero timeout":        {AdmissionSection{}, true},
		"timeout too long":    {AdmissionSection{Timeout: 6 * time.Second}, false},
		"negative timeout":    {AdmissionSection{Timeout: -time.Second}, false},
		"https webhook":       {AdmissionSection{Webhook: AdmissionWebhookSection{URL: "https://policy.svc/admit"}}, true},
		"plain http":          {AdmissionSection{Webhook: AdmissionWebhookSection{URL: "http://policy.svc/admit"}}, false},
		"loopback http":       {AdmissionSection{Webhook: AdmissionWebhookSection{URL: "http://127.0.0.1:9000/admit"}}, true},
		"files without a url": {AdmissionSection{Webhook: AdmissionWebhookSection{TokenFile: "/t"}}, false},
	} {
		if err := c.a.validate(); (err == nil) != c.ok {
			t.Errorf("%s: validate() = %v, want ok=%v", name, err, c.ok)
		}
	}
}
