package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	yaml "go.yaml.in/yaml/v3"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

// projectConfigFile is the name of a project's config file.
const projectConfigFile = "dexaflow.yaml"

// legacyProjectConfigFile is the config file's name before the rename. It is
// read when it is the only one present.
const legacyProjectConfigFile = "leoflow.yaml"

// projectConfigPath returns the config file a project in dir is read from:
// dexaflow.yaml, or leoflow.yaml when that is the only one present. For a
// directory with neither it names dexaflow.yaml, the file to create.
func projectConfigPath(dir string) string {
	current := filepath.Join(dir, projectConfigFile)
	if fileExists(current) {
		return current
	}
	legacy := filepath.Join(dir, legacyProjectConfigFile)
	if fileExists(legacy) {
		return legacy
	}
	return current
}

// projectConfigNote explains which config file a project is read from when
// the pre-rename leoflow.yaml is involved, and is "" otherwise.
func projectConfigNote(dir string) string {
	hasCurrent := fileExists(filepath.Join(dir, projectConfigFile))
	hasLegacy := fileExists(filepath.Join(dir, legacyProjectConfigFile))
	switch {
	case hasCurrent && hasLegacy:
		return fmt.Sprintf("%s has both dexaflow.yaml and leoflow.yaml; using dexaflow.yaml (leoflow.yaml is ignored)", dir)
	case hasLegacy:
		return fmt.Sprintf("%s is configured by leoflow.yaml; it keeps working, and renaming it to dexaflow.yaml uses the current name", dir)
	}
	return ""
}

// announcedConfigDirs remembers the projects whose config note was printed, so
// a command that loads the same project several times says it once.
var announcedConfigDirs sync.Map

// announceProjectConfig prints projectConfigNote for dir once per process, on an
// interactive terminal only: piped and scripted output stays as it was.
func announceProjectConfig(dir string) {
	note := projectConfigNote(dir)
	if note == "" || !stderrIsTerminal() {
		return
	}
	if _, seen := announcedConfigDirs.LoadOrStore(filepath.Clean(dir), true); !seen {
		fmt.Fprintln(os.Stderr, "note: "+note)
	}
}

// fileExists reports whether path exists and is not a directory.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// loadProjectConfig reads and parses the project config in dir, then applies the
// canonical schema defaults so every downstream consumer sees a fully-populated
// config (DagSource, PythonVersion, exclude paths, Build/Registry defaults).
// Centralizing the defaults here is what lets the multi-DAG workspace synthesize
// a working config from a sparse yaml (or none at all — see DiscoverProjects).
func loadProjectConfig(dir string) (*domain.LeoflowConfig, error) {
	announceProjectConfig(dir)
	p := projectConfigPath(dir)
	data, err := os.ReadFile(p) //nolint:gosec // G304: project path is supplied by the operator on the CLI.
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	cfg, unknown, err := parseProjectConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	if unknown != nil {
		return nil, fmt.Errorf("%s: %w", p, unknown)
	}
	return cfg, nil
}

// loadProjectConfigLenient is loadProjectConfig without the unknown-key
// refusal. Discovery uses it: a Lite workspace loads every subdirectory, and a
// project whose yaml has a stray key still has a dag_id, a dbt block and a
// source. Refusing here would either rename the DAG after its directory or drop
// it from the workspace entirely — a typo should be reported by compile, with
// the file and the key, not by a DAG quietly disappearing. Genuine syntax
// errors still fail.
func loadProjectConfigLenient(dir string) (*domain.LeoflowConfig, error) {
	p := projectConfigPath(dir)
	data, err := os.ReadFile(p) //nolint:gosec // G304: project path is supplied by the operator on the CLI.
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	cfg, _, perr := parseProjectConfig(data) //nolint:errcheck // discarding the unknown-key error is what "lenient" means here.
	if perr != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, perr)
	}
	return cfg, nil
}

