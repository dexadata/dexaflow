package main

import (
	"testing"
	"time"
)

// TestEnvInt locks the fail-open-to-disabled convention for the warm-worker caps:
// a valid non-negative integer parses; unset, empty, non-numeric, or negative all
// yield 0 (= "no bound"), so a missing or malformed cap disables that bound rather
// than crashing the agent (ADR 0058 N1d-d).
func TestEnvInt(t *testing.T) {
	cases := []struct {
		name, val string
		set       bool
		want      int
	}{
		{name: "valid", val: "50", set: true, want: 50},
		{name: "zero", val: "0", set: true, want: 0},
		{name: "unset", set: false, want: 0},
		{name: "empty", val: "", set: true, want: 0},
		{name: "non-numeric", val: "abc", set: true, want: 0},
		{name: "negative", val: "-5", set: true, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const key = "LEOFLOW_TEST_CAP"
			if tc.set {
				t.Setenv(key, tc.val)
			}
			if got := envInt(key); got != tc.want {
				t.Errorf("envInt(%q=%q set=%v) = %d, want %d", key, tc.val, tc.set, got, tc.want)
			}
		})
	}
}

// TestEnvSeconds locks that an integer-seconds env var becomes the matching
// Duration, and that a missing/invalid value is 0 (= "no bound").
func TestEnvSeconds(t *testing.T) {
	const key = "LEOFLOW_TEST_CAP_SECONDS"
	t.Run("valid", func(t *testing.T) {
		t.Setenv(key, "3600")
		if got := envSeconds(key); got != time.Hour {
			t.Errorf("envSeconds(3600) = %v, want 1h", got)
		}
	})
	t.Run("unset-is-zero", func(t *testing.T) {
		if got := envSeconds("LEOFLOW_TEST_CAP_UNSET"); got != 0 {
			t.Errorf("envSeconds(unset) = %v, want 0", got)
		}
	})
}
