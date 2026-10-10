package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestExcludePathsNegationIsDroppedAndSaidSo covers #1081. expandPattern's
// comment claimed a negation is "passed through untouched"; it returns nil, so
// the caller appends nothing and the entry contributes nothing at all. The
// behavior is the defensible one — order matters in a .dockerignore, the
// leoflow block is appended after whatever the author already wrote, and a `!`
// we emit could resurrect a path an earlier line of THEIRS excluded — but doing
// it silently makes exclude_paths a field that accepts input and discards it.
func TestExcludePathsNegationIsDroppedAndSaidSo(t *testing.T) {
	t.Run("a negation still contributes no pattern", func(t *testing.T) {
		// Pinning the behavior, not just the message: emitting `!keep.txt` into
		// our appended block is the riskier half and must stay unimplemented
		// until it is a deliberate decision.
		if got := expandPattern("!keep.txt"); len(got) != 0 {
			t.Errorf("expandPattern(%q) = %v, want nothing emitted", "!keep.txt", got)
		}
		if got := expandPattern("#a comment"); len(got) != 0 {
			t.Errorf("a comment line must emit nothing; got %v", got)
		}
		if got := expandPattern("   "); len(got) != 0 {
			t.Errorf("a blank entry must emit nothing; got %v", got)
		}
	})

	t.Run("the author is told, by name", func(t *testing.T) {
		var w bytes.Buffer
		dir := t.TempDir()
		cfg := &domain.LeoflowConfig{DagID: "d"}
		cfg.ApplyDefaults()
		cfg.ExcludePaths = append(cfg.ExcludePaths, "!keep.txt", "!also/kept")
		if _, _, err := ensureDockerignore(&w, dir, cfg, false); err != nil {
			t.Fatal(err)
		}
		out := w.String()
		for _, want := range []string{"keep.txt", "also/kept", ".dockerignore"} {
			if !strings.Contains(out, want) {
				t.Errorf("the warning must name the dropped entry and where a negation does work; missing %q in %q", want, out)
			}
		}
	})

	t.Run("nothing is said when there is nothing to say", func(t *testing.T) {
		// A build with no negations must stay quiet: a warning that prints on
		// every build is a warning nobody reads.
		var w bytes.Buffer
		dir := t.TempDir()
		cfg := &domain.LeoflowConfig{DagID: "d"}
		cfg.ApplyDefaults()
		if _, _, err := ensureDockerignore(&w, dir, cfg, false); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(w.String(), "negation") {
			t.Errorf("no negations declared, so nothing should be said; got %q", w.String())
		}
	})

	t.Run("a comment line is dropped without a warning", func(t *testing.T) {
		// `#` is not the author asking for something and being refused; it is a
		// comment. Warning about it would be noise.
		var w bytes.Buffer
		dir := t.TempDir()
		cfg := &domain.LeoflowConfig{DagID: "d"}
		cfg.ApplyDefaults()
		cfg.ExcludePaths = append(cfg.ExcludePaths, "# just a note")
		if _, _, err := ensureDockerignore(&w, dir, cfg, false); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(w.String(), "just a note") {
			t.Errorf("a comment must not be warned about; got %q", w.String())
		}
	})
}