// parseProjectConfig decodes the project config twice, on purpose: once strictly to
// learn whether the file carries keys the schema does not define, and once
// leniently to produce the config regardless. Splitting the two lets one caller
// refuse the file while another keeps working with it.
//
// The strict pass is what makes the schema's `additionalProperties: false`
// reachable at all (#15). It was not: yaml.Unmarshal drops an unknown key into
// the void, and Validate() then marshals the STRUCT back to JSON and validates
// that — so by the time the schema sees the document, the offending key has
// already ceased to exist. Every typo, and every key written at the wrong level,
// was accepted in silence.
func parseProjectConfig(data []byte) (cfg *domain.LeoflowConfig, unknown, err error) {
	// The lenient decode runs FIRST and unconditionally, and its own verdict is
	// the one that decides whether a config exists at all. That ordering is
	// deliberate, but it is forward insurance rather than a fix for a reachable
	// bug: measured, DiscoverProjects returns byte-identical results with and
	// without the restructure, because yaml.Unmarshal rejects the same inputs the
	// classifier mishandles. What it buys is that isUnknownFieldError is a string
	// match against a third-party library's prose. It used to GATE this decode, so
	// a yaml/v3 bump that reworded the error would have cost a caller its config —
	// and for a dbt-only project that means discovery drops it and the DAG is
	// removed from the registry as "folder gone". Now the same bump costs an ugly
	// message and nothing else.
	var lenient domain.LeoflowConfig
	if lerr := yaml.Unmarshal(data, &lenient); lerr != nil {
		return nil, nil, lerr
	}
	lenient.ApplyDefaults()

	// The strict pass is a REPORTER. It never decides whether the config parses,
	// only what the strict callers are told about it.
	var strict domain.LeoflowConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	switch serr := dec.Decode(&strict); {
	case serr == nil, errors.Is(serr, io.EOF):
		// io.EOF: an empty or comments-only file. yaml.Unmarshal treats that as
		// an empty document and so must this, or a mid-edit save in the Lite web
		// IDE reports a bare "EOF" where it used to report the missing dag_id.
	case isUnknownFieldError(serr):
		unknown = unknownKeyError(serr)
	default:
		// A genuine decode error the lenient pass tolerated. Report it to the
		// strict callers, but the config still stands for discovery.
		unknown = serr
	}
	return &lenient, unknown, nil
}

// isUnknownFieldError reports whether a yaml decode error is only about keys the
// struct does not define. yaml/v3 reports these as a TypeError whose every entry
// says "field X not found in type Y"; anything else in the list is a real
// decode failure and must not be downgraded to an unknown-key warning.
func isUnknownFieldError(err error) bool {
	te := &yaml.TypeError{}
	if !errors.As(err, &te) || len(te.Errors) == 0 {
		return false
	}
	for _, e := range te.Errors {
		if !strings.Contains(e, "not found in type") {
			return false
		}
	}
	return true
}

// unknownKeyError turns yaml's per-line "field X not found in type Y" list into
// one message that names the keys and, for the key our own docs taught, says
// where it actually belongs. A rejection that only says "unknown key" moves the
// user from a silent failure to a puzzle; naming the right key is the fix.
func unknownKeyError(err error) error {
	te := &yaml.TypeError{}
	if !errors.As(err, &te) {
		return err
	}
	keys := make([]string, 0, len(te.Errors))
	seen := map[string]bool{}
	// Per KEY, not per message. Two independent ORs over the same error list let
	// any top-level unknown key light the flag while a nested `schedule` lit the
	// hint, so `build.schedule` next to an unrelated typo got lectured about
	// DAG(schedule=...).
	topLevelKeys := map[string]bool{}
	for _, e := range te.Errors {
		m := unknownFieldRe.FindStringSubmatch(e)
		if len(m) < 2 {
			continue
		}
		// Marked before the dedup: a key reported at two levels must not lose
		// its top-level mark to an early continue.
		if strings.HasSuffix(e, "not found in type "+leoflowConfigTypeName) {
			topLevelKeys[m[1]] = true
		}
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		keys = append(keys, m[1])
	}
	slices.Sort(keys)
	// Quote each key, then join. Sprintf("%q", ...) over a string that already
	// carries the join's quotes re-escapes them and renders: unknown keys
	// "bar\", \"foo".
	quoted := make([]string, 0, len(keys))
	for _, k := range keys {
		quoted = append(quoted, strconv.Quote(k))
	}
	noun := "unknown key "
	if len(keys) > 1 {
		noun = "unknown keys "
	}
	msg := noun + strings.Join(quoted, ", ")
	// Only for the top-level key. The hint was keyed off the leaf name, so a
	// `schedule` nested anywhere — build.schedule, say — got told its DAG takes
	// its schedule from DAG(schedule=...), which is not that person's problem.
	// The TypeError entry names the struct the field was missing from.
	if topLevelKeys["schedule"] {
		// The one we taught ourselves, in three places, for three releases.
		msg += ". A dag.py DAG takes its schedule from DAG(schedule=…); a pure-dbt DAG declares it under dbt.schedule. There is no top-level schedule:"
	}
	return fmt.Errorf("%s in dexaflow.yaml", msg)
}

