// Package failurealert adapts a DAG's on-failure alert rules (#424) to the
// scheduler's Alerter seam: it resolves each rule's managed connection to an
// endpoint URL, renders the message, and sends it via the alerts notifier —
// best-effort, so a delivery failure is logged and never fails the run. It is
// the piece that wires the pure notifier (internal/alerts) and the connection
// store together, kept out of the scheduler so that package stays storage-free.
package failurealert

import (
	"context"
	"log/slog"

	"github.com/dexadata/dexaflow/internal/alerts"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

// Endpoint is a resolved alert channel: the URL to POST to and any headers the
// endpoint requires (e.g. an Authorization header when the token is not carried
// in the URL, as with Opsgenie).
type Endpoint struct {
	URL     string
	Headers map[string]string
}

// EndpointResolver maps a managed connection id to its channel Endpoint
// (including any secret the connection carries). tenantID scopes the lookup. The
// connection→endpoint contract lives in the implementation so this package needs
// no storage dependency.
type EndpointResolver interface {
	ResolveAlertEndpoint(ctx context.Context, tenantID, connID string) (Endpoint, error)
}

// sender is the subset of *alerts.Notifier this package needs, named locally so
// it can be faked in tests.
type sender interface {
	Send(ctx context.Context, channelType, url string, headers map[string]string, message string, ev alerts.Event) error
}

// Recorder counts alert dispatches for observability (principle 10). result is
// "sent" on a successful POST or "failed" when the connection can't be resolved
// or the send errors. A nil Recorder disables metrics.
type Recorder interface {
	RecordAlert(dagID, channelType, result string)
}

// alert dispatch outcomes reported to the Recorder.
const (
	resultSent   = "sent"
	resultFailed = "failed"
)

// Dispatcher implements scheduler.Alerter by resolving and sending a run's
// on-failure alert rules.
type Dispatcher struct {
	notifier sender
	resolver EndpointResolver
	recorder Recorder
	logger   *slog.Logger
}

// New builds a Dispatcher over the given notifier, connection resolver, metrics
// recorder (nil disables metrics), and logger.
func New(notifier sender, resolver EndpointResolver, recorder Recorder, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{notifier: notifier, resolver: resolver, recorder: recorder, logger: logger}
}

// record reports an alert outcome to the Recorder when one is configured.
func (d *Dispatcher) record(dagID, channelType, result string) {
	if d.recorder != nil {
		d.recorder.RecordAlert(dagID, channelType, result)
	}
}

// AlertRunFailed dispatches every on_failure rule of a failed run and reports
// whether ALL of them were delivered. Each rule is resolved, rendered, and sent
// independently: a resolver or send error on one rule is logged and skipped so
// the remaining rules still fire.
//
// The boolean is what lets the caller distinguish a delivered page from a lost
// one. Before it existed the caller marked the run alerted before sending, so any
// failure here was permanent (#470 validation: a 500 left the run marked with no
// retry). All-or-nothing is deliberate: a partial success is retried in full, so
// a rule that already landed pages twice. That is the trade-off this code already
// declares — a duplicate beats a missed page.
func (d *Dispatcher) AlertRunFailed(ctx context.Context, run scheduler.RunState) bool {
	if run.Alerts == nil {
		return true
	}
	delivered := true
	ev := alerts.Event{
		DagID: run.DagID,
		// The user-facing run id, not RunState.RunID: an operator cannot paste a
		// UUID into the UI or the API.
		RunID:       run.DisplayRunID,
		LogicalDate: run.LogicalDate,
		FailedTasks: failedTasks(run),
	}
	for _, rule := range run.Alerts.OnFailure {
		endpoint, err := d.resolver.ResolveAlertEndpoint(ctx, run.TenantID, rule.Conn)
		if err != nil {
			d.logger.Error("resolving alert connection",
				"dag", run.DagID, "run", run.RunID, "conn", rule.Conn, "error", err)
			d.record(run.DagID, rule.Type, resultFailed)
			delivered = false
			continue
		}
		message := alerts.Render(rule.Message, ev)
		if serr := d.notifier.Send(ctx, rule.Type, endpoint.URL, endpoint.Headers, message, ev); serr != nil {
			d.logger.Error("sending on-failure alert",
				"dag", run.DagID, "run", run.RunID, "type", rule.Type, "conn", rule.Conn, "error", serr)
			d.record(run.DagID, rule.Type, resultFailed)
			delivered = false
			continue
		}
		d.logger.Info("on-failure alert sent",
			"dag", run.DagID, "run", run.RunID, "type", rule.Type, "conn", rule.Conn)
		d.record(run.DagID, rule.Type, resultSent)
	}
	return delivered
}

// failedTasks returns the task_ids that actually failed (not merely upstream-
// failed), in DAG order, so the message template names a real culprit.
func failedTasks(run scheduler.RunState) []string {
	var out []string
	for _, t := range run.Tasks {
		if run.States[t.TaskID] == domain.TaskStateFailed {
			out = append(out, t.TaskID)
		}
	}
	return out
}
