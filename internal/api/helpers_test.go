package api

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

func TestAtoiOr(t *testing.T) {
	cases := []struct {
		raw string
		def int
		out int
	}{
		{"", 5, 5},     // empty -> default
		{"42", 5, 42},  // valid
		{"nope", 5, 5}, // garbage -> default
		{"-3", 5, -3},  // negative parses (clamping is clampLimit's job)
		{"0", 9, 0},    // zero parses
	}
	for _, c := range cases {
		if got := atoiOr(c.raw, c.def); got != c.out {
			t.Errorf("atoiOr(%q, %d) = %d, want %d", c.raw, c.def, got, c.out)
		}
	}
}

func TestClampLimit(t *testing.T) {
	cases := []struct {
		raw              string
		def, upper, want int
	}{
		{"", 50, 100, 50},     // empty -> default
		{"10", 50, 100, 10},   // within range
		{"500", 50, 100, 100}, // above upper -> clamped
		{"0", 50, 100, 50},    // non-positive -> default
		{"-7", 50, 100, 50},   // negative -> default
		{"junk", 50, 100, 50}, // garbage -> default
	}
	for _, c := range cases {
		if got := clampLimit(c.raw, c.def, c.upper); got != c.want {
			t.Errorf("clampLimit(%q, %d, %d) = %d, want %d", c.raw, c.def, c.upper, got, c.want)
		}
	}
}

func TestNonZeroTime(t *testing.T) {
	if nonZeroTime(time.Time{}) != nil {
		t.Error("the zero time should map to nil")
	}
	now := time.Now()
	if p := nonZeroTime(now); p == nil || !p.Equal(now) {
		t.Errorf("a non-zero time should map to itself, got %v", p)
	}
}

func TestStrSafe(t *testing.T) {
	if strSafe("", "fallback") != "fallback" {
		t.Error("empty -> fallback")
	}
	if strSafe("value", "fallback") != "value" {
		t.Error("non-empty -> value")
	}
}

func TestRFC3339Ptr(t *testing.T) {
	if rfc3339Ptr(nil) != nil {
		t.Error("nil time -> nil string")
	}
	tm := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if p := rfc3339Ptr(&tm); p == nil || *p != "2026-01-02T03:04:05Z" {
		t.Errorf("rfc3339Ptr = %v, want 2026-01-02T03:04:05Z", p)
	}
}

func TestOperatorName(t *testing.T) {
	cases := map[domain.TaskType]string{
		domain.TaskTypePython:     "PythonOperator",
		domain.TaskTypeBash:       "BashOperator",
		domain.TaskType("custom"): "custom", // unknown -> passthrough
	}
	for in, want := range cases {
		if got := operatorName(in); got != want {
			t.Errorf("operatorName(%q) = %q, want %q", in, got, want)
		}
	}
}