var unknownFieldRe = regexp.MustCompile(`field (.+) not found in type`)

// leoflowConfigTypeName is how yaml/v3 names the root config in its "not found
// in type X" errors. Derived from the type so renaming the struct cannot quietly
// unlink the top-level-key detection above.
var leoflowConfigTypeName = reflect.TypeOf(domain.LeoflowConfig{}).String()

// dagSourcePath resolves the DAG source file for a project. The caller is
// expected to have applied schema defaults (cfg.ApplyDefaults), so cfg.DagSource
// is always populated — loadProjectConfig does this automatically.
func dagSourcePath(dir string, cfg *domain.LeoflowConfig) string {
	return filepath.Join(dir, cfg.DagSource)
}

// regularFileExists reports whether path names an existing regular file.
//
// The regularity test is load-bearing, not defensive. `dag_source` has no
// pattern in the authoring schema, so `dag_source: "."` is legal and stats
// successfully as the project directory — a plain existence check refuses every
// dbt-only project with "delete .". A directory literally named dag.py stats
// the same way.
func regularFileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// errDbtBlockWithDagSource refuses a project carrying BOTH a top-level dbt:
// block and a DAG source, because the two describe different DAGs and only one
// of them can win.
//
// It used to be the dbt: block, silently: compile routes on cfg.Dbt before the
// parser is ever invoked, so the Python was never read and nothing said so. The
// author's whole feedback loop — validate, compile, deploy — reported success
// while shipping a DAG missing every non-dbt task (#1001). This is also the
// state a migration passes through, since writing the dag.py before deleting
// dbt: is the order a person naturally works in.
//
// Refusing rather than merging keeps the semantics question ("which wins?
// how would they compose?") out of the decision: there is no reading of both
// blocks at once that the author could have intended.
func errDbtBlockWithDagSource(dir string, cfg *domain.LeoflowConfig) error {
	if cfg == nil || cfg.Dbt == nil {
		return nil
	}
	src := dagSourcePath(dir, cfg)
	// Absence is the ordinary case — a genuine dbt-only project has no DAG
	// source — so it is not an error to report, and an unreadable path is not
	// evidence of a conflict either.
	if !regularFileExists(src) {
		return nil
	}
	return fmt.Errorf(
		"%s declares a top-level dbt: block and %s also exists; refusing, because the two describe different DAGs.\n"+
			"  To keep the Python: delete the dbt: block, move the project under dbt_groups.<name>: in %s,\n"+
			"                      and call dbt_group(\"<name>\") in %s.\n"+
			"  To keep the dbt-only DAG: delete %s.\n"+
			"(Compiling the pair used to succeed and drop the Python silently.)",
		projectConfigPath(dir), src, projectConfigPath(dir), src, src)
}

// configFilePath returns the config file to load: the --config flag when set,
// otherwise the default path when it exists, otherwise empty (defaults + env).
func configFilePath(cmd *cobra.Command) string {
	if p, err := cmd.Flags().GetString("config"); err == nil && p != "" {
		return p
	}
	def, err := config.DefaultConfigFile()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(def); err == nil {
		return def
	}
	return ""
}

// stateDirIn returns the per-user state directory under the home directory
// home: ~/.dexaflow, or a link to an existing ~/.leoflow (config.HomeDirIn).
func stateDirIn(home string) string {
	dir, err := config.HomeDirIn(home)
	if err != nil {
		return filepath.Join(home, config.HomeDirName)
	}
	return dir
}
