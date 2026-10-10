package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// poisonedProjects are dexaflow.yaml bodies the generated Dockerfile cannot
// carry, one per field the refusal covers, each with the field the error has to
// name. They are written as YAML and driven through the real commands, because
// the defect (#1268) was a correct check living where only `compile --build`
// reached it.
var poisonedProjects = []struct {
	name  string
	field string
	yaml  string
}{
	{"base_image newline", "base_image", "base_image: \"alpine\\nVOLUME/x\"\n"},
	{"base_image whitespace", "base_image", "base_image: \"alpine AS builder\"\n"},
	{"dag_source lexer meta", "dag_source", "dag_source: \"d'a't.py\"\n"},
	{"dbt group heredoc", "dbt_groups.g.project", "dbt_groups:\n  g:\n    project: \"<<EOF\"\n"},
	{"include_paths flag", "include_paths", "include_paths: [\".\", \"--from=alpine\"]\n"},
	{"exclude_paths newline", "exclude_paths", "exclude_paths: [\"foo\\n!secrets.env\"]\n"},
	{"dependencies newline", "dependencies", "dependencies: [\"six\\nRUN evil\"]\n"},
}

// writePoisonedProject writes a dag.py project whose config carries extra, under
// the given config file name, so the legacy leoflow.yaml is covered too.
func writePoisonedProject(t *testing.T, configName, extra string) string {
	t.Helper()
	dir := t.TempDir()
	body := "schema_version: \"1.0\"\ndag_id: sales\n" + extra
	if err := os.WriteFile(filepath.Join(dir, configName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// `validate` is the sub-second command an author runs with dexaflow.yaml open,
// and it called every one of these projects valid.
func TestValidateCommandRefusesADockerfileValue(t *testing.T) {
	for _, configName := range []string{projectConfigFile, legacyProjectConfigFile} {
		for _, tc := range poisonedProjects {
			t.Run(configName+"/"+tc.name, func(t *testing.T) {
				dir := writePoisonedProject(t, configName, tc.yaml)
				cmd := newValidateCommand()
				cmd.SetArgs([]string{dir})
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				err := cmd.Execute()
				if err == nil {
					t.Fatal("validate called the project valid")
				}
				if !strings.Contains(err.Error(), tc.field) {
					t.Errorf("the error must name %q, got: %v", tc.field, err)
				}
				if !strings.Contains(err.Error(), configName) {
					t.Errorf("the error must name the config file %q, got: %v", configName, err)
				}
			})
		}
	}
}

// Plain `compile`, without --build, must refuse before it starts a parser: the
// refusal used to wait for the build, on the CI runner that was about to run it.
func TestPlainCompileRefusesADockerfileValue(t *testing.T) {
	for _, tc := range poisonedProjects {
		t.Run(tc.name, func(t *testing.T) {
			dir := writePoisonedProject(t, projectConfigFile, tc.yaml)
			cmd := newCompileCommand()
			cmd.SetArgs([]string{dir, "--parser-cmd", "/nonexistent/parser"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if err == nil {
				t.Fatal("compile accepted the project")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("the error must name %q, got: %v", tc.field, err)
			}
		})
	}
}

// Each generator must refuse a value only the shared check catches, which is
// what proves it is wired rather than covered by a renderer guard by accident:
// the dev generator always builds FROM its own base and so never looked at
// base_image, and a `.` group short-circuits the COPY of every sibling, so
// neither the `$` nor the `<` below reaches a COPY guard in either renderer.
func TestEveryDockerfileGeneratorRunsTheSharedCheck(t *testing.T) {
	poisons := map[string]func(*domain.LeoflowConfig){
		"base_image": func(c *domain.LeoflowConfig) { c.BaseImage = "alpine AS builder" },
		"dbt_groups.b.project": func(c *domain.LeoflowConfig) {
			c.DbtGroups = map[string]*domain.DbtConfig{"a": {Project: "."}, "b": {Project: "raw/$schema"}}
		},
		"include_paths": func(c *domain.LeoflowConfig) {
			c.DbtGroups = map[string]*domain.DbtConfig{"a": {Project: "."}}
			c.IncludePaths = []string{".", "<<EOF"}
		},
	}
	generators := map[string]func(*domain.LeoflowConfig) error{
		"generatedDockerfile": func(c *domain.LeoflowConfig) error {
			_, err := generatedDockerfile(c, "dag.py")
			return err
		},
		"ensureProjectDockerfile": func(c *domain.LeoflowConfig) error {
			return ensureProjectDockerfile(devTestCmd(), t.TempDir(), c)
		},
	}
	for gname, generate := range generators {
		for field, poison := range poisons {
			t.Run(gname+"/"+field, func(t *testing.T) {
				cfg := &domain.LeoflowConfig{DagID: "d"}
				cfg.ApplyDefaults()
				poison(cfg)
				err := generate(cfg)
				if err == nil {
					t.Fatal("the generator rendered a value Validate refuses")
				}
				if !strings.Contains(err.Error(), field) {
					t.Errorf("the error must name %q, got: %v", field, err)
				}
			})
		}
	}
}

// The structural half of #1268: a Dockerfile generator is any function in this
// package that writes a `FROM ` line. Each one must call
// ValidateDockerfileValues itself, or be reached only from functions that do,
// so a third generator cannot be added without the shared check. The second
// generator having none of #1267's guards is how this issue was found.
func TestEveryFunctionThatWritesFROMIsGuarded(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string][]string{} // function -> functions it calls
	var renderers []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, f, nil, 0)
		if perr != nil {
			t.Fatal(perr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if s, uerr := strconv.Unquote(x.Value); uerr == nil && strings.HasPrefix(s, "FROM ") {
						renderers = append(renderers, name)
					}
				case *ast.CallExpr:
					switch fun := x.Fun.(type) {
					case *ast.Ident:
						calls[name] = append(calls[name], fun.Name)
					case *ast.SelectorExpr:
						calls[name] = append(calls[name], fun.Sel.Name)
					}
				}
				return true
			})
		}
	}
	if len(renderers) < 2 {
		t.Fatalf("found %d Dockerfile generators, expected at least generatedDockerfile and devDockerfile: the scan is broken", len(renderers))
	}
	guarded := func(fn string) bool { return slices.Contains(calls[fn], "ValidateDockerfileValues") }
	for _, r := range renderers {
		if guarded(r) {
			continue
		}
		var callers []string
		for fn, callees := range calls {
			if slices.Contains(callees, r) {
				callers = append(callers, fn)
			}
		}
		if len(callers) == 0 {
			t.Errorf("%s writes a FROM line, does not call ValidateDockerfileValues, and has no caller that does", r)
		}
		for _, c := range callers {
			if !guarded(c) {
				t.Errorf("%s writes a FROM line without calling ValidateDockerfileValues, and its caller %s does not call it either", r, c)
			}
		}
	}
}
