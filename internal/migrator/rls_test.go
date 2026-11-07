package migrator

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintRLSVerdicts(t *testing.T) {
	on := rlsState{table: "dags", enabled: true, forced: true, policy: true, existing: true}
	off := rlsState{table: "pools", policy: true, existing: true}
	for _, tc := range []struct {
		name   string
		states []rlsState
		want   []string
	}{
		{"all on", []rlsState{on, on}, []string{"row level security: on"}},
		{"all off", []rlsState{off, off}, []string{"row level security: off"}},
		{"mixed", []rlsState{on, off}, []string{"row level security: partial"}},
		{"enabled not forced", []rlsState{{table: "roles", enabled: true, policy: true, existing: true}}, []string{"enabled=true forced=false", "partial"}},
		{"missing table", []rlsState{{table: "users"}}, []string{"users", "missing", "partial"}},
		{"no policy", []rlsState{{table: "variables", existing: true}}, []string{"off, no policy"}},
	} {
		var buf bytes.Buffer
		printRLS(&buf, tc.states)
		for _, w := range tc.want {
			if !strings.Contains(buf.String(), w) {
				t.Errorf("%s: output %q lacks %q", tc.name, buf.String(), w)
			}
		}
	}
}

func TestParseCommandRLS(t *testing.T) {
	for _, a := range []string{"on", "off", "status"} {
		c, err := parseCommand([]string{"rls", a})
		if err != nil || c.name != "rls" || c.action != a {
			t.Fatalf("rls %s: %+v, %v", a, c, err)
		}
	}
	for _, args := range [][]string{{"rls"}, {"rls", "maybe"}, {"rls", "on", "now"}} {
		if _, err := parseCommand(args); err == nil {
			t.Fatalf("%v: want an error", args)
		}
	}
}
