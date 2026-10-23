package cli

import (
	"strings"
	"testing"
)

func TestIsLoopback(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"http://localhost:8080":   true,
		"http://127.0.0.1":        true,
		"http://[::1]:8080":       true,
		"https://pro.example.com": false,
		"http://10.0.0.5:8080":    false,
		"://malformed":            false, // unparseable URL is treated as non-loopback
	}
	for url, want := range cases {
		if got := isLoopback(url); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestShouldConfirm(t *testing.T) {
	t.Parallel()
	// Prompt only when interactive, not --yes, and a real (non-loopback) server.
	if !shouldConfirm("https://pro", false, true) {
		t.Error("want confirm for an interactive deploy to a real server")
	}
	if shouldConfirm("https://pro", true, true) {
		t.Error("--yes must skip the prompt")
	}
	if shouldConfirm("https://pro", false, false) {
		t.Error("non-interactive (CI) must skip the prompt")
	}
	if shouldConfirm("http://localhost:8080", false, true) {
		t.Error("loopback (Lite) must skip the prompt")
	}
}

func TestConfirmDeploy(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "n\n": false, "\n": false, "nope\n": false} {
		var out strings.Builder
		if got := confirmDeploy(strings.NewReader(in), &out, "etl", "https://pro"); got != want {
			t.Errorf("confirmDeploy(%q) = %v, want %v", in, got, want)
		}
		if !strings.Contains(out.String(), "Deploy etl") {
			t.Errorf("prompt = %q, want it to name the target", out.String())
		}
	}
}
