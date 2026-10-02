package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// deprecatedPythonLine returns a python_version the schema marks deprecated, or
// skips the test. Hard-coding "3.10" here would turn every case below into a
// failure on the day the line is finally dropped, which is the one day nobody
// wants to be debugging the test suite.
func deprecatedPythonLine(t *testing.T) domain.PythonDeprecation {
	t.Helper()
	versions, err := domain.SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}
	for _, v := range versions {
		if d, ok := domain.DeprecatedPythonVersion(v); ok {
			return d
		}
	}
	t.Skip("no python_version is currently deprecated")
	return domain.PythonDeprecation{}
}

// supportedPythonLine returns a python_version with no deprecation note.
func supportedPythonLine(t *testing.T) string {
	t.Helper()
	versions, err := domain.SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}
	for _, v := range versions {
		if _, ok := domain.DeprecatedPythonVersion(v); !ok {
			return v
		}
	}
	t.Fatal("every supported python_version is deprecated")
	return ""
}

// warn runs the warning and returns what a terminal would show.
func warn(cfg *domain.LeoflowConfig) string {
	var out strings.Builder
	warnDeprecatedPython(&out, cfg)
	return out.String()
}

// flatten collapses the word wrapping so a phrase assertion is about the words
// the reader gets, not about where the folding happened to land. Line breaks are
// the subject of exactly one test (the width one), which reads the raw output.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }

// mustContainAll asserts on the RENDERED text, not on "a warning fired".
// Asserting only that output is non-empty is what let the base_image path ship
// with the python_version path's message: both produced bytes, and the bytes
// were wrong.
func mustContainAll(t *testing.T, got string, want ...string) {
	t.Helper()
	flat := flatten(got)
	for _, w := range want {
		if !strings.Contains(flat, flatten(w)) {
			t.Errorf("warning does not contain %q:\n%s", w, got)
		}
	}
}

func mustContainNone(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	flat := flatten(got)
	for _, u := range unwanted {
		if strings.Contains(flat, flatten(u)) {
			t.Errorf("warning must not contain %q:\n%s", u, got)
		}
	}
}

func TestWarnDeprecatedPythonDefaultPath(t *testing.T) {
	d := deprecatedPythonLine(t)
	got := warn(&domain.LeoflowConfig{PythonVersion: d.Version})
	mustContainAll(t, got,
		"warning:",
		"python_version "+d.Version+" is deprecated",
		publishedBaseRepo+":py"+d.Version,
		d.RemoveAfter,
		`Fix: set python_version: "`+d.Replacement+`" in dexaflow.yaml and rebuild.`,
	)
	// The remedy for the field the author actually set must not be the other
	// field's remedy.
	mustContainNone(t, got, "repoint base_image")
}

// The property the whole warning rests on: following the Fix must make the
// warning stop. A base_image pin is used verbatim by resolveBaseImage, so a
// remedy naming python_version is one the author can follow, rebuild, and watch
// reproduce the identical warning — at which point they filter the message and
// the deprecation reaches nobody.
//
// This is the case the original test could not see: it built the config with
// PythonVersion empty and asserted only that a warning fired.
func TestWarnDeprecatedPythonPinnedBaseImageNamesBaseImage(t *testing.T) {
	d := deprecatedPythonLine(t)
	supported := supportedPythonLine(t)
	for name, tc := range map[string]struct{ pinned, wantFixRef string }{
		"moving line tag": {
			publishedBaseRepo + ":py" + d.Version,
			publishedBaseRepo + ":py" + d.Replacement,
		},
		"per-release pin": {
			publishedBaseRepo + ":py" + d.Version + "-v0.4.5",
			publishedBaseRepo + ":py" + d.Replacement + "-v0.4.5",
		},
		"per-release rc": {
			publishedBaseRepo + ":py" + d.Version + "-v0.4.5-rc.1",
			publishedBaseRepo + ":py" + d.Replacement + "-v0.4.5-rc.1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			// python_version is set to a SUPPORTED line, as it is in a real file
			// that pins base_image: reporting "python_version 3.10 is deprecated"
			// to this author describes a file they do not have.
			got := warn(&domain.LeoflowConfig{PythonVersion: supported, BaseImage: tc.pinned})
			mustContainAll(t, got,
				"warning: base_image "+tc.pinned+" pins Python "+d.Version,
				d.RemoveAfter,
				"Fix: repoint base_image to "+tc.wantFixRef+" in dexaflow.yaml and rebuild.",
			)
			// The headline must not blame a field whose value is not deprecated,
			// and the remedy must not send the author to a field that is unused.
			mustContainNone(t, got,
				"python_version "+d.Version+" is deprecated",
				"python_version "+supported+" is deprecated",
				`Fix: set python_version`,
			)
		})
	}
}

