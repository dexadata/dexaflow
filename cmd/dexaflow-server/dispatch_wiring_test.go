package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

type noopInner struct{}

func (noopInner) Dispatch(context.Context, string, string, string, domain.TaskSpec) (executor.Disposition, error) {
	return executor.Dispatched, nil
}

// wrapBuffered must hand back a closeable pool ONLY in buffered mode, so run()
// can drain it on shutdown (#133). Passthrough (Lite, BufferSize=0) has no pool
// and returns a nil closer — deferring Close() on it would be a nil-pointer bug.
func TestWrapBuffered_ReturnsCloserOnlyWhenBuffered(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	disp, closer := wrapBuffered(noopInner{}, nil, log, nil, config.DispatchSection{BufferSize: 0})
	if disp == nil {
		t.Fatal("passthrough dispatcher is nil")
	}
	if closer != nil {
		t.Errorf("passthrough returned a non-nil closer (%T); Lite has no pool to close", closer)
	}

	disp2, closer2 := wrapBuffered(noopInner{}, nil, log, nil, config.DispatchSection{BufferSize: 4, Workers: 2})
	if disp2 == nil {
		t.Fatal("buffered dispatcher is nil")
	}
	if closer2 == nil {
		t.Fatal("buffered returned a nil closer; run() can't drain the pool on shutdown (#133)")
	}
	if err := closer2.Close(); err != nil {
		t.Errorf("closer.Close() = %v, want nil", err)
	}
}
