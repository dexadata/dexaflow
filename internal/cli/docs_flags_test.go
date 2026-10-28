package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The documentation is where users copy commands from, and a flag that does not
// exist fails only when somebody runs the page (#1709). These tests read every
// `dexaflow ...` command written in a fenced code block or an inline code span
// of the docs site and the examples, and check its subcommands and flags
// against the real command tree, so a renamed or removed flag fails here
// instead of in a user's pipeline. A flag mentioned on its own in prose
// (`--arch` with no command before it) is not a command and is not checked.

// docInvocation is one `dexaflow ...` command found in a Markdown file: the
// words after the program name, up to the end of that shell command.
type docInvocation struct {
	file string
	line int
	args []string
}

func (d docInvocation) String() string {
	return fmt.Sprintf("%s:%d: dexaflow %s", d.file, d.line, strings.Join(d.args, " "))
}

// docProgramRe matches the program name at a command position: the start of a
// line or of an inline code span (after an optional `$ ` prompt and `VAR=value`
// assignments), a YAML `run:` or list item, a subshell, a pipe or list
// operator, or a shell keyword that starts a command. Anywhere else the word is
// an argument of another program (`helm upgrade dexaflow ...` names a release),
// or part of a path or image (`~/.dexaflow`, `.../dexaflow-runtime`), and is not
// a command.
var docProgramRe = regexp.MustCompile(`(?:^\s*-?|[|;&(\x60]|\brun:|\b(?:then|do|else|exec|sudo))\s*` +
	`(?:\$\s+)?(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*(?:dexaflow|leoflow)[ \t]+`)

// docFlagNameRe extracts the flag itself from a word, dropping the value of a
// `--flag=value` and any punctuation the surrounding text glued to it.
var docFlagNameRe = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9-]*`)

// docCommandEnd lists the shell tokens that end one command: whatever follows
// belongs to another program (a pipe, a redirect, a list operator, a comment).
var docCommandEnd = map[string]bool{
	"|": true, "||": true, "&&": true, ";": true, "&": true, ">": true, ">>": true,
	"<": true, "<<": true, "2>": true, "2>&1": true, ")": true, "--": true,
}

// docInvocationsIn returns the dexaflow invocations in a Markdown document:
// every line of a fenced code block (joining `\` continuations) and every
// inline code span. Prose is ignored, so a sentence that merely names the CLI
// is never parsed as a command.
func docInvocationsIn(file string, md []byte) []docInvocation {
	var out []docInvocation
	inFence := false
	pending, pendingLine := "", 0
	sc := bufio.NewScanner(bytes.NewReader(md))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			pending = ""
			continue
		}
		if !inFence {
			parts := strings.Split(line, "`")
			for i := 1; i < len(parts); i += 2 {
				out = append(out, invocationsInText(file, n, parts[i])...)
			}
			continue
		}
		if pending == "" {
			pendingLine = n
		}
		if strings.HasSuffix(trimmed, `\`) {
			pending += strings.TrimSuffix(trimmed, `\`) + " "
			continue
		}
		out = append(out, invocationsInText(file, pendingLine, pending+line)...)
		pending = ""
	}
	return out
}

// invocationsInText finds each program-name occurrence in one shell line and
// tokenizes what follows it up to the end of that command.
func invocationsInText(file string, line int, text string) []docInvocation {
	var out []docInvocation
	for _, loc := range docProgramRe.FindAllStringIndex(text, -1) {
		args := docShellWords(text[loc[1]:])
		if len(args) > 0 {
			out = append(out, docInvocation{file: file, line: line, args: args})
		}
	}
	return out
}

// docShellWords splits a shell command into words, honoring quotes, and stops
// at the first token that ends the command (see docCommandEnd) or at a token
// that only makes sense as text around a command (a backtick, a comment).
func docShellWords(s string) []string {
	var words []string
	var cur strings.Builder
	var quote rune
	flush := func() bool {
		w := cur.String()
		cur.Reset()
		if w == "" {
			return true
		}
		if docCommandEnd[w] || strings.HasPrefix(w, "#") {
			return false
		}
		trimmed := strings.TrimRight(w, ";")
		if trimmed != "" {
			words = append(words, trimmed)
		}
		return trimmed == w
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
			cur.WriteRune(r)
		case r == ' ' || r == '\t':
			if !flush() {
				return words
			}
		case r == '`':
			flush()
			return words
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return words
}

