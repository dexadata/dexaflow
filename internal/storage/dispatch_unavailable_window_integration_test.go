//go:build integration

package storage_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// errQuota is what the Kubernetes executor returns, classified Backpressure,
// when a ResourceQuota refuses the pod (ADR 0053).
var errQuota = errors.New(`pods "a" is forbidden: exceeded quota`)

// errRejected is a create the apiserver understood and refused, classified
// Rejected.
var errRejected = errors.New(`pods "a" is invalid: spec.containers[0].image: Required value`)

// answeredDispatcher fails every create with the disposition it is set to:
// an answer from the apiserver (Backpressure, Rejected) or an unavailable API.
type answeredDispatcher struct{ disp executor.Disposition }

func (d *answeredDispatcher) Dispatch(context.Context, string, string, string, domain.TaskSpec) (executor.Disposition, error) {
	switch d.disp {
	case executor.Backpressure:
		return executor.Backpressure, errQuota
	case executor.Rejected:
		return executor.Rejected, errRejected
	default:
		return d.disp, errAPIUnavailable
	}
}

// ageWindow moves the start of the task's outage window 20 minutes into the
// past, as 20 minutes of answered creates would. A window that is not open
// is left alone.
func (f *outageFixture) ageWindow(t *testing.T) {
	t.Helper()
	if _, err := f.pg.Pool.Exec(f.ctx,
		`UPDATE task_instances SET dispatch_unavailable_since = dispatch_unavailable_since - interval '20 minutes'
		  WHERE dag_run_id = $1::uuid AND task_id = 'a'`, f.runUUID); err != nil {
		t.Fatal(err)
	}
}

// wantFreshWindow checks the task is still held after one more unavailable
// create, with a window that started at that create rather than at the first
// one of the dispatch episode.
func (f *outageFixture) wantFreshWindow(t *testing.T, what string) {
	t.Helper()
	r := f.row(t)
	if r.state != "scheduled" {
		t.Fatalf("a single 503 after 20 minutes of %s failed the task: %+v", what, r)
	}
	if r.since == nil || time.Since(*r.since) > time.Minute {
		t.Errorf("the 503 after %s must start a new outage window, since = %v", what, r.since)
	}
}

// TestUnavailableWindowClosedByAnsweredCreate: one 503 opens the outage window
// of ADR 0093, then the apiserver answers every create for 20 minutes (quota
// backpressure, or a rejection), then one more 503 arrives. The answers ended
// the first outage, so the second 503 starts a new window and the task is
// re-offered instead of failing as dispatch_failed.
func TestUnavailableWindowClosedByAnsweredCreate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  executor.Disposition
		answers int
	}{
		{"backpressure", executor.Backpressure, 3},
		{"rejected", executor.Rejected, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOutageFixture(t, "unavail_answered_"+tc.name) // first create: 503
			if r := f.row(t); r.since == nil {
				t.Fatalf("the first 503 must open the window: %+v", r)
			}
			d := &answeredDispatcher{disp: tc.answer}
			f.sched.SetDispatcher(d)
			for i := 0; i < tc.answers; i++ {
				f.releaseHold(t)
				f.steps(t, 1)
			}
			if r := f.row(t); r.since != nil {
				t.Errorf("an answered create must close the window, since = %v", *r.since)
			}
			f.ageWindow(t)
			d.disp = executor.Unavailable
			f.releaseHold(t)
			f.steps(t, 1)
			f.wantFreshWindow(t, tc.name)
		})
	}
}

// TestBufferedUnavailableWindowClosedByAnsweredCreate is the buffered twin of
// TestUnavailableWindowClosedByAnsweredCreate: the failures are handed to the
// buffered workers' failure handler over the real store. A buffer wait past
// its bound is reported as Backpressure but never reached the API, so it
// keeps the window, and the next 503 fails the task.
func TestBufferedUnavailableWindowClosedByAnsweredCreate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		answer    executor.Disposition
		cause     error
		keepsOpen bool
	}{
		{"backpressure", executor.Backpressure, errQuota, false},
		{"rejected", executor.Rejected, errRejected, false},
		{"buffer_wait_exceeded", executor.Backpressure, dispatch.ErrBufferWaitExceeded, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOutageFixture(t, "unavail_buffered_"+tc.name) // first create: 503
			h := scheduler.NewAsyncDispatchFailures(storage.NewSchedulerStore(f.pg), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := h.HandleDispatchFailure(f.ctx, f.runUUID, "a", tc.answer, tc.cause); err != nil {
				t.Fatalf("handling the %s failure: %v", tc.name, err)
			}
			if r := f.row(t); (r.since != nil) != tc.keepsOpen {
				t.Errorf("after %s: since = %v, want the window kept open = %v", tc.name, r.since, tc.keepsOpen)
			}
			f.ageWindow(t)
			if err := h.HandleDispatchFailure(f.ctx, f.runUUID, "a", executor.Unavailable, errAPIUnavailable); err != nil {
				t.Fatalf("handling the 503: %v", err)
			}
			if !tc.keepsOpen {
				f.wantFreshWindow(t, tc.name)
				return
			}
			if r := f.row(t); r.state != "failed" {
				t.Errorf("a buffer wait says nothing about the API: the 503 after it must fail the task, got %+v", r)
			}
		})
	}
}
