package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
)

// MaxWorkspaceDepth caps the recursion DiscoverProjects performs from the
// workspace root. <ws>/a/b/c/d/dag.py is the deepest allowed (depth 5);
// any project nested deeper is silently skipped. See
// docs/dag-authoring.md#discovery-rules for the rationale (workspace
// navigability + protection against pathological layouts).
const MaxWorkspaceDepth = 5

// dagSourceFile is the canonical DAG source filename DiscoverProjects looks
// for. The schema's `dag_source` field can rename it inside a single project,
// but discovery uses the canonical name to decide whether a directory is a
// project at all.
const dagSourceFile = "dag.py"

// defaultExcludeDirs are the directory names skipped during discovery. They
// mirror the JSON-Schema `exclude_paths` default so a project's image build
// and the workspace scan agree on what's noise. Hidden directories (any name
// starting with ".") are also skipped — captured separately rather than
// enumerating every dotfile.
var defaultExcludeDirs = map[string]struct{}{
	".git":        {},
	"__pycache__": {},
	".venv":       {},
	"venv":        {},
}

// Project is a single DAG project discovered in the workspace by
// DiscoverProjects. The fields are populated from dexaflow.yaml (when present)
// or synthesized from the subdirectory's basename otherwise.
type Project struct {
	// Path is the absolute path to the project directory containing dag.py.
	Path string
	// DagID resolves to the yaml's dag_id when present, else the subdir
	// basename.
	DagID string
	// ConfigPath is the absolute path to dexaflow.yaml when one exists, or
	// empty when DiscoverProjects synthesized auto-defaults. The lite watcher
	// logs this on every compile so "which config did it pick up?" is
	// greppable.
	ConfigPath string
	// HasYAML reports whether a dexaflow.yaml was present in the project dir.
	HasYAML bool
	// Config is the resolved, default-filled config for the project. Always
	// non-nil; when HasYAML is false the struct holds the schema defaults
	// plus DagID set to the subdir basename.
	Config *domain.LeoflowConfig
}

// DiscoverProjects walks workspace and returns every directory containing a
// dag.py file (the project marker). Each project's config is the dexaflow.yaml
// in the same dir — loaded via loadProjectConfig so schema defaults apply —
// or, when no yaml exists, a synthesized LeoflowConfig with DagID set to the
// subdir basename and every other field at its schema default.
//
// The walk caps at MaxWorkspaceDepth from the workspace root and skips both
// defaultExcludeDirs and any directory whose name begins with a dot. The
// workspace root itself counts as depth 0 — a root-level dag.py + dexaflow.yaml
// is still recognized as a project for backward compatibility with the
// single-DAG layout (recommended new layout is one project per subdir; see
// docs/dag-authoring.md).
//
// If two discovered projects resolve to the same dag_id (whether from yaml or
// from the subdir-basename fallback), the function returns an error naming
// the id and every colliding path — per simple-reliable-then-grow the lite
// loop refuses to compile any of them rather than silently letting the
// latest tick clobber the previous one.
func DiscoverProjects(workspace string) ([]Project, error) {
	absWs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace path: %w", err)
	}
	rootDepth := pathDepth(absWs)

	var projects []Project
	werr := filepath.WalkDir(absWs, func(path string, d fs.DirEntry, perr error) error {
		if perr != nil {
			return fmt.Errorf("walking %s: %w", path, perr)
		}
		if !d.IsDir() {
			return nil
		}
		if path != absWs {
			base := d.Name()
			if _, skip := defaultExcludeDirs[base]; skip {
				return fs.SkipDir
			}
			if strings.HasPrefix(base, ".") {
				return fs.SkipDir
			}
		}
		// Workspace root is depth 0; refuse to descend past MaxWorkspaceDepth.
		depth := pathDepth(path) - rootDepth
		if depth >= MaxWorkspaceDepth {
			if proj, ok := projectAt(path); ok {
				projects = append(projects, proj)
			}
			return fs.SkipDir
		}
		if proj, ok := projectAt(path); ok {
			projects = append(projects, proj)
		}
		return nil
	})
	if werr != nil {
		return nil, fmt.Errorf("walking workspace: %w", werr)
	}
	if derr := checkDuplicateDagIDs(projects); derr != nil {
		return nil, derr
	}
	return projects, nil
}