// isDocFlag reports whether a word is a flag rather than a positional value:
// `-` alone is stdin and `-1` is a number.
func isDocFlag(w string) bool {
	return len(w) > 1 && w[0] == '-' && (w[1] < '0' || w[1] > '9')
}

// lookupDocFlag finds a flag word on a command, local, persistent or inherited,
// long or shorthand. The help and version flags cobra adds lazily at Execute
// are always accepted.
func lookupDocFlag(cmd *cobra.Command, word string) (found, takesValue bool) {
	hasValue := strings.Contains(word, "=")
	name := strings.TrimLeft(docFlagNameRe.FindString(word), "-")
	if name == "help" || name == "h" || (name == "version" && !cmd.HasParent()) {
		return true, false
	}
	for _, flagSet := range []func() *pflag.FlagSet{cmd.Flags, cmd.InheritedFlags, cmd.PersistentFlags} {
		set := flagSet()
		f := set.Lookup(name)
		if f == nil && len(name) == 1 && !strings.HasPrefix(word, "--") {
			f = set.ShorthandLookup(name)
		}
		if f != nil {
			return true, !hasValue && f.Value.Type() != "bool"
		}
	}
	return false, false
}

// unknownDocFlags resolves an invocation to its command in the tree and
// returns what that command does not have, each as "flag --x" or
// "subcommand x". It returns ok=false when the words do not name a command at
// all (a placeholder such as `<command>`, or text that happens to follow the
// program name), which is not the docs' business to fail on.
func unknownDocFlags(root *cobra.Command, args []string) (unknown []string, ok bool) {
	cmd := root
	var flags []string
	positional := false
	for i := 0; i < len(args); i++ {
		w := args[i]
		if isDocFlag(w) {
			flags = append(flags, w)
			if found, takesValue := lookupDocFlag(cmd, w); found && takesValue {
				i++
			}
			continue
		}
		if positional {
			continue
		}
		if sub := findDocSubcommand(cmd, w); sub != nil {
			cmd = sub
			continue
		}
		if !cmd.Runnable() {
			// Under a group such as `auth` or `admin`, a word that is not one of
			// its subcommands is a misspelling. At the root it is as likely to be
			// text after the program name, so only groups are checked.
			if cmd == root || isDocPlaceholder(w) {
				return nil, false
			}
			return []string{"subcommand " + w}, true
		}
		positional = true
	}
	if cmd == root && !positional && len(flags) == 0 {
		return nil, false
	}
	for _, f := range flags {
		if found, _ := lookupDocFlag(cmd, f); !found {
			unknown = append(unknown, "flag "+docFlagNameRe.FindString(f))
		}
	}
	return unknown, true
}

// isDocPlaceholder reports whether a word stands for something the reader
// fills in (`<subcommand>`, `[flags]`, `$CMD`, `a|b`, `...`) rather than a
// literal.
func isDocPlaceholder(w string) bool {
	return strings.ContainsAny(w, "<>[]{}$|") || strings.Contains(w, "...")
}

// findDocSubcommand returns the direct subcommand named (or aliased) w.
func findDocSubcommand(cmd *cobra.Command, w string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == w || c.HasAlias(w) {
			return c
		}
	}
	return nil
}

// docFlagViolations checks every invocation in the given Markdown documents
// and returns one line per flag the command tree does not have.
func docFlagViolations(root *cobra.Command, docs map[string][]byte) []string {
	var out []string
	for file, md := range docs {
		for _, inv := range docInvocationsIn(file, md) {
			unknown, ok := unknownDocFlags(root, inv.args)
			if !ok {
				continue
			}
			for _, f := range unknown {
				out = append(out, fmt.Sprintf("%s (unknown %s)", inv, f))
			}
		}
	}
	// Map iteration order is random; a sorted report reads the same every run.
	slices.Sort(out)
	return out
}