// A digest pin is what a supply-chain-conscious author writes, and it is the
// author most likely to still be sitting on an old base. `strings.Cut(ref, ":")`
// read the tag as "py3.10@sha256" and skipped exactly that population.
func TestWarnDeprecatedPythonDigestPinnedBase(t *testing.T) {
	d := deprecatedPythonLine(t)
	const digest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	pinned := publishedBaseRepo + ":py" + d.Version + "-v0.4.5" + digest
	got := warn(&domain.LeoflowConfig{BaseImage: pinned})
	mustContainAll(t, got,
		"warning: base_image "+pinned+" pins Python "+d.Version,
		"Fix: repoint base_image to "+publishedBaseRepo+":py"+d.Replacement+"-v0.4.5",
		// Carrying the old digest over under a new tag would pull the deprecated
		// manifest right back in, so the remedy has to say so.
		"re-pinning its digest",
	)
	// The digest must not leak into the suggested replacement reference.
	mustContainNone(t, got, publishedBaseRepo+":py"+d.Replacement+"-v0.4.5"+digest)
}

// A digest-ONLY pin names a manifest, not a line. Resolving it needs a registry
// round-trip, so the warning stays silent rather than guessing.
func TestWarnDeprecatedPythonSilentOnDigestOnlyPin(t *testing.T) {
	d := deprecatedPythonLine(t)
	ref := publishedBaseRepo + "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := warn(&domain.LeoflowConfig{PythonVersion: d.Version, BaseImage: ref}); got != "" {
		t.Fatalf("a digest-only pin produced a warning:\n%s", got)
	}
}

// A registry port puts a ":" in the reference that is not the tag separator.
func TestWarnDeprecatedPythonSilentOnPortedForeignRegistry(t *testing.T) {
	d := deprecatedPythonLine(t)
	ref := "registry.example.com:5000/team/base:py" + d.Version
	if got := warn(&domain.LeoflowConfig{BaseImage: ref}); got != "" {
		t.Fatalf("a foreign registry with a port produced a warning:\n%s", got)
	}
}

