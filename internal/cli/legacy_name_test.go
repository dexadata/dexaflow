package cli

import (
	"strings"
	"testing"
)

// The CLI is called dexaflow. Help, usage and completion all derive from the
// root command's Use, so asserting it here locks every surface that prints it.
func TestRootCommandIsNamedDexaflow(t *testing.T) {
	if got := NewRootCommand().Use; got != "dexaflow" {
		t.Fatalf("root command Use = %q, want %q", got, "dexaflow")
	}
}

// Installs created before the rename keep a `leoflow` entry point (a symlink
// to the same binary). Running it must keep working and say, once, what the
// command is called now. No removal is announced: the old name stays.
func TestLegacyNameNotice(t *testing.T) {
	cases := []struct {
		argv0 string
		want  bool
	}{
		{"leoflow", true},
		{"/usr/local/bin/leoflow", true},
		{`C:\tools\leoflow.exe`, true},
		{"dexaflow", false},
		{"/home/u/.local/bin/dexaflow", false},
		{"dexaflow.exe", false},
		// A binary that merely contains the old name is not the old entry point.
		{"leoflow-wrapper", false},
		{"", false},
	}
	for _, c := range cases {
		got := legacyNameNotice(c.argv0)
		if (got != "") != c.want {
			t.Errorf("legacyNameNotice(%q) = %q, want notice=%v", c.argv0, got, c.want)
			continue
		}
		if got == "" {
			continue
		}
		if !strings.Contains(got, "dexaflow") {
			t.Errorf("notice %q does not name the new command", got)
		}
		for _, word := range []string{"removed", "deprecated in", "will stop"} {
			if strings.Contains(strings.ToLower(got), word) {
				t.Errorf("notice %q announces a removal (%q); the old name stays", got, word)
			}
		}
	}
}