// TestDocFlagCheckerCatchesSeededFlag proves the checker can fail: a page that
// documents a flag the command does not have is reported, by file, line and
// flag, while the flags around it that do exist, the pipe and the shell around
// the command are not.
func TestDocFlagCheckerCatchesSeededFlag(t *testing.T) {
	md := []byte("# Deploy\n\n" +
		"Run `dexaflow deploy --yes` once.\n\n" +
		"```bash\n" +
		"export DEXAFLOW_TOKEN=$(dexaflow auth create-token --username ci --password-stdin < pw.txt)\n" +
		"dexaflow deploy --arch linux/arm64 --yes\n" +
		"dexaflow compile dags/x -o dag.json --build \\\n" +
		"  --push --frobnicate | tee build.log --not-ours\n" +
		"docker build --platform linux/amd64 .   # not our CLI\n" +
		"```\n")
	got := docFlagViolations(NewRootCommand(), map[string][]byte{"seed.md": md})
	want := []string{
		"seed.md:7: dexaflow deploy --arch linux/arm64 --yes (unknown flag --arch)",
		"seed.md:8: dexaflow compile dags/x -o dag.json --build --push --frobnicate (unknown flag --frobnicate)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("violations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestDocFlagCheckerIgnoresNonCommands keeps the false positives down: names
// of files and images that contain the program name, placeholders and prose in
// a code block are not commands.
func TestDocFlagCheckerIgnoresNonCommands(t *testing.T) {
	md := []byte("```text\n" +
		"FROM ghcr.io/dexadata/dexaflow-runtime:py3.11 --platform\n" +
		"cat ~/.dexaflow/config.yaml --nope\n" +
		"dexaflow <command> --flag\n" +
		"dexaflow.yaml --nope\n" +
		"dexaflow admin <subcommand> --whatever\n" +
		"helm upgrade dexaflow oci://example/charts/dexaflow --reset-then-reuse-values\n" +
		"```\n")
	if got := docFlagViolations(NewRootCommand(), map[string][]byte{"x.md": md}); len(got) != 0 {
		t.Fatalf("false positives:\n%s", strings.Join(got, "\n"))
	}
}

// TestDocFlagCheckerCatchesUnknownSubcommand covers the misspelled subcommand
// under a group, which used to be dropped without a word because the group
// itself cannot run. A placeholder under a group and text after the bare
// program name are still not reported.
func TestDocFlagCheckerCatchesUnknownSubcommand(t *testing.T) {
	md := []byte("```bash\n" +
		"dexaflow auth create-tokn --username ci --password-stdin\n" +
		"dexaflow auth <subcommand> --help\n" +
		"dexaflow is the CLI\n" +
		"```\n")
	got := docFlagViolations(NewRootCommand(), map[string][]byte{"sub.md": md})
	want := "sub.md:2: dexaflow auth create-tokn --username ci --password-stdin (unknown subcommand create-tokn)"
	if strings.Join(got, "\n") != want {
		t.Fatalf("violations:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

// docFlagRoots are the trees the docs test reads, relative to the repository
// root. website/content/project holds ADRs, specs and plans: decision records
// keep the flags of the day they were written (they are immutable once
// accepted) and specs describe flags that do not exist yet, so neither is a
// page a user copies a command from. reference/go is generated from GoDoc.
var docFlagRoots = []string{"website/content", "examples"}

// docFlagSkip lists the directories under docFlagRoots that are not read, for
// the reasons given on docFlagRoots.
var docFlagSkip = []string{"website/content/project", "website/content/reference/go"}

// TestDocumentedFlagsExist walks the docs site and the examples and fails on
// any `dexaflow <command> --flag` whose flag the command does not have.
func TestDocumentedFlagsExist(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(filepath.Join(repo, "go.mod")); serr != nil {
		t.Fatalf("repository root not found at %s: %v", repo, serr)
	}
	docs := map[string][]byte{}
	for _, root := range docFlagRoots {
		werr := filepath.WalkDir(filepath.Join(repo, root), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repo, p) //nolint:errcheck // p is under repo by construction
			rel = filepath.ToSlash(rel)
			for _, skip := range docFlagSkip {
				if d.IsDir() && rel == skip {
					return filepath.SkipDir
				}
			}
			if d.IsDir() || !strings.HasSuffix(p, ".md") {
				return nil
			}
			data, rerr := os.ReadFile(p) //nolint:gosec // a file of this repository
			if rerr != nil {
				return rerr
			}
			docs[rel] = data
			return nil
		})
		if werr != nil {
			t.Fatalf("walking %s: %v", root, werr)
		}
	}
	if len(docs) < 50 {
		t.Fatalf("read only %d Markdown files; the walk is not reaching the docs", len(docs))
	}
	if v := docFlagViolations(NewRootCommand(), docs); len(v) > 0 {
		t.Errorf("documented flags that the CLI does not have (fix the page, or the flag):\n%s",
			strings.Join(v, "\n"))
	}
}
