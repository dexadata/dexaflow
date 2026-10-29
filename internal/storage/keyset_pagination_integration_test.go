//go:build integration

package storage_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// seedKeysetRuns registers a DAG in the default tenant and inserts runs whose
// logical dates collide in pairs, so only the run id orders a tie.
func seedKeysetRuns(t *testing.T, n int) (dagID string, repoRuns func() []domain.DagRun) {
	t.Helper()
	repo, _, ctx := openRepo(t)
	dagID = fmt.Sprintf("keyset_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	states := []domain.DagRunState{domain.DagRunStateSuccess, domain.DagRunStateFailed}
	for i := range n {
		run := domain.DagRun{
			RunID: fmt.Sprintf("manual__%03d", i), LogicalDate: base.Add(time.Duration(i/2) * time.Hour),
			State: states[i%3%2], RunType: "manual",
		}
		if _, err := repo.CreateDagRun(ctx, "default", dagID, run); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
	}
	return dagID, func() []domain.DagRun {
		all, _, err := repo.ListDagRuns(ctx, "default", dagID, 1000, 0)
		if err != nil {
			t.Fatal(err)
		}
		return all
	}
}

func runIDs(runs []domain.DagRun) string {
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.RunID
	}
	return strings.Join(ids, ",")
}

// TestListDagRunsAfterMatchesOffsetOrderIntegration: walking a DAG's runs by
// cursor visits every run exactly once, in the offset order, ties broken by
// run id, and reports the same total.
func TestListDagRunsAfterMatchesOffsetOrderIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, all := seedKeysetRuns(t, 9)
	want := all()
	if len(want) != 9 {
		t.Fatalf("seeded %d runs, want 9", len(want))
	}
	for i := 1; i < len(want); i++ {
		a, b := want[i-1], want[i]
		if a.LogicalDate.Equal(b.LogicalDate) && a.RunID < b.RunID {
			t.Fatalf("offset order breaks a tie ascending: %s before %s", a.RunID, b.RunID)
		}
	}
	var got []domain.DagRun
	cur := domain.PageCursor{At: want[0].LogicalDate.Add(time.Hour), Key: ""}
	for page := 0; page < 10; page++ {
		rows, total, err := repo.ListDagRunsAfter(ctx, "default", dagID, nil, cur, 2)
		if err != nil {
			t.Fatalf("ListDagRunsAfter: %v", err)
		}
		if total != 9 {
			t.Errorf("total = %d, want 9", total)
		}
		if len(rows) == 0 {
			break
		}
		got = append(got, rows...)
		last := rows[len(rows)-1]
		cur = domain.PageCursor{At: last.LogicalDate, Key: last.RunID}
	}
	if runIDs(got) != runIDs(want) {
		t.Fatalf("cursor walk = %s\nwant          %s", runIDs(got), runIDs(want))
	}
}

// TestListDagRunsAfterFiltersStatesIntegration: the state filter applies in
// the query, to the rows and to the total.
func TestListDagRunsAfterFiltersStatesIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, all := seedKeysetRuns(t, 9)
	var wantFailed []domain.DagRun
	for _, r := range all() {
		if r.State == domain.DagRunStateFailed {
			wantFailed = append(wantFailed, r)
		}
	}
	rows, total, err := repo.ListDagRunsAfter(ctx, "default", dagID, []string{"failed"},
		domain.PageCursor{At: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if runIDs(rows) != runIDs(wantFailed) || total != len(wantFailed) {
		t.Fatalf("failed runs = %s (total %d), want %s (%d)", runIDs(rows), total, runIDs(wantFailed), len(wantFailed))
	}
}

// TestListDagRunsAfterIsTenantScopedIntegration: another tenant's DAG id does
// not resolve, so its runs never leak through a cursor.
func TestListDagRunsAfterIsTenantScopedIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, _ := seedKeysetRuns(t, 2)
	if _, _, err := repo.ListDagRunsAfter(ctx, "no_such_tenant_keyset", dagID, nil,
		domain.PageCursor{At: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}, 10); err == nil {
		t.Fatal("ListDagRunsAfter served a DAG from another tenant")
	}
}

