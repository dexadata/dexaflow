package failurealert

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/dexadata/dexaflow/internal/alerts"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

type fakeRecorder struct {
	mu    sync.Mutex
	calls []string // "dag:type:result"
}

func (f *fakeRecorder) RecordAlert(dagID, channelType, result string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dagID+":"+channelType+":"+result)
}

type fakeResolver struct {
	urls    map[string]string
	headers map[string]map[string]string
	err     map[string]error
}

func (f *fakeResolver) ResolveAlertEndpoint(_ context.Context, _, connID string) (Endpoint, error) {
	if e := f.err[connID]; e != nil {
		return Endpoint{}, e
	}
	return Endpoint{URL: f.urls[connID], Headers: f.headers[connID]}, nil
}

type capturedSend struct {
	channelType, url, message string
	headers                   map[string]string
	ev                        alerts.Event
}

type fakeSender struct {
	mu   sync.Mutex
	sent []capturedSend
	fail map[string]error // keyed by url
}

func (f *fakeSender) Send(_ context.Context, channelType, url string, headers map[string]string, message string, ev alerts.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.fail[url]; e != nil {
		return e
	}
	f.sent = append(f.sent, capturedSend{channelType, url, message, headers, ev})
	return nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func failedRun() scheduler.RunState {
	return scheduler.RunState{
		RunID: "r1", DagID: "etl", TenantID: "t1",
		Tasks: []domain.TaskSpec{{TaskID: "extract"}, {TaskID: "load"}},
		States: map[string]domain.TaskState{
			"extract": domain.TaskStateFailed,
			"load":    domain.TaskStateUpstreamFailed,
		},
		Alerts: &domain.AlertsConfig{OnFailure: []domain.AlertRule{
			{Type: "slack", Conn: "slack_prod", Message: "{{dag}} failed on {{task}}"},
			{Type: "webhook", Conn: "pagerduty"},
		}},
	}
}

// Every rule is resolved, rendered, and sent; the failed task drives the template.
func TestAlertRunFailedSendsEveryRule(t *testing.T) {
	res := &fakeResolver{urls: map[string]string{
		"slack_prod": "https://hooks.slack.com/services/xxx",
		"pagerduty":  "https://events.pagerduty.com/enqueue",
	}}
	snd := &fakeSender{}
	d := New(snd, res, nil, quietLogger())

	d.AlertRunFailed(context.Background(), failedRun())

	if len(snd.sent) != 2 {
		t.Fatalf("sent %d alerts, want 2", len(snd.sent))
	}
	got := snd.sent[0]
	if got.channelType != "slack" || got.url != "https://hooks.slack.com/services/xxx" {
		t.Errorf("slack rule mis-sent: %+v", got)
	}
	if got.message != "etl failed on extract" {
		t.Errorf("message = %q, want rendered with the failed task", got.message)
	}
}

// A resolver failure on one rule is logged and skipped; the other rule still sends.
func TestAlertRunFailedIsBestEffort(t *testing.T) {
	res := &fakeResolver{
		urls: map[string]string{"pagerduty": "https://events.pagerduty.com/enqueue"},
		err:  map[string]error{"slack_prod": errors.New("no such connection")},
	}
	snd := &fakeSender{}
	d := New(snd, res, nil, quietLogger())

	d.AlertRunFailed(context.Background(), failedRun())

	if len(snd.sent) != 1 || snd.sent[0].channelType != "webhook" {
		t.Fatalf("expected only the webhook to send, got %+v", snd.sent)
	}
}

// A send failure on one rule is logged and swallowed; the run is never affected
// and the remaining rule still sends (best-effort at the send layer too).
func TestAlertRunFailedSwallowsSendError(t *testing.T) {
	res := &fakeResolver{urls: map[string]string{
		"slack_prod": "https://hooks.slack.com/services/xxx",
		"pagerduty":  "https://events.pagerduty.com/enqueue",
	}}
	snd := &fakeSender{fail: map[string]error{
		"https://hooks.slack.com/services/xxx": errors.New("slack 500"),
	}}
	d := New(snd, res, nil, quietLogger())

	d.AlertRunFailed(context.Background(), failedRun())

	if len(snd.sent) != 1 || snd.sent[0].channelType != "webhook" {
		t.Fatalf("a failed send must not drop the other rule; got %+v", snd.sent)
	}
}

// Each rule records a metric: "sent" on success, "failed" when the send errors —
// so operators can alert on the alerter itself (observability, principle 10).
func TestAlertRunFailedRecordsMetrics(t *testing.T) {
	res := &fakeResolver{urls: map[string]string{
		"slack_prod": "https://hooks.slack.com/services/xxx",
		"pagerduty":  "https://events.pagerduty.com/enqueue",
	}}
	snd := &fakeSender{fail: map[string]error{
		"https://events.pagerduty.com/enqueue": errors.New("pd 500"),
	}}
	rec := &fakeRecorder{}
	d := New(snd, res, rec, quietLogger())

	d.AlertRunFailed(context.Background(), failedRun())

	got := strings.Join(rec.calls, ",")
	if !strings.Contains(got, "etl:slack:sent") {
		t.Errorf("missing slack sent metric: %v", rec.calls)
	}
	if !strings.Contains(got, "etl:webhook:failed") {
		t.Errorf("missing webhook failed metric: %v", rec.calls)
	}
}

// A resolver failure also records a "failed" metric (not a silent drop).
func TestAlertRunFailedRecordsResolveFailure(t *testing.T) {
	res := &fakeResolver{
		urls: map[string]string{"pagerduty": "https://events.pagerduty.com/enqueue"},
		err:  map[string]error{"slack_prod": errors.New("no such connection")},
	}
	rec := &fakeRecorder{}
	d := New(&fakeSender{}, res, rec, quietLogger())

	d.AlertRunFailed(context.Background(), failedRun())

	if !strings.Contains(strings.Join(rec.calls, ","), "etl:slack:failed") {
		t.Errorf("a resolve failure must record failed: %v", rec.calls)
	}
}

// The resolver's headers reach the sender, so an endpoint that needs an auth
// header (e.g. Opsgenie) gets one.
func TestAlertRunFailedForwardsHeaders(t *testing.T) {
	res := &fakeResolver{
		urls:    map[string]string{"slack_prod": "u1", "pagerduty": "u2"},
		headers: map[string]map[string]string{"pagerduty": {"Authorization": "GenieKey k"}},
	}
	snd := &fakeSender{}
	d := New(snd, res, nil, quietLogger())

	d.AlertRunFailed(context.Background(), failedRun())

	for _, s := range snd.sent {
		if s.channelType == "webhook" && s.headers["Authorization"] != "GenieKey k" {
			t.Fatalf("webhook send missing auth header: %+v", s)
		}
	}
}

// The Dispatcher satisfies the scheduler's Alerter seam.
func TestDispatcherImplementsAlerter(t *testing.T) {
	var _ scheduler.Alerter = New(&fakeSender{}, &fakeResolver{}, nil, quietLogger())
}

// An operator reading the alert needs the run identifier they can paste into the
// UI or the API. RunState.RunID is the database UUID; the user-facing id is the
// dag_runs.run_id column, which is what {{run_id}} must render.
func TestAlertRunFailedRendersUserFacingRunID(t *testing.T) {
	run := failedRun()
	run.RunID = "018f3a1c-7e6b-7c3a-9f21-4d5e6f7a8b9c" // the UUID the scheduler carries
	run.DisplayRunID = "manual__2026-07-30T12:00:00+00:00"
	run.Alerts.OnFailure = []domain.AlertRule{{Type: "slack", Conn: "slack_prod", Message: "run {{run_id}}"}}

	snd := &fakeSender{}
	d := New(snd, &fakeResolver{urls: map[string]string{"slack_prod": "https://example.invalid/hook"}}, nil, quietLogger())
	d.AlertRunFailed(context.Background(), run)

	if len(snd.sent) != 1 {
		t.Fatalf("sent %d alerts, want 1", len(snd.sent))
	}
	if got := snd.sent[0].message; got != "run manual__2026-07-30T12:00:00+00:00" {
		t.Errorf("message = %q, want the user-facing run id, not the UUID", got)
	}
}

// {{logical_date}} is a documented placeholder, so it must not render empty on a
// scheduled run: an alert saying "failed for logical date " is worse than useless
// for the operator deciding whether to backfill.
func TestAlertRunFailedRendersLogicalDate(t *testing.T) {
	run := failedRun()
	run.LogicalDate = "2026-07-30T00:00:00Z"
	run.Alerts.OnFailure = []domain.AlertRule{{Type: "slack", Conn: "slack_prod", Message: "for {{logical_date}}"}}

	snd := &fakeSender{}
	d := New(snd, &fakeResolver{urls: map[string]string{"slack_prod": "https://example.invalid/hook"}}, nil, quietLogger())
	d.AlertRunFailed(context.Background(), run)

	if len(snd.sent) != 1 {
		t.Fatalf("sent %d alerts, want 1", len(snd.sent))
	}
	if got := snd.sent[0].message; got != "for 2026-07-30T00:00:00Z" {
		t.Errorf("message = %q, want the logical date substituted", got)
	}
}

// An unscheduled run has no logical date. Rather than leaving a dangling "for ",
// the placeholder renders a legible marker.
func TestAlertRunFailedLogicalDateAbsent(t *testing.T) {
	run := failedRun()
	run.Alerts.OnFailure = []domain.AlertRule{{Type: "slack", Conn: "slack_prod", Message: "for {{logical_date}}"}}

	snd := &fakeSender{}
	d := New(snd, &fakeResolver{urls: map[string]string{"slack_prod": "https://example.invalid/hook"}}, nil, quietLogger())
	d.AlertRunFailed(context.Background(), run)

	if got := snd.sent[0].message; got != "for (none)" {
		t.Errorf("message = %q, want a legible marker for an absent logical date", got)
	}
}
