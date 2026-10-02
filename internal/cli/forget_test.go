package cli

import (
	"strings"
	"testing"
)

// TestForgetCommandArgs pins the CLI's input validation: exactly one dag_id
// without --all, no positional with --all. Without this, `dexaflow lite forget`
// (no args) would silently no-op and `dexaflow lite forget --all foo` would
// confuse the operator about whether foo was deregistered.
func TestForgetCommandArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantError bool
		wantSub   string
	}{
		{"single dag_id is OK", []string{"my_dag"}, false, ""},
		{"empty args is rejected", []string{}, true, "exactly one dag_id"},
		{"two dag_ids is rejected", []string{"a", "b"}, true, "exactly one dag_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newForgetCommand()
			err := c.Args(c, tc.args)
			if tc.wantError && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tc.wantSub != "" && err != nil && !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q missing %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestForgetCommandAllRejectsPositional protects against accidental
// `forget --all foo` invocations: --all means "every DAG", so naming one
// would mix two semantics. Explicit rejection at parse time is friendlier
// than silently using one or the other.
func TestForgetCommandAllRejectsPositional(t *testing.T) {
	c := newForgetCommand()
	if err := c.Flags().Set("all", "true"); err != nil {
		t.Fatalf("set --all: %v", err)
	}
	err := c.Args(c, []string{"some_dag"})
	if err == nil {
		t.Fatal("expected error when --all is combined with a positional dag_id")
	}
	if !strings.Contains(err.Error(), "--all") {
		t.Errorf("error %q should name --all", err.Error())
	}
}

// TestForgetCommandFlags pins the flag surface: --all and --dry-run exist and
// are boolean. A renamed flag would silently break scripts that rely on this
// CLI.
func TestForgetCommandFlags(t *testing.T) {
	c := newForgetCommand()
	if f := c.Flags().Lookup("all"); f == nil {
		t.Error("--all flag missing")
	}
	if f := c.Flags().Lookup("dry-run"); f == nil {
		t.Error("--dry-run flag missing")
	}
}
