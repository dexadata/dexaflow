// Command configref regenerates the tables of the configuration reference page
// (website/content/reference/configuration.md) from internal/config, so the page
// lists every key, variable and default the loaders actually have (#1570).
//
// The page stays hand-written prose around generated blocks. A block is the
// text between a begin marker and an end marker:
//
//	<!-- BEGIN GENERATED configref settings server. !server.cors. -->
//	...
//	<!-- END GENERATED configref -->
//
// and its body is replaced on every run. The marker names what goes inside:
//
//   - settings PATTERN...: one row per server setting whose key matches a
//     pattern. "a.b." matches every key under a.b, "a.b" matches that key, and
//     a leading "!" excludes. Every server setting must land in exactly one block.
//   - env GROUP: the variables of that group in settings.yaml's environment list.
//   - cli: the developer CLI's config keys (~/.dexaflow/config.yaml).
//
// Names, types and defaults come from the structs and default maps the loaders
// use (config.ServerSettings, config.CLISettings). The purpose of a key comes
// from settings.yaml when it has an entry there, and otherwise from the first
// paragraph of the Go field's doc comment.
//
// Usage, from the repository root:
//
//	go run ./internal/tools/configref          # rewrite the page
//	go run ./internal/tools/configref -check   # fail when the page is stale
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/envcompat"
)

// Paths relative to the repository root.
const (
	pagePath      = "website/content/reference/configuration.md"
	registryPath  = "internal/tools/configref/settings.yaml"
	configPkgPath = "internal/config"
)

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "fail instead of writing when the page is stale")
	flag.Parse()
	if err := run(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, "configref:", err)
		os.Exit(1)
	}
}

func run(root string, check bool) error {
	path := filepath.Join(root, pagePath)
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading page: %w", err)
	}
	want, err := generate(root, current)
	if err != nil {
		return err
	}
	if bytes.Equal(current, want) {
		return nil
	}
	if check {
		return fmt.Errorf("%s is stale: run `make gen-config-reference` and commit the result", pagePath)
	}
	if err := os.WriteFile(path, want, 0o644); err != nil { //nolint:gosec // a docs page, world-readable like the rest of the tree
		return fmt.Errorf("writing page: %w", err)
	}
	return nil
}

// registry is settings.yaml.
type registry struct {
	Settings    map[string]settingDoc `yaml:"settings"`
	Environment []envDoc              `yaml:"environment"`
	TestOnly    []testOnlyDoc         `yaml:"test_only"`
}

// settingDoc is the hand-written part of one server setting's row.
type settingDoc struct {
	Edition     string `yaml:"edition"`
	DefaultNote string `yaml:"default_note"`
	Description string `yaml:"description"`
}

// envDoc documents a variable a binary reads outside the config loaders.
type envDoc struct {
	Name        string `yaml:"name"`
	Group       string `yaml:"group"`
	Default     string `yaml:"default"`
	Description string `yaml:"description"`
}

// testOnlyDoc names a variable only automated tests set, and why it is not on
// the page.
type testOnlyDoc struct {
	Name   string `yaml:"name"`
	Reason string `yaml:"reason"`
}

func loadRegistry(root string) (*registry, error) {
	data, err := os.ReadFile(filepath.Join(root, registryPath))
	if err != nil {
		return nil, fmt.Errorf("reading registry: %w", err)
	}
	var r registry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", registryPath, err)
	}
	return &r, nil
}

// fieldDocs maps "Type.Field" to the first paragraph of the field's doc
// comment, for every struct in internal/config.
func fieldDocs(root string) (map[string]string, error) {
	fset := token.NewFileSet()
	dir := filepath.Join(root, configPkgPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	docs := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				for _, id := range field.Names {
					docs[ts.Name.Name+"."+id.Name] = firstParagraph(field.Doc.Text())
				}
			}
			return false
		})
	}
	return docs, nil
}

// firstParagraph joins the lines of a comment's first paragraph into one.
func firstParagraph(text string) string {
	para, _, _ := strings.Cut(strings.TrimSpace(text), "\n\n")
	return strings.Join(strings.Fields(para), " ")
}

var (
	beginRe = regexp.MustCompile(`^<!-- BEGIN GENERATED configref (.+?) -->$`)
	endLine = "<!-- END GENERATED configref -->"
	// pipeRe finds a column separator not already escaped.
	pipeRe = regexp.MustCompile(`(^|[^\\])\|`)
)

// generator holds everything one run renders from.
type generator struct {
	reg      *registry
	docs     map[string]string
	server   []config.Setting
	cli      []config.Setting
	used     map[string]int
	usedEnvs map[string]bool
}

// generate returns page with every generated block re-rendered.
func generate(root string, page []byte) ([]byte, error) {
	reg, err := loadRegistry(root)
	if err != nil {
		return nil, err
	}
	docs, err := fieldDocs(root)
	if err != nil {
		return nil, err
	}
	g := &generator{
		reg: reg, docs: docs,
		server: config.ServerSettings(), cli: config.CLISettings(),
		used: map[string]int{}, usedEnvs: map[string]bool{},
	}
	out, err := g.rewrite(string(page))
	if err != nil {
		return nil, err
	}
	if err := g.checkCoverage(); err != nil {
		return nil, err
	}
	return []byte(out), nil
}

func (g *generator) rewrite(page string) (string, error) {
	lines := strings.Split(page, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		m := beginRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		end := i + 1
		for end < len(lines) && lines[end] != endLine {
			end++
		}
		if end == len(lines) {
			return "", fmt.Errorf("line %d: %q has no %q", i+1, lines[i], endLine)
		}
		body, err := g.block(strings.Fields(m[1]))
		if err != nil {
			return "", fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, "", body, "", endLine)
		i = end
	}
	return strings.Join(out, "\n"), nil
}

