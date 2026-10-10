package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
)

// deprecationWrapCols is the widest RENDERED line the warning emits, indent
// included. Narrow enough to stay readable in a split terminal, wide enough
// that the sentence structure of the reason survives. Body lines carry a
// two-space indent, so they are folded at deprecationWrapCols-len(bodyIndent);
// folding them at deprecationWrapCols and then indenting is how the `Fix:` line
// came out at 91 columns against a declared 78.
const deprecationWrapCols = 78

// bodyIndent prefixes every line of the warning after the headline.
const bodyIndent = "  "

// The two dexaflow.yaml fields that can put a compile on a deprecated Python
// line. They are spelled exactly as the author writes them, because the warning
// names the field it wants changed and a name that does not match the file is a
// name the reader has to translate.
const (
	fieldPythonVersion = "python_version"
	fieldBaseImage     = "base_image"
)

// pythonDeprecationHit is a compile that resolves to a deprecated Python line,
// and — the part that matters — WHICH field put it there.
//
// The two sources need different remedies, and a remedy that does not apply is
// worse than none. `resolveBaseImage` returns cfg.BaseImage verbatim, so telling
// an author who pinned base_image to change python_version is an instruction
// they can follow, rebuild, and watch produce the identical warning. At that
// point the honest conclusion is that the tool is broken, and the message gets
// filtered — which is precisely the fate this warning exists to avoid.
type pythonDeprecationHit struct {
	// version is the deprecated Python line, e.g. "3.10".
	version string
	// field is the dexaflow.yaml key that selected it: fieldPythonVersion or
	// fieldBaseImage.
	field string
	// ref is the base_image value as written, empty on the python_version path.
	ref string
	// tag is ref's tag ("py3.10-v0.4.5"), empty on the python_version path.
	tag string
	// digest is ref's digest ("sha256:…") when the pin carries one.
	digest string
}

// warnDeprecatedPython prints a one-time warning when the base image this
// compile will build FROM is a Python line that is on its way out.
//
// It runs at compile, not at task boot. The choice matters: the author picking
// `python_version` is the only person who can change it, and compile is the
// moment they make the choice, with the file open. A warning from the agent in
// the task pod would reach the operator instead — who cannot edit the DAG's
// dexaflow.yaml — and would repeat on every task of every run of every DAG,
// which is how a real warning becomes log noise people filter out. A build-time
// warning inside runtime/Dockerfile was the third option and is weaker still:
// it only fires for people who build the base from a source checkout, which is
// nobody on the path this deprecation is about (they pull the published tag).
//
// Nothing here fails the compile — not even a failed write to w. The line is
// still published, still pulls, and still works; the point is to reach the
// author months before it stops being rebuilt, not to break them today. A
// compile that produced a correct dag.json must not exit non-zero because a
// closed pipe swallowed an advisory.
//
// Two populations get the warning, and each gets its own headline and its own
// remedy:
//   - the default path, where base_image is unset and the FROM is derived from
//     python_version;
//   - an explicit base_image that pins one of OUR published deprecated tags,
//     which is the population the deprecation is actually about — a pin is what
//     keeps a user on a frozen base after we stop publishing it.
//
// An explicit base_image pointing anywhere else is somebody else's image and
// gets no warning, even when python_version is set: the field is unused for the
// FROM in that case, and nagging about it would train people to ignore this.
func warnDeprecatedPython(w io.Writer, cfg *domain.LeoflowConfig) {
	hit, ok := deprecatedPythonForBuild(cfg)
	if !ok {
		return
	}
	d, ok := domain.DeprecatedPythonVersion(hit.version)
	if !ok {
		return
	}
	var b strings.Builder
	writeWrapped(&b, deprecationHeadline(hit, d), false)
	writeWrapped(&b, d.Reason, true)
	writeWrapped(&b, deprecationRemedy(hit, d), true)
	// Deliberately unchecked: a warning that can abort the compile is not a
	// warning. A closed pipe (`dexaflow compile | head`) must not turn a correct
	// dag.json into a non-zero exit.
	_, _ = io.WriteString(w, b.String()) //nolint:errcheck // advisory output; a write failure must not fail the compile
}

