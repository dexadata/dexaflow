package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/logs"
)

type closableSink struct {
	logs.Sink
	closed int
	err    error
}

func (c *closableSink) Close() error {
	c.closed++
	return c.err
}

// TestCloseLogSinkClosesAndReportsFailure pins the shutdown seam of #647: a
// sink with Close is closed once, a failure is logged, and a sink without one
// is left alone.
func TestCloseLogSinkClosesAndReportsFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	ok := &closableSink{}
	closeLogSink(ok, logger)
	if ok.closed != 1 || buf.Len() != 0 {
		t.Fatalf("closes = %d, log %q; want 1 close and nothing logged", ok.closed, buf.String())
	}

	failing := &closableSink{err: errors.New("pool busy")}
	closeLogSink(failing, logger)
	if failing.closed != 1 || !strings.Contains(buf.String(), "pool busy") {
		t.Fatalf("closes = %d, log %q; want the failure logged", failing.closed, buf.String())
	}

	closeLogSink(logs.NewDiskSink(t.TempDir()), logger)
}
