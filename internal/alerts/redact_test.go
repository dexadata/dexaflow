package alerts

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// An alert URL can carry its credential in the path or query (a Slack
// incoming webhook is one). Send's errors end up in the server log, so they
// name the endpoint by scheme and host only.

func TestSendErrorDoesNotLeakTheWebhookSecret(t *testing.T) {
	n := NewNotifier(&http.Client{Timeout: 2 * time.Second})
	// Nothing listens on port 1, so the dial fails and the client returns a
	// *url.Error, which would otherwise print the whole URL.
	err := n.Send(context.Background(), "slack", "http://127.0.0.1:1/services/T0/B0/s3cr3t?token=q5ecret", nil, "m", Event{})
	if err == nil {
		t.Fatal("Send to a closed port succeeded")
	}
	msg := err.Error()
	if strings.Contains(msg, "s3cr3t") || strings.Contains(msg, "q5ecret") {
		t.Errorf("error leaks the webhook secret: %s", msg)
	}
	if !strings.Contains(msg, "127.0.0.1:1") {
		t.Errorf("error should still name the host: %s", msg)
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Errorf("error chain lost the *url.Error (callers match on its cause): %v", err)
	}
}

func TestSendBadURLErrorDoesNotLeakIt(t *testing.T) {
	n := NewNotifier(&http.Client{Timeout: time.Second})
	err := n.Send(context.Background(), "webhook", "http://host\x7f/hooks/s3cr3t", nil, "m", Event{})
	if err == nil {
		t.Fatal("Send with an invalid URL succeeded")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("error leaks the URL: %s", err)
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://hooks.slack.com/services/T/B/x":         "https://hooks.slack.com",
		"https://user:pass@example.com:8443/p?token=abc": "https://example.com:8443",
		"not a url\x7f":    "(unparseable URL)",
		"/relative/secret": "(URL without a host)",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
