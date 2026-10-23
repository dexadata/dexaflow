package cli

import (
	"errors"
	"fmt"
	"sort"
	"testing"
)

// TestRemoveMissingDagsHappyPath pins the core contract of the Lite watcher's
// set-diff (issue #345): when a DAG project disappears from the workspace
// between two ticks, the watcher calls DELETE on the API so the registry stops
// showing a "ghost" DAG. Without this, you delete the folder from disk and the
// DAG stays in the UI forever — the exact symptom reported during the
// prealpha.29 hands-on.
func TestRemoveMissingDagsHappyPath(t *testing.T) {
	t.Parallel()
	lastSeen := map[string]struct{}{
		"hello":   {},
		"sales":   {},
		"reports": {},
	}
	current := map[string]struct{}{ // reports/ was deleted from disk
		"hello": {},
		"sales": {},
	}
	var deleted []string
	deleteDag := func(dagID string) error {
		deleted = append(deleted, dagID)
		return nil
	}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, formatLog(format, args...))
	}

	newSeen := removeMissingDags(lastSeen, current, deleteDag, logf)

	if got, want := deleted, []string{"reports"}; !equalSlice(got, want) {
		t.Errorf("deleted = %v, want %v", got, want)
	}
	if _, still := newSeen["reports"]; still {
		t.Errorf("reports should be dropped from newSeen on success; got %v", keys(newSeen))
	}
	if _, kept := newSeen["hello"]; !kept {
		t.Errorf("hello should stay in newSeen (still present on disk); got %v", keys(newSeen))
	}
	// At least one user-visible log line should name the dropped DAG so the
	// operator sees what happened — silent removal would be worse than the
	// original bug.
	found := false
	for _, l := range logs {
		if contains(l, "reports") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a log line naming the removed dag %q; got %v", "reports", logs)
	}
}

// TestRemoveMissingDagsNewlyAdded pins the "first sight" rule: a DAG that
// appeared this tick (not in lastSeen) must NOT trigger a delete, even though
// the function sees it for the first time. Otherwise the very first tick of a
// fresh Lite would try to delete every DAG it just discovered.
func TestRemoveMissingDagsNewlyAdded(t *testing.T) {
	t.Parallel()
	lastSeen := map[string]struct{}{} // first tick, nothing seen yet
	current := map[string]struct{}{"hello": {}, "sales": {}}
	var deleted []string
	deleteDag := func(dagID string) error {
		deleted = append(deleted, dagID)
		return nil
	}
	newSeen := removeMissingDags(lastSeen, current, deleteDag, func(string, ...any) {})

	if len(deleted) != 0 {
		t.Errorf("first-tick newly-added DAGs should not be deleted; got %v", deleted)
	}
	if _, ok := newSeen["hello"]; !ok {
		t.Error("newly-added hello should be tracked in newSeen")
	}
	if _, ok := newSeen["sales"]; !ok {
		t.Error("newly-added sales should be tracked in newSeen")
	}
}

// TestRemoveMissingDagsRetryOnError covers the failure path the production
// flow has to survive: the control plane returns 500 / network blips / etc.
// We must NOT drop the DAG from lastSeen on error — otherwise the next tick
// thinks it's already gone and never retries. The contract: failures are
// logged and the DAG stays "seen" so the next tick tries again.
func TestRemoveMissingDagsRetryOnError(t *testing.T) {
	t.Parallel()
	lastSeen := map[string]struct{}{
		"sticky": {},
	}
	current := map[string]struct{}{} // sticky is gone from disk
	tries := 0
	deleteDag := func(dagID string) error {
		tries++
		return errors.New("simulated API 500")
	}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, formatLog(format, args...))
	}
	newSeen := removeMissingDags(lastSeen, current, deleteDag, logf)

	if tries != 1 {
		t.Errorf("deleteDag should be called once; got %d", tries)
	}
	if _, kept := newSeen["sticky"]; !kept {
		t.Error("on delete error, dag must stay in newSeen so the next tick retries")
	}
	// The user has to see the error — silent retry would mask a permanent
	// permission problem.
	found := false
	for _, l := range logs {
		if contains(l, "sticky") && (contains(l, "fail") || contains(l, "✗")) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected an error log naming sticky; got %v", logs)
	}
}

// TestRemoveMissingDagsNoChange pins the no-op contract: every DAG present
// last tick is still present, so deleteDag must not be called and newSeen
// must equal current.
func TestRemoveMissingDagsNoChange(t *testing.T) {
	t.Parallel()
	lastSeen := map[string]struct{}{
		"a": {},
		"b": {},
	}
	current := map[string]struct{}{"a": {}, "b": {}}
	deleteDag := func(dagID string) error {
		t.Fatalf("deleteDag must not be called when nothing was removed; got call for %q", dagID)
		return nil
	}
	newSeen := removeMissingDags(lastSeen, current, deleteDag, func(string, ...any) {})

	if got, want := keys(newSeen), []string{"a", "b"}; !equalSlice(got, want) {
		t.Errorf("newSeen = %v, want %v", got, want)
	}
}

// helpers — keep them inline so the test file is self-contained.

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalSlice(a, b []string) bool {
	a2 := append([]string(nil), a...)
	b2 := append([]string(nil), b...)
	sort.Strings(a2)
	sort.Strings(b2)
	if len(a2) != len(b2) {
		return false
	}
	for i := range a2 {
		if a2[i] != b2[i] {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > len(sub) && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func formatLog(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
