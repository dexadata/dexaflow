// Package alerts sends native on-failure notifications (#424). The scheduler
// resolves each dexaflow.yaml alert rule's connection to an endpoint URL and calls
// a Notifier — Slack incoming webhooks and generic HTTP webhooks — entirely in
// Go, with no task pod and no Python in the hot path. Connection lookup lives in
// the caller; this package only renders the message and performs the HTTP POST,
// so it stays trivially testable and free of storage dependencies.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
)

// Event carries the DagRun context exposed to an alert message template and to
// the structured webhook payload.
type Event struct {
	// DagID is the failed run's DAG id.
	DagID string
	// RunID is the failed DagRun id.
	RunID string
	// LogicalDate is the run's logical date (may be empty for unscheduled runs).
	LogicalDate string
	// FailedTasks lists the task_ids that reached the failed state.
	FailedTasks []string
}

// Render substitutes the run context into a message template, replacing
// {{dag}}, {{run_id}}, {{logical_date}}, {{task}} (the first failed task) and
// {{tasks}} (all failed tasks, comma-separated). An empty template yields a
// default one-line summary so an operator never receives a blank alert.
func Render(tmpl string, ev Event) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = "DAG {{dag}} run {{run_id}} failed"
		if len(ev.FailedTasks) > 0 {
			tmpl += ": {{tasks}}"
		}
	}
	pairs := make([]string, 0, 2*len(Placeholders))
	for _, name := range Placeholders {
		pairs = append(pairs, "{{"+name+"}}", ev.value(name))
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}

// Placeholders names every substitution Render performs, in the order it
// documents them. Template validation reads this list, so a placeholder added to
// value() and here is supported everywhere at once — and one added to only one of
// them fails the round-trip test rather than silently half-working.
var Placeholders = []string{"dag", "run_id", "logical_date", "task", "tasks"}

// absent is what a placeholder renders when the run carries no value for it. A
// literal marker beats an empty string: "failed for logical date " reads as a
// broken alert, and an operator cannot tell it from a truncated one.
const absent = "(none)"

// value resolves one placeholder name against the event.
func (ev Event) value(name string) string {
	switch name {
	case "dag":
		return orAbsent(ev.DagID)
	case "run_id":
		return orAbsent(ev.RunID)
	case "logical_date":
		// Empty on an unscheduled (manually triggered) run, which is legitimate.
		return orAbsent(ev.LogicalDate)
	case "task":
		if len(ev.FailedTasks) == 0 {
			return absent
		}
		return ev.FailedTasks[0]
	case "tasks":
		if len(ev.FailedTasks) == 0 {
			return absent
		}
		return strings.Join(ev.FailedTasks, ", ")
	default:
		return ""
	}
}

func orAbsent(v string) string {
	if v == "" {
		return absent
	}
	return v
}

// Notifier posts resolved alerts over HTTP. It is safe for concurrent use.
type Notifier struct {
	client *http.Client
}

// NewNotifier returns a Notifier that sends with the given HTTP client. The
// client SHOULD carry a timeout so a slow endpoint cannot wedge the caller.
func NewNotifier(client *http.Client) *Notifier {
	return &Notifier{client: client}
}

// Send posts message to url for the given channel type: "slack" sends Slack's
// {"text": …} incoming-webhook shape; "webhook" sends a structured JSON payload
// carrying the run context. Any headers are applied to the request (e.g. an
// Authorization header for an endpoint whose token is not in the URL). A non-2xx
// response or an unknown channel type is an error; the caller decides whether
// that is fatal (it should not be — an alert failure must not fail the run).
func (n *Notifier) Send(ctx context.Context, channelType, url string, headers map[string]string, message string, ev Event) error {
	var payload any
	switch channelType {
	case "slack":
		payload = map[string]string{"text": message}
	case "webhook":
		payload = map[string]any{
			"dag_id":       ev.DagID,
			"run_id":       ev.RunID,
			"logical_date": ev.LogicalDate,
			"failed_tasks": ev.FailedTasks,
			"message":      message,
		}
	default:
		return fmt.Errorf("unknown alert channel type %q", channelType)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding %s alert payload: %w", channelType, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building %s alert request: %w", channelType, redactURLError(err))
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending %s alert: %w", channelType, redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of the alert response body
	//nolint:errcheck // draining the (capped) body for connection reuse; count/err are irrelevant
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s alert endpoint returned %s", channelType, resp.Status)
	}
	return nil
}

// redactURLError replaces the URL a *url.Error carries with its scheme and
// host. An alert URL is a connection secret (a Slack incoming webhook carries
// its credential in the path), and these errors are logged. The rest of the
// chain is kept, so callers can still match its cause with errors.Is/As.
func redactURLError(err error) error {
	var ue *neturl.Error
	if !errors.As(err, &ue) {
		return err
	}
	return &neturl.Error{Op: ue.Op, URL: redactURL(ue.URL), Err: ue.Err}
}

// redactURL keeps only the scheme and host (with port) of raw.
func redactURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return "(unparseable URL)"
	}
	if u.Host == "" {
		return "(URL without a host)"
	}
	return u.Scheme + "://" + u.Host
}
