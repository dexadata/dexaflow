package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/egress"
)

func postTo(t *testing.T, c *http.Client, url string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	return err
}

// TestAlertHTTPClientGuardsWhenEnabled: with the block on, an alert pointed at
// the control plane's loopback is refused before it connects.
func TestAlertHTTPClientGuardsWhenEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c, err := alertHTTPClient(config.AlertsSection{BlockPrivateDestinations: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := postTo(t, c, srv.URL); !errors.Is(err, egress.ErrBlockedDestination) {
		t.Errorf("POST to loopback = %v, want ErrBlockedDestination", err)
	}
}

// TestAlertHTTPClientUnguardedByDefault: the gate is off by default and the
// client behaves as before, so an in-cluster alert endpoint keeps working.
func TestAlertHTTPClientUnguardedByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c, err := alertHTTPClient(config.AlertsSection{})
	if err != nil {
		t.Fatal(err)
	}
	if err := postTo(t, c, srv.URL); err != nil {
		t.Errorf("POST with the guard off = %v, want success", err)
	}
	if c.Timeout != alertHTTPTimeout {
		t.Errorf("timeout = %v, want %v", c.Timeout, alertHTTPTimeout)
	}
}
