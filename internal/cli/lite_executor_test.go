package cli

import (
	"strings"
	"testing"
)

// TestResolveExecutor pins the auto-detect default: `dexaflow lite` (executor
// "auto") uses k3d when Docker is available, else falls back to the subprocess
// executor so Lite still runs Docker-free; an explicit choice is honored.
func TestResolveExecutor(t *testing.T) {
	cases := []struct {
		flag   string
		docker bool
		want   string
	}{
		{"auto", true, "k8s"},
		{"auto", false, "subprocess"},
		{"k8s", false, "k8s"},
		{"subprocess", true, "subprocess"},
	}
	for _, c := range cases {
		if got := resolveExecutor(c.flag, c.docker); got != c.want {
			t.Errorf("resolveExecutor(%q, %v) = %q, want %q", c.flag, c.docker, got, c.want)
		}
	}
}

// TestExecutorNote pins the user-facing line for the resolved executor, naming a
// present-but-unresponsive Docker so the user fixes it (or forces a choice)
// instead of seeing the misleading "no Docker detected" (#403).
func TestExecutorNote(t *testing.T) {
	cases := []struct {
		mode    string
		present bool
		wantSub string
	}{
		{"k8s", true, "k3d executor"},
		{"subprocess", true, "not responding"},
		{"subprocess", false, "no Docker detected"},
	}
	for _, c := range cases {
		if got := executorNote(c.mode, c.present); !strings.Contains(got, c.wantSub) {
			t.Errorf("executorNote(%q, present=%v) = %q, want substring %q", c.mode, c.present, got, c.wantSub)
		}
	}
}
