package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// repoRoot is the repository root seen from this package's directory.
const repoRoot = "../../.."

// TestPageIsCurrent fails when the committed configuration reference differs
// from what the generator writes, which is how a new or changed setting that
// was not regenerated is caught in CI (`go test ./...`).
func TestPageIsCurrent(t *testing.T) {
	page, err := os.ReadFile(filepath.Join(repoRoot, pagePath))
	if err != nil {
		t.Fatal(err)
	}
	want, err := generate(repoRoot, page)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, page) {
		t.Fatalf("%s is stale: run `make gen-config-reference` and commit the result", pagePath)
	}
}

// TestCheckFailsOnStalePage drives the -check entry point against a copy of
// the repository whose page has a row edited by hand.
func TestCheckFailsOnStalePage(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{pagePath, registryPath} {
		copyFile(t, filepath.Join(repoRoot, rel), filepath.Join(root, rel))
	}
	cfgDir := filepath.Join(repoRoot, configPkgPath)
	entries, err := os.ReadDir(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		copyFile(t, filepath.Join(cfgDir, e.Name()), filepath.Join(root, configPkgPath, e.Name()))
	}
	if cerr := run(root, true); cerr != nil {
		t.Fatalf("check on an unmodified copy: %v", cerr)
	}

	page := filepath.Join(root, pagePath)
	data, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "| `DEXAFLOW_DATABASE_MAX_OPEN_CONNS` | `database.max_open_conns` | int | `25` |",
		"| `DEXAFLOW_DATABASE_MAX_OPEN_CONNS` | `database.max_open_conns` | int | `50` |", 1)
	if edited == string(data) {
		t.Fatal("fixture row not found on the page")
	}
	if err := os.WriteFile(page, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(root, true); err == nil || !strings.Contains(err.Error(), "is stale") {
		t.Fatalf("check on an edited page = %v, want a stale error", err)
	}
	if err := run(root, false); err != nil {
		t.Fatal(err)
	}
	if err := run(root, true); err != nil {
		t.Fatalf("check after regenerating: %v", err)
	}
}

// TestEverySettingMustBeOnATable fails generation when a block selection
// leaves a setting out, so a new key cannot be added without a row.
func TestEverySettingMustBeOnATable(t *testing.T) {
	page := "intro\n<!-- BEGIN GENERATED configref settings server. -->\n<!-- END GENERATED configref -->\n"
	_, err := generate(repoRoot, []byte(page))
	if err == nil || !strings.Contains(err.Error(), "setting database.url is on no table") {
		t.Fatalf("generate = %v, want a missing-setting error", err)
	}
}

func TestMatches(t *testing.T) {
	patterns := []string{"auth.", "!auth.oidc.", "secret_key"}
	for key, want := range map[string]bool{
		"auth.provider":              true,
		"auth.jwt.secret":            true,
		"auth.oidc.issuer":           false,
		"secret_key":                 true,
		"secret_key_migration_lock":  false,
		"authx.provider":             false,
		"executor.defaults.unit.cpu": false,
	} {
		if got := matches(key, patterns); got != want {
			t.Errorf("matches(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestCellEscapesPipes(t *testing.T) {
	if got, want := cell("a | b \\| c\n  d"), `a \| b \| c d`; got != want {
		t.Errorf("cell = %q, want %q", got, want)
	}
}

// envLiteral finds a variable name written in Go source: a DEXAFLOW_/LEOFLOW_
// string literal, or the suffix passed to envcompat.Lookup, which reads both
// prefixes without spelling either.
var envLiteral = regexp.MustCompile(`"(?:DEXAFLOW|LEOFLOW)_([A-Z0-9_]+)"|envcompat\.Lookup\("([A-Z0-9_]+)"\)`)

// TestEveryEnvironmentVariableIsListed fails when Go code under cmd/, internal/
// or pkg/ names a DEXAFLOW_/LEOFLOW_ variable that is neither a config setting
// nor listed in settings.yaml (environment or test_only), so a new variable
// cannot ship undocumented (#1570).
func TestEveryEnvironmentVariableIsListed(t *testing.T) {
	reg, err := loadRegistry(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	var families []string
	for _, s := range append(config.ServerSettings(), config.CLISettings()...) {
		if s.Env != "" {
			known[strings.TrimPrefix(s.Env, "DEXAFLOW_")] = true
		}
	}
	for _, e := range reg.Environment {
		if family, ok := strings.CutSuffix(e.Name, "<NAME>"); ok {
			families = append(families, family)
			continue
		}
		known[e.Name] = true
	}
	for _, e := range reg.TestOnly {
		known[e.Name] = true
	}

	unlisted := map[string]string{}
	for _, dir := range []string{"cmd", "internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range envLiteral.FindAllStringSubmatch(string(src), -1) {
				if name := m[1] + m[2]; !known[name] && !inFamily(name, families) {
					unlisted[name] = path
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	names := make([]string, 0, len(unlisted))
	for name := range unlisted {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Errorf("DEXAFLOW_%s (read in %s) is not documented: add it to the environment list of %s, or to test_only with a reason",
			name, unlisted[name], registryPath)
	}
}

func inFamily(name string, families []string) bool {
	for _, f := range families {
		if strings.HasPrefix(name, f) {
			return true
		}
	}
	return false
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
