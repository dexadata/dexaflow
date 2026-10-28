package ui

import (
	"io/fs"
	"strings"
	"testing"
)

// TestSPAClearSendsRunOnLatestVersion: the Clear dialog's "Run with latest
// bundle version" checkbox must still reach the server.
//
// The control plane now honors `run_on_latest_version` on clearTaskInstances
// (ADR 0020, 2026-09-15 amendment). Before that it ignored the field, so the
// checkbox the SPA has always rendered was inert: unticking it changed nothing,
// and the UI told the user something untrue.
//
// The server half is covered by handler and storage tests. This covers the half
// those cannot see — that the shipped bundle actually sends the field — because
// the SPA is vendored: an Airflow upgrade that renamed or dropped it would make
// the checkbox inert again, silently, and every Go test would stay green.
//
// It is a string search over a minified bundle, which is a weak instrument; it
// is here because it costs nothing and fails loudly on the one event that
// matters (a bundle upgrade). A browser test proving the checkbox is wired to
// the request lives in test/ui-contract/clear_version_toggle.py.
func TestSPAClearSendsRunOnLatestVersion(t *testing.T) {
	assets := Assets()
	var checked int
	var found bool
	err := fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return err
		}
		b, rerr := fs.ReadFile(assets, p)
		if rerr != nil {
			return rerr
		}
		checked++
		// Anchored on only_failed, which the clear request body also carries:
		// run_on_latest_version alone would also match the Backfill form, which
		// has its own copy of the field and is not what this guards.
		s := string(b)
		for _, i := range indexesOf(s, "only_failed") {
			window := s[i:min(i+120, len(s))]
			if strings.Contains(window, "run_on_latest_version") {
				found = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded bundle: %v", err)
	}
	if checked == 0 {
		t.Skip("no JS in the embedded bundle (placeholder build; run `make fetch-airflow-ui`)")
	}
	if !found {
		t.Errorf("no clear request body in the embedded SPA carries run_on_latest_version next to only_failed "+
			"(%d JS files searched); the Clear dialog's version toggle is inert again — see ADR 0020", checked)
	}
}

func indexesOf(s, sub string) []int {
	var out []int
	for i := 0; ; {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return out
		}
		out = append(out, i+j)
		i += j + len(sub)
	}
}

// TestSPAClearConfirmSendsDryRunFalse: the Clear dialog's CONFIRM must send
// dry_run explicitly.
//
// Since #1137 an omitted dry_run previews, matching Apache Airflow 3's
// `dry_run: bool = True`. That is the right default for API clients, and it puts
// the embedded SPA one bundle upgrade away from a silent failure: if a future
// vendored bundle stopped putting dry_run in the confirm body, the Clear button
// would return 200 with the affected set and clear nothing, which reads to a
// user as "clear does nothing" with no error anywhere.
//
// The current bundle sends dry_run:!1 (false) on the confirm mutations and
// injects dry_run:!0 (true) into the preview queries, so both literals must be
// present. Same weak instrument as the test above, same reason: it costs nothing
// and fails loudly on the one event that matters.
func TestSPAClearConfirmSendsDryRunFalse(t *testing.T) {
	assets := Assets()
	var checked int
	var confirm, preview bool
	err := fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return err
		}
		b, rerr := fs.ReadFile(assets, p)
		if rerr != nil {
			return rerr
		}
		checked++
		s := string(b)
		confirm = confirm || strings.Contains(s, "dry_run:!1")
		preview = preview || strings.Contains(s, "dry_run:!0")
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded bundle: %v", err)
	}
	if checked == 0 {
		t.Skip("no JS in the embedded bundle (placeholder build; run `make fetch-airflow-ui`)")
	}
	if !confirm {
		t.Errorf("no request body in the embedded SPA sends dry_run=false (%d JS files searched); "+
			"an omitted dry_run previews, so the Clear dialog's confirm would silently clear nothing", checked)
	}
	if !preview {
		t.Errorf("no request body in the embedded SPA sends dry_run=true (%d JS files searched); "+
			"the Clear dialog's preview would be executing instead of previewing", checked)
	}
}
