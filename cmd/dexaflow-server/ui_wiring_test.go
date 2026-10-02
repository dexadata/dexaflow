package main

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestNewUIServerWiresTheHomeLink locks the wiring, not the leaf (#1290): the
// configured ui.home_link must reach the served shell through the real
// constructor buildAPIServer uses.
func TestNewUIServerWiresTheHomeLink(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.UI.HomeLink = config.HomeLinkSection{Label: "Back to portal", URL: "https://portal.example.com"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv, _ := newUIServer(cfg, logger)
	rec := httptest.NewRecorder()
	srv.Index(rec, "/")

	body := rec.Body.String()
	if !strings.Contains(body, `href="https://portal.example.com"`) || !strings.Contains(body, ">Back to portal<") {
		t.Errorf("served shell lacks the configured home link:\n%s", body)
	}
}
