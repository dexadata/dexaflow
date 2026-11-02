package serverapp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"

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

// TestSelectLogSinkKeepsTheOptionSinkOpen pins ADR 0099 §3 for the log sink:
// a sink passed as an option replaces the configured backend, boot says so,
// and the server never closes it (the caller owns it). The configured sink
// is still built when no option is given.
func TestSelectLogSinkKeepsTheOptionSinkOpen(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := &config.ServerConfig{}
	cfg.Logs.Backend = "s3" // would fail without a bucket: the option must win

	custom := &closableSink{}
	sink, release, err := selectLogSink(context.Background(), cfg, custom, logger)
	if err != nil {
		t.Fatalf("selectLogSink: %v", err)
	}
	if sink != custom {
		t.Fatalf("sink = %T; want the option's sink", sink)
	}
	release()
	if custom.closed != 0 {
		t.Fatalf("option sink closed %d times; the caller owns it", custom.closed)
	}
	if !strings.Contains(buf.String(), "sink from options") {
		t.Fatalf("boot log %q does not name the option sink", buf.String())
	}

	cfg.Logs.Backend = "disk"
	cfg.Logs.Dir = t.TempDir()
	sink, release, err = selectLogSink(context.Background(), cfg, nil, logger)
	if err != nil {
		t.Fatalf("selectLogSink without option: %v", err)
	}
	if _, disk := sink.(*logs.DiskSink); !disk {
		t.Fatalf("sink = %T; want the configured disk sink", sink)
	}
	release()
}
