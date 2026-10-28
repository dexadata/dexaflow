package cli

import (
	"strings"
	"testing"
)

// TestRootCommandsHaveGroups covers #D1 from the Lite dogfood audit (#212):
// `leoflow --help` listed every command in one long unordered block of 17
// entries. New users could not tell authoring commands from runtime, runtime
// from inspection, or admin from anything. Cobra command groups break the
// list into navigable sections.
func TestRootCommandsHaveGroups(t *testing.T) {
	want := map[string]string{
		// Authoring loop — write, check, package, push a DAG.
		"init":     "authoring",
		"validate": "authoring",
		"compile":  "authoring",
		"push":     "authoring",
		// Runtime — start the control plane(s).
		"lite":   "runtime",
		"server": "runtime",
		// Inspection — query state of registered DAGs / runs / auth tokens.
		"dags": "inspection",
		"runs": "inspection",
		"auth": "inspection",
		// Operations — operate a running control plane.
		"admin":    "operations",
		"rollback": "operations",
		// Lifecycle — install, configure, repair, version, retire the host.
		"setup":     "lifecycle",
		"doctor":    "lifecycle",
		"db":        "lifecycle",
		"uninstall": "lifecycle",
		"version":   "lifecycle",
	}

	root := NewRootCommand()
	for _, cmd := range root.Commands() {
		expected, ok := want[cmd.Name()]
		if !ok {
			continue // help/completion/gen-docs — leave ungrouped on purpose.
		}
		if cmd.GroupID != expected {
			t.Errorf("%s GroupID = %q, want %q", cmd.Name(), cmd.GroupID, expected)
		}
	}
}

// TestRootDefinesGroups pins the group set itself so a typo in a GroupID
// fails the GROUP definition, not the assignment.
func TestRootDefinesGroups(t *testing.T) {
	root := NewRootCommand()
	want := map[string]bool{"authoring": false, "runtime": false, "inspection": false, "operations": false, "lifecycle": false}
	for _, g := range root.Groups() {
		if _, ok := want[g.ID]; ok {
			want[g.ID] = true
		}
	}
	for id, ok := range want {
		if !ok {
			t.Errorf("root command is missing group %q", id)
		}
	}
}

// TestDBHelpReferencesLite covers #D2: the `db` command's Short used to read
// "Manage the local dev database (leoflow_dev)" — stale after the Dev→Lite
// product rename. The underlying database name `leoflow_dev` is preserved
// for backward compatibility, but the help should brand it as the Lite
// database so new users do not look for a non-existent "dev" edition.
func TestDBHelpReferencesLite(t *testing.T) {
	t.Parallel()
	db := newDBCommand()
	if !strings.Contains(db.Short, "Lite") {
		t.Errorf("db Short should mention Lite (post-rename); got %q", db.Short)
	}
}
