package cli

import "testing"

// TestCompileSummaryNoImage covers #D10 from the Lite dogfood audit (#212):
// when `--image` is not set, the success line previously printed a dangling
// comma — `(image , version dev)` — making the output look broken. The empty
// case must render a clean substitute like `no image`.
func TestCompileSummaryNoImage(t *testing.T) {
	t.Parallel()
	got := compileSummary("dag.py", "dag.json", "", "dev")
	want := "Compiled dag.py -> dag.json (no image, version dev)\n"
	if got != want {
		t.Errorf("empty image summary mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestCompileSummaryWithImage pins the happy-path format so the no-image fix
// does not silently rewrite the populated case.
func TestCompileSummaryWithImage(t *testing.T) {
	t.Parallel()
	got := compileSummary("dag.py", "dag.json", "registry/etl:v1", "dev")
	want := "Compiled dag.py -> dag.json (image registry/etl:v1, version dev)\n"
	if got != want {
		t.Errorf("populated image summary mismatch\n got: %q\nwant: %q", got, want)
	}
}