// pathDepth counts the number of path separators in a cleaned absolute path.
// It is used only to compute relative depth from the workspace root; the
// absolute value carries no meaning outside that comparison.
func pathDepth(p string) int {
	return strings.Count(filepath.Clean(p), string(os.PathSeparator))
}

// projectAt builds a Project for path when the directory is a DAG project. A
// project is marked by either a dag.py (a Python DAG; dexaflow.yaml optional) OR
// a dexaflow.yaml carrying a dbt: block with no dag.py (a pure dbt project, whose
// DAG is generated from the dbt manifest — the zero-config Lite path documented
// in docs/dbt.md). The returned Project's Config is fully defaulted; when no
// dexaflow.yaml exists the DagID falls back to filepath.Base(path).
func projectAt(path string) (Project, bool) {
	yamlPath := projectConfigPath(path)
	if _, err := os.Stat(filepath.Join(path, dagSourceFile)); err != nil {
		// No dag.py: the only other kind of project is a pure dbt project — a
		// dexaflow.yaml with a dbt: block. Anything else (no yaml, or a yaml with
		// neither dag.py nor dbt:) is not a project.
		return dbtOnlyProjectAt(path, yamlPath)
	}
	if _, err := os.Stat(yamlPath); err == nil {
		cfg, lerr := loadProjectConfigLenient(path)
		if lerr != nil {
			// A yaml that fails to parse is still a discovered project; its
			// config falls back to defaults + DagID = basename. The compile
			// step surfaces the parse error to the user — discovery should
			// not hide the project.
			fallback := &domain.LeoflowConfig{DagID: filepath.Base(path)}
			fallback.ApplyDefaults()
			return Project{
				Path:       path,
				DagID:      filepath.Base(path),
				ConfigPath: yamlPath,
				HasYAML:    true,
				Config:     fallback,
			}, true
		}
		id := cfg.DagID
		if id == "" {
			id = filepath.Base(path)
			cfg.DagID = id
		}
		return Project{
			Path:       path,
			DagID:      id,
			ConfigPath: yamlPath,
			HasYAML:    true,
			Config:     cfg,
		}, true
	}
	cfg := &domain.LeoflowConfig{DagID: filepath.Base(path)}
	cfg.ApplyDefaults()
	return Project{
		Path:       path,
		DagID:      filepath.Base(path),
		ConfigPath: "",
		HasYAML:    false,
		Config:     cfg,
	}, true
}

// dbtOnlyProjectAt recognizes a pure dbt project: a dexaflow.yaml carrying a dbt:
// block, with no dag.py. Its DAG is generated from the dbt manifest at compile
// time, so it has no Python source. A directory with no dexaflow.yaml, or a
// dexaflow.yaml without a dbt: block (or one that fails to parse — we can't tell
// it's dbt without parsing), is not a project.
func dbtOnlyProjectAt(path, yamlPath string) (Project, bool) {
	if _, err := os.Stat(yamlPath); err != nil {
		return Project{}, false
	}
	cfg, lerr := loadProjectConfigLenient(path)
	if lerr != nil || cfg.Dbt == nil {
		return Project{}, false
	}
	id := cfg.DagID
	if id == "" {
		id = filepath.Base(path)
		cfg.DagID = id
	}
	return Project{
		Path:       path,
		DagID:      id,
		ConfigPath: yamlPath,
		HasYAML:    true,
		Config:     cfg,
	}, true
}

// checkDuplicateDagIDs scans projects for repeated dag_ids and returns a
// single error listing every collision with all of its paths. The list is
// sorted for stable error messages across runs (tests + user-facing
// reproducibility).
func checkDuplicateDagIDs(projects []Project) error {
	byID := map[string][]string{}
	for _, p := range projects {
		byID[p.DagID] = append(byID[p.DagID], p.Path)
	}
	var ids []string
	for id, paths := range byID {
		if len(paths) > 1 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("duplicate dag_id in workspace — rename one of the colliding projects:\n")
	for _, id := range ids {
		paths := byID[id]
		sort.Strings(paths)
		fmt.Fprintf(&b, "  - %q: %s\n", id, strings.Join(paths, ", "))
	}
	return errors.New(b.String())
}