// TestListAuditLogsAfterMatchesOffsetOrderIntegration: the event log walks by
// cursor in the offset order (newest first, id breaking ties), with the DAG
// filter applied.
func TestListAuditLogsAfterMatchesOffsetOrderIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("keyset_audit_%d", time.Now().UnixNano())
	for i := range 7 {
		if err := repo.RecordTaskActionAudit(ctx, "default", "", fmt.Sprintf("keyset.%d", i), dagID, "run", "", 0); err != nil {
			t.Fatalf("record audit: %v", err)
		}
	}
	want, total, err := repo.ListAuditLogs(ctx, "default", dagID, 100, 0)
	if err != nil || total != 7 {
		t.Fatalf("offset list = %d entries, %v", total, err)
	}
	var got []domain.AuditLogEntry
	cur := domain.PageCursor{At: want[0].When.Add(time.Hour), Key: "0"}
	for page := 0; page < 10; page++ {
		rows, n, err := repo.ListAuditLogsAfter(ctx, "default", dagID, cur, 3)
		if err != nil {
			t.Fatalf("ListAuditLogsAfter: %v", err)
		}
		if n != 7 {
			t.Errorf("total = %d, want 7", n)
		}
		if len(rows) == 0 {
			break
		}
		got = append(got, rows...)
		last := rows[len(rows)-1]
		cur = domain.PageCursor{At: last.When, Key: fmt.Sprint(last.ID)}
	}
	if len(got) != len(want) {
		t.Fatalf("cursor walk returned %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("entry %d = %d, want %d", i, got[i].ID, want[i].ID)
		}
	}
	if _, _, err := repo.ListAuditLogsAfter(ctx, "default", dagID, domain.PageCursor{At: time.Now(), Key: "x"}, 3); err == nil {
		t.Error("a non-numeric audit cursor key was accepted")
	}
}

// TestKeysetPagesTrustTheCursorTotalIntegration: a cursor that carries the
// total its first page counted is answered with it, without a COUNT (#1612);
// one without a total counts.
func TestKeysetPagesTrustTheCursorTotalIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, _ := seedKeysetRuns(t, 3)
	future := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		states []string
	}{{"all states", nil}, {"failed", []string{"failed"}}} {
		_, total, err := repo.ListDagRunsAfter(ctx, "default", dagID, tc.states, domain.PageCursor{At: future, Total: 42}, 10)
		if err != nil || total != 42 {
			t.Errorf("%s: runs total with a carried 42 = %d, %v", tc.name, total, err)
		}
	}
	_, total, err := repo.ListDagRunsAfter(ctx, "default", dagID, nil, domain.PageCursor{At: future}, 10)
	if err != nil || total != 3 {
		t.Errorf("runs total without a carried one = %d, %v, want the count 3", total, err)
	}

	auditDag := fmt.Sprintf("keyset_audit_total_%d", time.Now().UnixNano())
	for i := range 2 {
		if err := repo.RecordTaskActionAudit(ctx, "default", "", fmt.Sprintf("keyset.%d", i), auditDag, "run", "", 0); err != nil {
			t.Fatalf("record audit: %v", err)
		}
	}
	if _, n, err := repo.ListAuditLogsAfter(ctx, "default", auditDag, domain.PageCursor{At: future, Key: "0", Total: 42}, 10); err != nil || n != 42 {
		t.Errorf("audit total with a carried 42 = %d, %v", n, err)
	}
	if _, n, err := repo.ListAuditLogsAfter(ctx, "default", auditDag, domain.PageCursor{At: future, Key: "0"}, 10); err != nil || n != 2 {
		t.Errorf("audit total without a carried one = %d, %v, want the count 2", n, err)
	}
}

// TestListDagRunsPageMatchesListDagRunsIntegration (#1328, #1616): the
// count-free page the grid and the run scans read returns, for every limit
// and offset, exactly the runs ListDagRuns returns, in the same order, with
// the same fields; an offset past the end is an empty page, not an error; and
// another tenant's DAG id does not resolve.
func TestListDagRunsPageMatchesListDagRunsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, _ := seedKeysetRuns(t, 7)
	for _, limit := range []int{1, 3, 7, 10} {
		for offset := 0; offset <= 8; offset++ {
			want, total, err := repo.ListDagRuns(ctx, "default", dagID, limit, offset)
			if err != nil {
				t.Fatalf("ListDagRuns(%d, %d): %v", limit, offset, err)
			}
			if total != 7 {
				t.Fatalf("ListDagRuns total = %d, want 7", total)
			}
			got, err := repo.ListDagRunsPage(ctx, "default", dagID, limit, offset)
			if err != nil {
				t.Fatalf("ListDagRunsPage(%d, %d): %v", limit, offset, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("limit %d offset %d: ListDagRunsPage = %s, ListDagRuns = %s", limit, offset, runIDs(got), runIDs(want))
			}
			if wantLen := max(0, min(limit, 7-offset)); len(got) != wantLen {
				t.Errorf("limit %d offset %d: %d runs, want %d", limit, offset, len(got), wantLen)
			}
		}
	}
	if _, err := repo.ListDagRunsPage(ctx, "no_such_tenant_page", dagID, 10, 0); err == nil {
		t.Error("ListDagRunsPage served a DAG from another tenant")
	}
}
