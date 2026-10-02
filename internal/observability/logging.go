// Package observability wires structured logging, Prometheus metrics, and
// OpenTelemetry tracing for the Dexaflow control plane (ADR 0010).
package observability

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewLogger builds a slog.Logger writing to stdout at the given level. A format
// of "text" uses the human-readable handler; anything else (default "json")
// uses the JSON handler required for production (ADR 0010).
func NewLogger(level, format string) *slog.Logger {
	return slog.New(newHandler(os.Stdout, level, format))
}

func newHandler(w io.Writer, level, format string) slog.Handler {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	if strings.EqualFold(format, "text") {
		return slog.NewTextHandler(w, opts)
	}
	return slog.NewJSONHandler(w, opts)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
