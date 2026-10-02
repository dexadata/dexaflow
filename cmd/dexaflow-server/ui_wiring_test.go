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

// TestNewUIServerWiresBranding locks that the favicon and stylesheets from
// config reach the served shell through the real constructor (#1289).
func TestNewUIServerWiresBranding(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.UI.FaviconURL = "https://cdn.example.com/f.png"
	cfg.UI.StylesheetURLs = []string{"https://fonts.example.com/a.css"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv, _ := newUIServer(cfg, logger)
	rec := httptest.NewRecorder()
	srv.Index(rec, "/")

	body := rec.Body.String()
	if !strings.Contains(body, "https://cdn.example.com/f.png") || !strings.Contains(body, "https://fonts.example.com/a.css") {
		t.Errorf("served shell lacks the configured branding:\n%s", body)
	}
}

// TestUIThemeFromConfig locks that ui.theme reaches /ui/config as raw JSON and
// that an unset theme stays nil (served as null).
func TestUIThemeFromConfig(t *testing.T) {
	cfg := &config.ServerConfig{}
	if got := uiTheme(cfg); got != nil {
		t.Errorf("uiTheme(unset) = %s, want nil", got)
	}
	cfg.UI.Theme = `{"icon":"/b.svg"}`
	if got := uiTheme(cfg); string(got) != `{"icon":"/b.svg"}` {
		t.Errorf("uiTheme = %s, want the configured JSON", got)
	}
}