func TestSplitImageRef(t *testing.T) {
	const dig = "sha256:abc"
	cases := []struct{ in, repo, tag, digest string }{
		{"ghcr.io/o/r:py3.10", "ghcr.io/o/r", "py3.10", ""},
		{"ghcr.io/o/r:py3.10@" + dig, "ghcr.io/o/r", "py3.10", dig},
		{"ghcr.io/o/r@" + dig, "ghcr.io/o/r", "", dig},
		{"ghcr.io/o/r", "ghcr.io/o/r", "", ""},
		{"registry:5000/o/r", "registry:5000/o/r", "", ""},
		{"registry:5000/o/r:py3.10", "registry:5000/o/r", "py3.10", ""},
	}
	for _, tc := range cases {
		repo, tag, digest := splitImageRef(tc.in)
		if repo != tc.repo || tag != tc.tag || digest != tc.digest {
			t.Errorf("splitImageRef(%q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.in, repo, tag, digest, tc.repo, tc.tag, tc.digest)
		}
	}
}

func TestWarnDeprecatedPythonSilentOnSupported(t *testing.T) {
	if got := warn(&domain.LeoflowConfig{PythonVersion: supportedPythonLine(t)}); got != "" {
		t.Fatalf("a supported python_version produced a warning:\n%s", got)
	}
}

// A base_image pointing at somebody else's registry means python_version is not
// what the FROM is built from, so warning about it would be wrong and would
// train the author to filter this message out.
func TestWarnDeprecatedPythonSilentOnForeignBaseImage(t *testing.T) {
	d := deprecatedPythonLine(t)
	cfg := &domain.LeoflowConfig{PythonVersion: d.Version, BaseImage: "registry.example.com/team/base:2026-09"}
	if got := warn(cfg); got != "" {
		t.Fatalf("an unrelated base_image produced a warning:\n%s", got)
	}
}

func TestWarnDeprecatedPythonSilentOnSupportedPin(t *testing.T) {
	ref := publishedBaseRepo + ":py" + supportedPythonLine(t)
	if got := warn(&domain.LeoflowConfig{BaseImage: ref}); got != "" {
		t.Fatalf("a pin to a supported line produced a warning:\n%s", got)
	}
}

func TestWarnDeprecatedPythonNilAndEmpty(t *testing.T) {
	if got := warn(nil) + warn(&domain.LeoflowConfig{}); got != "" {
		t.Fatalf("nil/empty config produced a warning:\n%s", got)
	}
}

// The warning declares a width. The `Fix:` line was folded at that width and
// THEN indented, so it rendered two columns over on the default path and much
// wider on the base_image path, where the replacement reference is longer.
func TestWarnDeprecatedPythonRespectsItsDeclaredWidth(t *testing.T) {
	d := deprecatedPythonLine(t)
	for name, cfg := range map[string]*domain.LeoflowConfig{
		"python_version": {PythonVersion: d.Version},
		"base_image":     {BaseImage: publishedBaseRepo + ":py" + d.Version + "-v0.4.5"},
	} {
		t.Run(name, func(t *testing.T) {
			// The post-build reminder is held to the same width: it is emitted by
			// the same code path and read on the same terminal.
			var after strings.Builder
			remindDeprecatedPythonAfterBuild(&after, cfg)
			for _, line := range strings.Split(strings.TrimRight(warn(cfg)+after.String(), "\n"), "\n") {
				// An unbreakable token (an image reference) is allowed to overhang:
				// a wrapped reference cannot be copied, which is the only thing the
				// reader wants to do with it. Everything else must fit.
				if len([]rune(line)) > deprecationWrapCols && len(strings.Fields(line)) > 1 {
					longest := 0
					for _, f := range strings.Fields(line) {
						longest = max(longest, len([]rune(f)))
					}
					if longest+len(bodyIndent) <= deprecationWrapCols {
						t.Errorf("line is %d cols, declared max is %d, and nothing on it is unbreakable:\n%s",
							len([]rune(line)), deprecationWrapCols, line)
					}
				}
			}
		})
	}
}

// errWriter fails every write, standing in for a closed pipe (`leoflow compile
// | head`) or a full disk.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// The warning is advisory, and the assertion has to be made where the damage
// was: checkProjectPreconditions used to `return warnDeprecatedPython(...)`, so
// a closed stderr aborted a compile that would otherwise have succeeded. The
// renderer alone cannot show this — a caller is free to discard a returned
// error — so the test drives the precondition gate the commands actually call.
func TestDeprecationWarningIsNotFatalOnAFailingWriter(t *testing.T) {
	d := deprecatedPythonLine(t)
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\npython_version: \""+d.Version+"\"\n")
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatalf("loadProjectConfig: %v", err)
	}
	cmd := newValidateCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(errWriter{})
	if perr := checkProjectPreconditions(cmd, dir, cfg); perr != nil {
		t.Fatalf("a stderr write failure failed the command: %v", perr)
	}
	// The post-build reminder is on the same footing.
	remindDeprecatedPythonAfterBuild(errWriter{}, cfg)
}

