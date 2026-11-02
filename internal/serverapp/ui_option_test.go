package serverapp

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/ui"
)

type optionUI struct{}

func (optionUI) StaticHandler() http.Handler           { return http.NotFoundHandler() }
func (optionUI) Index(w http.ResponseWriter, _ string) { _, _ = w.Write([]byte("custom")) }

// TestSelectUIServerPrefersTheOptionUI pins ADR 0099 §3 for the UI: a UI
// passed as an option is served in place of the embedded SPA and boot names
// it; without one the embedded SPA is built from the ui.* keys.
func TestSelectUIServerPrefersTheOptionUI(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := &config.ServerConfig{}

	got, _ := selectUIServer(cfg, optionUI{}, logger)
	if _, ok := got.(optionUI); !ok {
		t.Fatalf("UI = %T, want the option's UI", got)
	}
	if !strings.Contains(buf.String(), "ui: server from options") {
		t.Fatalf("boot log %q does not name the option UI", buf.String())
	}

	got, _ = selectUIServer(cfg, nil, logger)
	if _, ok := got.(*ui.Server); !ok {
		t.Fatalf("UI = %T, want the embedded SPA", got)
	}
}
