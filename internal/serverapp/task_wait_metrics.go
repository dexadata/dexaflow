package serverapp

import (
	"context"
	"log/slog"
	"time"

	"github.com/dexadata/dexaflow/internal/gatedtick"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// oldestWaitingInterval paces dexaflow_task_oldest_waiting_seconds.
const oldestWaitingInterval = 15 * time.Second

// waitingSource reads the oldest waiting age per tenant and state.
type waitingSource interface {
	OldestWaiting(ctx context.Context) ([]storage.WaitingAge, error)
}

// waitingGauge receives dexaflow_task_oldest_waiting_seconds.
type waitingGauge interface {
	SetOldestWaiting(ages []observability.WaitingAge)
}

// oldestWaiting refreshes the gauge on the scheduler leader (#2300). Every
// replica runs the cycle so a replica that loses the lead clears its series:
// two replicas exporting different ages for one tenant would make max() lie.
type oldestWaiting struct {
	src     waitingSource
	gauge   waitingGauge
	leading func() bool
	logger  *slog.Logger
	// failing is set while reads fail, so an outage warns once.
	failing bool
}

func (w *oldestWaiting) cycle(ctx context.Context) {
	if !w.leading() {
		w.gauge.SetOldestWaiting(nil)
		return
	}
	ages, err := w.src.OldestWaiting(ctx)
	if err != nil {
		if !w.failing {
			w.logger.Warn("could not read the oldest waiting tasks; dexaflow_task_oldest_waiting_seconds is cleared until the read succeeds", "error", err)
		}
		w.failing = true
		// A stale age reads as a task that stopped waiting at that age; no
		// series makes the gap visible instead.
		w.gauge.SetOldestWaiting(nil)
		return
	}
	w.failing = false
	out := make([]observability.WaitingAge, 0, len(ages))
	for _, a := range ages {
		out = append(out, observability.WaitingAge{Tenant: a.Tenant, State: a.State, AgeSeconds: a.AgeSeconds})
	}
	w.gauge.SetOldestWaiting(out)
}

// startOldestWaiting runs the gauge refresh when this process exports metrics.
func startOldestWaiting(ctx context.Context, src waitingSource, gauge waitingGauge, leading func() bool, logger *slog.Logger) {
	if gauge == nil {
		return
	}
	w := &oldestWaiting{src: src, gauge: gauge, leading: leading, logger: logger}
	gatedtick.Start(ctx, "oldest-waiting-tasks", oldestWaitingInterval, nil, logger, func() { w.cycle(ctx) })
}