// writeWrapped folds text to deprecationWrapCols and appends it to b.
//
// Body paragraphs are indented on every line; the headline hangs, so it starts
// at column 0 and its continuations line up with the body. Everything is folded
// at deprecationWrapCols-len(bodyIndent) and indented afterwards, which is the
// arithmetic the previous version got backwards — folding at the declared width
// and THEN adding the indent is how a message that declares 78 columns rendered
// at 91, and the headline, folded at no width at all, at 189.
func writeWrapped(b *strings.Builder, text string, indentFirst bool) {
	for i, line := range wrapWords(text, deprecationWrapCols-len(bodyIndent)) {
		if i > 0 || indentFirst {
			b.WriteString(bodyIndent)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
}

// remindDeprecatedPythonAfterBuild re-states the deprecation in one line after
// the image is built.
//
// `--build` shells out to a container builder, and the minutes of layer output
// that follow scroll the warning off the top of the terminal. Authors read the
// last screen of a long command, not the first, so the finding has to appear
// where the eye lands. One line, not the full argument: the argument is above,
// and repeating it in full is how a warning becomes wallpaper.
func remindDeprecatedPythonAfterBuild(w io.Writer, cfg *domain.LeoflowConfig) {
	hit, ok := deprecatedPythonForBuild(cfg)
	if !ok {
		return
	}
	d, ok := domain.DeprecatedPythonVersion(hit.version)
	if !ok {
		return
	}
	var b strings.Builder
	writeWrapped(&b, fmt.Sprintf(
		"warning: built on the deprecated Python %s line, selected by %s; %s:py%s stops being published after %s — see the warning above this build for the fix.",
		d.Version, hit.field, publishedBaseRepo, d.Version, d.RemoveAfter), false)
	_, _ = io.WriteString(w, b.String()) //nolint:errcheck // advisory output; a write failure must not fail the compile
}

// deprecationHeadline names the deprecated line AND the field that selected it.
// Reporting `python_version 3.10` to an author whose file says
// `python_version: "3.12"` next to a py3.10 base_image pin is a message about
// somebody else's project.
func deprecationHeadline(hit pythonDeprecationHit, d domain.PythonDeprecation) string {
	if hit.field == fieldBaseImage {
		return fmt.Sprintf("warning: base_image %s pins Python %s, which is deprecated; %s:py%s stops being published after %s.",
			hit.ref, d.Version, publishedBaseRepo, d.Version, d.RemoveAfter)
	}
	return fmt.Sprintf("warning: python_version %s is deprecated; %s:py%s stops being published after %s.",
		d.Version, publishedBaseRepo, d.Version, d.RemoveAfter)
}

// deprecationRemedy renders the `Fix:` line for the field that actually
// controls the FROM.
//
// On the base_image path it names the replacement tag in full rather than the
// line, because the author pinned a specific tag shape (`py3.10`,
// `py3.10-v0.4.5`, `py3.10-v0.4.5-rc.1`) and the useful instruction is the
// string that replaces theirs, suffix preserved. python_version is deliberately
// not mentioned there: it is unused for the FROM, so changing it does nothing.
func deprecationRemedy(hit pythonDeprecationHit, d domain.PythonDeprecation) string {
	if hit.field != fieldBaseImage {
		return fmt.Sprintf("Fix: set python_version: %q in dexaflow.yaml and rebuild. Existing images keep running.", d.Replacement)
	}
	replacement := publishedBaseRepo + ":py" + d.Replacement + strings.TrimPrefix(hit.tag, "py"+hit.version)
	if hit.digest != "" {
		// The pinned digest names a py3.10 manifest; carrying it over would pull
		// the deprecated image back in under a 3.11 tag.
		return fmt.Sprintf("Fix: repoint base_image to %s in dexaflow.yaml, re-pinning its digest, and rebuild. Existing images keep running.", replacement)
	}
	return fmt.Sprintf("Fix: repoint base_image to %s in dexaflow.yaml and rebuild. Existing images keep running.", replacement)
}

// deprecatedPythonForBuild returns the deprecation-relevant facts about this
// config's build, and whether a deprecated line is involved at all. See
// warnDeprecatedPython for why an unrelated base_image opts out.
func deprecatedPythonForBuild(cfg *domain.LeoflowConfig) (pythonDeprecationHit, bool) {
	if cfg == nil {
		return pythonDeprecationHit{}, false
	}
	if cfg.BaseImage == "" {
		if cfg.PythonVersion == "" {
			return pythonDeprecationHit{}, false
		}
		return pythonDeprecationHit{version: cfg.PythonVersion, field: fieldPythonVersion}, true
	}
	repo, tag, digest := splitImageRef(cfg.BaseImage)
	if repo != publishedBaseRepo || !strings.HasPrefix(tag, "py") {
		// A digest-only pin (repo@sha256:…) lands here with an empty tag. The
		// reference names a manifest, not a line, and resolving it would mean a
		// registry round-trip inside a command that must stay offline-safe — so it
		// stays silent rather than guessing.
		return pythonDeprecationHit{}, false
	}
	// Both published tag shapes name the line first: the moving `py3.10` and the
	// immutable per-release `py3.10-v0.4.5`.
	line, _, _ := strings.Cut(strings.TrimPrefix(tag, "py"), "-")
	if line == "" {
		return pythonDeprecationHit{}, false
	}
	return pythonDeprecationHit{
		version: line,
		field:   fieldBaseImage,
		ref:     cfg.BaseImage,
		tag:     tag,
		digest:  digest,
	}, true
}

// splitImageRef splits an OCI reference into repository, tag and digest.
//
// A plain strings.Cut on ":" gets both real-world shapes wrong. A digest pin —
// `repo:py3.10@sha256:…`, which is what a supply-chain-conscious author writes,
// and exactly the author most likely to be pinned to an old base — yields the
// tag "py3.10@sha256" and stops matching anything, so the population this
// warning is for is the population it silently skips. A registry port
// (`registry:5000/team/img`) is misread as a tag for the same reason.
func splitImageRef(ref string) (repo, tag, digest string) {
	if before, after, found := strings.Cut(ref, "@"); found {
		ref, digest = before, after
	}
	// The tag separator is the last ":" that comes after the last "/"; anything
	// earlier belongs to the registry host's port.
	if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
		return ref[:colon], ref[colon+1:], digest
	}
	return ref, "", digest
}

// wrapWords folds s onto lines of at most cols runes, breaking only at spaces. A
// single word longer than cols is left over-long rather than cut, because the
// long tokens in these messages are image references and URLs, and a broken one
// cannot be copied.
func wrapWords(s string, cols int) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	lines := []string{fields[0]}
	for _, word := range fields[1:] {
		last := len(lines) - 1
		if len([]rune(lines[last]))+1+len([]rune(word)) <= cols {
			lines[last] += " " + word
			continue
		}
		lines = append(lines, word)
	}
	return lines
}