func (g *generator) block(args []string) (string, error) {
	switch {
	case len(args) >= 2 && args[0] == "settings":
		return g.settingsTable(args[1:])
	case len(args) == 2 && args[0] == "env":
		return g.envTable(args[1])
	case len(args) == 1 && args[0] == "cli":
		return g.cliTable()
	default:
		return "", fmt.Errorf("unknown block %q", strings.Join(args, " "))
	}
}

// matches reports whether key is selected by patterns (see the package doc).
func matches(key string, patterns []string) bool {
	in := false
	for _, p := range patterns {
		exclude := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		hit := key == p || (strings.HasSuffix(p, ".") && strings.HasPrefix(key, p))
		if hit && exclude {
			return false
		}
		in = in || hit
	}
	return in
}

func (g *generator) settingsTable(patterns []string) (string, error) {
	var b strings.Builder
	b.WriteString("| Variable | Key | Type | Default | Edition | Purpose |\n|---|---|---|---|---|---|")
	n := 0
	for _, s := range g.server {
		if !matches(s.Key, patterns) {
			continue
		}
		n++
		g.used[s.Key]++
		doc := g.reg.Settings[s.Key]
		purpose, err := g.purpose(s, doc.Description)
		if err != nil {
			return "", err
		}
		edition := doc.Edition
		if edition == "" {
			edition = "both"
		}
		fmt.Fprintf(&b, "\n| %s | `%s` | %s | %s | %s | %s |",
			variableCell(s), s.Key, s.Type, defaultCell(s, doc.DefaultNote), edition, purpose)
	}
	if n == 0 {
		return "", fmt.Errorf("settings %v match no key", patterns)
	}
	return b.String(), nil
}

func (g *generator) cliTable() (string, error) {
	var b strings.Builder
	b.WriteString("| Key | Variable | Type | Default | Purpose |\n|---|---|---|---|---|")
	for _, s := range g.cli {
		purpose, err := g.purpose(s, "")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n| `%s` | %s | %s | %s | %s |",
			s.Key, variableCell(s), s.Type, defaultCell(s, ""), purpose)
	}
	return b.String(), nil
}

func (g *generator) envTable(group string) (string, error) {
	var b strings.Builder
	b.WriteString("| Variable | Default | Purpose |\n|---|---|---|")
	n := 0
	for _, e := range g.reg.Environment {
		if e.Group != group {
			continue
		}
		n++
		g.usedEnvs[e.Name] = true
		def := "_(empty)_"
		if e.Default != "" {
			def = "`" + e.Default + "`"
		}
		fmt.Fprintf(&b, "\n| `%s%s` | %s | %s |", envcompat.NewPrefix, e.Name, def, cell(e.Description))
	}
	if n == 0 {
		return "", fmt.Errorf("env group %q has no variable", group)
	}
	return b.String(), nil
}

// purpose is the registry text when there is one, else the field's doc comment.
func (g *generator) purpose(s config.Setting, registered string) (string, error) {
	if registered != "" {
		return cell(registered), nil
	}
	if doc := g.docs[s.Owner+"."+s.Field]; doc != "" {
		return cell(readerSentence(doc, s.Field)), nil
	}
	return "", fmt.Errorf("%s (%s.%s) has no description: add a doc comment to the field or an entry to %s",
		s.Key, s.Owner, s.Field, registryPath)
}

// readerSentence drops the Go convention of opening a doc comment with the
// field name ("Token is the JWT ..." becomes "The JWT ..."), which names an
// identifier the page's reader never sees.
func readerSentence(doc, field string) string {
	for _, verb := range []string{" is ", " are "} {
		if rest, ok := strings.CutPrefix(doc, field+verb); ok && rest != "" {
			return strings.ToUpper(rest[:1]) + rest[1:]
		}
	}
	return doc
}

func variableCell(s config.Setting) string {
	if s.Env == "" {
		return "_(config file only)_"
	}
	return "`" + s.Env + "`"
}

func defaultCell(s config.Setting, note string) string {
	def := "_(empty)_"
	if s.Default != "" {
		def = "`" + s.Default + "`"
	}
	if note != "" {
		def += ", " + note
	}
	return def
}

// cell flattens text into one table cell, escaping the column separator.
func cell(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	return pipeRe.ReplaceAllString(text, `$1\|`)
}

// checkCoverage fails when a setting is missing from the page or listed twice,
// when settings.yaml documents a key the code does not have, or when an
// environment variable in settings.yaml is on no table.
func (g *generator) checkCoverage() error {
	var problems []string
	known := map[string]bool{}
	for _, s := range g.server {
		known[s.Key] = true
		switch g.used[s.Key] {
		case 0:
			problems = append(problems, fmt.Sprintf("setting %s is on no table: add it to a settings block", s.Key))
		case 1:
		default:
			problems = append(problems, fmt.Sprintf("setting %s is on %d tables", s.Key, g.used[s.Key]))
		}
	}
	for key := range g.reg.Settings {
		if !known[key] {
			problems = append(problems, fmt.Sprintf("%s documents %s, which is no server setting", registryPath, key))
		}
	}
	seen := map[string]bool{}
	for _, e := range g.reg.Environment {
		if seen[e.Name] {
			problems = append(problems, fmt.Sprintf("environment variable %s listed twice", e.Name))
		}
		seen[e.Name] = true
		if !g.usedEnvs[e.Name] {
			problems = append(problems, fmt.Sprintf("environment variable %s (group %q) is on no table", e.Name, e.Group))
		}
		if e.Description == "" {
			problems = append(problems, fmt.Sprintf("environment variable %s has no description", e.Name))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return nil
}
