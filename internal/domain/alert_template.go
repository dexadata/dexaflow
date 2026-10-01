package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/dexadata/dexaflow/internal/alerts"
)

// ErrUnknownAlertPlaceholder reports an alert message template referencing a
// substitution Leoflow does not perform.
var ErrUnknownAlertPlaceholder = errors.New("unknown alert placeholder")

// placeholderRe matches a {{name}} span, tolerating inner spaces so an
// Airflow-style "{{ ds }}" is reported by name rather than passing through as
// unrecognized text.
var placeholderRe = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// validateAlertTemplates rejects a placeholder Render will not substitute.
//
// An unknown placeholder is not a rendering error — Render leaves it alone — so
// it survives compile and reaches the operator verbatim. "etl failed on
// {{taskk}}" is a silent misconfiguration that only shows itself in the alert
// that was supposed to explain an outage, which is the worst moment to find it.
//
// The check runs at compile, while the author is still looking at the file.
func (c *LeoflowConfig) validateAlertTemplates() error {
	if c.Alerts == nil {
		return nil
	}
	known := make(map[string]bool, len(alerts.Placeholders))
	for _, name := range alerts.Placeholders {
		known[name] = true
	}
	for i, rule := range c.Alerts.OnFailure {
		var unknown []string
		for _, m := range placeholderRe.FindAllStringSubmatch(rule.Message, -1) {
			if name := m[1]; !known[name] {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			return fmt.Errorf("%w in alerts.on_failure[%d].message: %s; supported: %s",
				ErrUnknownAlertPlaceholder, i, strings.Join(unknown, ", "), supportedList())
		}
	}
	return nil
}

// supportedList renders the placeholder vocabulary for an error message.
func supportedList() string {
	out := make([]string, 0, len(alerts.Placeholders))
	for _, name := range alerts.Placeholders {
		out = append(out, "{{"+name+"}}")
	}
	return strings.Join(out, " ")
}