// `--build` shells out to a container builder; the layer output that follows
// scrolls the warning off the top. The reminder puts it back where the eye
// lands, and it must still name the field that has to change.
func TestRemindDeprecatedPythonAfterBuild(t *testing.T) {
	d := deprecatedPythonLine(t)
	var out strings.Builder
	remindDeprecatedPythonAfterBuild(&out, &domain.LeoflowConfig{PythonVersion: d.Version})
	mustContainAll(t, out.String(), "warning:", d.Version, fieldPythonVersion, d.RemoveAfter)

	out.Reset()
	pinned := publishedBaseRepo + ":py" + d.Version + "-v0.4.5"
	remindDeprecatedPythonAfterBuild(&out, &domain.LeoflowConfig{PythonVersion: supportedPythonLine(t), BaseImage: pinned})
	mustContainAll(t, out.String(), "warning:", d.Version, fieldBaseImage, d.RemoveAfter)
	mustContainNone(t, out.String(), fieldPythonVersion)

	out.Reset()
	remindDeprecatedPythonAfterBuild(&out, &domain.LeoflowConfig{PythonVersion: supportedPythonLine(t)})
	if out.String() != "" {
		t.Fatalf("a supported line produced a post-build reminder:\n%s", out.String())
	}
}

func TestWrapWords(t *testing.T) {
	cases := []struct {
		name string
		in   string
		cols int
		want []string
	}{
		{"empty", "   ", 10, nil},
		{"fits on one line", "a b c", 10, []string{"a b c"}},
		{"folds at the boundary", "aaaa bbbb cccc", 9, []string{"aaaa bbbb", "cccc"}},
		{"collapses runs of whitespace", "a \n  b", 10, []string{"a b"}},
		{
			// An image reference longer than the column budget must survive intact:
			// a wrapped one cannot be copy-pasted, which is the only thing the
			// reader wants to do with it.
			"an over-long token is not cut",
			"pull ghcr.io/dexadata/leoflow-runtime:py3.10-v0.4.5 now",
			20,
			[]string{"pull", "ghcr.io/dexadata/leoflow-runtime:py3.10-v0.4.5", "now"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapWords(tc.in, tc.cols)
			if len(got) != len(tc.want) {
				t.Fatalf("wrapWords(%q, %d) = %q, want %q", tc.in, tc.cols, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("wrapWords(%q, %d) = %q, want %q", tc.in, tc.cols, got, tc.want)
				}
			}
		})
	}
}

// `leoflow validate` is the sub-second command an author runs in a loop with
// dexaflow.yaml open — the exact moment this warning is worth something, and the
// one entry point that open-coded its preconditions and so never warned at all.
// Driving the real cobra command rather than calling the helper is the point:
// the bug was in the wiring, not in the renderer.
func TestValidateWarnsAboutADeprecatedPythonLine(t *testing.T) {
	d := deprecatedPythonLine(t)
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\npython_version: \""+d.Version+"\"\n")
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte("dag = None\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd := newValidateCommand()
	cmd.SetArgs([]string{dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(&stderr)
	// The dag.py syntax check may fail or be skipped depending on the host's
	// interpreter; the warning is emitted before it either way, and asserting on
	// the command's error would make this a test of the local Python install.
	_ = cmd.Execute()
	mustContainAll(t, stderr.String(), "warning:", "python_version "+d.Version+" is deprecated")
}

// A supported line must leave `validate` silent, or the warning is noise on
// every run of the command an author runs most.
func TestValidateSilentOnASupportedPythonLine(t *testing.T) {
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\npython_version: \""+supportedPythonLine(t)+"\"\n")
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte("dag = None\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd := newValidateCommand()
	cmd.SetArgs([]string{dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(&stderr)
	_ = cmd.Execute()
	if strings.Contains(stderr.String(), "is deprecated") {
		t.Fatalf("validate warned about a supported line:\n%s", stderr.String())
	}
}
