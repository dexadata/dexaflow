package cli

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
)

// WorkspaceSpec is the resolved view of a Lite workspace: the list of DAG
// projects discovered under it plus a synthesized "root" config that single-
// cfg consumers (venv setup, image-build defaults) can still rely on without
// knowing about multi-DAG. The Path is the absolute workspace directory.
type WorkspaceSpec struct {
	// Path is the absolute path of the workspace directory.
	Path string
	// Projects is every DAG project discovered under Path. Empty when the
	// workspace is empty (caller is expected to scaffold a starter then).
	Projects []Project
	// RootCfg is a synthesized LeoflowConfig representing the workspace as a
	// whole: the highest declared python_version and otherwise schema defaults.
	// It carries no dependencies: Lite provisions one venv per DAG from each
	// project's own `dependencies:` (ensureWorkspaceDagVenvs), so multi-DAG
	// aware code paths iterate Projects instead.
	RootCfg *domain.LeoflowConfig
	// broken lists the dexaflow.yaml files discovery could not parse.
	broken []brokenConfig
}

// ResolveWorkspace scans the workspace directory and returns its full
// WorkspaceSpec. It is the single entry point lite uses to convert a directory
// into "the set of DAGs I am responsible for"; both the watcher and the compile
// loop call it on every reload.
//
// Behavior:
//   - Walks subdirs via DiscoverProjects (max-depth 5, skips exclude_paths
//     defaults + hidden dirs, fails loud on duplicate dag_id).
//   - Validates that every project's dependencies are pip specifiers, naming
//     the project and the bad entry. Two projects pinning the same package to
//     different versions is NOT an error: each DAG gets its own venv under
//     ~/.dexaflow/dev/venvs/<dag_id>/ and its tasks run with that interpreter
//     (#346, #1458), so the pins never meet (#1679).
//   - Synthesizes RootCfg with the highest PythonVersion (per-component
//     numeric compare, so "3.10" > "3.9").
//   - Returns a non-nil RootCfg even when Projects is empty, so the caller
//     can scaffold against schema defaults without an extra nil-check.
//
// Discovery errors (e.g. duplicate dag_id) are propagated verbatim so the
// caller can surface every colliding path to the user.
func ResolveWorkspace(dir string) (*WorkspaceSpec, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	projects, broken, err := discoverWorkspace(abs)
	if err != nil {
		return nil, err
	}
	root := &domain.LeoflowConfig{}
	root.ApplyDefaults()
	if len(projects) > 0 {
		if derr := validateDependencies(projects); derr != nil {
			return nil, derr
		}
		if py := highestPythonVersion(projects); py != "" {
			root.PythonVersion = py
		}
	}
	return &WorkspaceSpec{Path: abs, Projects: projects, RootCfg: root, broken: broken}, nil
}

// WatchedPaths returns the file paths the mtime-polling watcher should track:
// every project's dexaflow.yaml (when present) and dag.py. A save in any of
// them must trigger a reload, since lite recompiles+reregisters every project
// on each reload.
func (w *WorkspaceSpec) WatchedPaths() []string {
	if w == nil {
		return nil
	}
	paths := make([]string, 0, len(w.Projects)*3)
	for _, p := range w.Projects {
		if p.HasYAML {
			paths = append(paths, p.ConfigPath)
		}
		// Always watch the DAG source, even for a project whose config says dbt.
		// projectAt keys on the source EXISTING while this used to key on
		// Dbt != nil, and the two disagree in exactly one shape: a project
		// carrying both, which compile now refuses. Watching only dbt_project.yml
		// there meant the user read "delete dag.py", deleted it, and nothing
		// reloaded — no watched path's mtime moved and the import-error banner is
		// only cleared by a successful reload. Watching a normally-absent path is
		// already safe: projectMtimes skips what it cannot stat.
		paths = append(paths, filepath.Join(p.Path, p.Config.DagSource))
		if p.Config.Dbt != nil {
			// A pure dbt project has no dag.py; watch its dbt project file so a
			// model/config edit still triggers a reload.
			paths = append(paths, filepath.Join(p.Path, p.Config.Dbt.Project, "dbt_project.yml"))
		}
	}
	return paths
}

// validateDependencies checks that every project's `dependencies:` entry starts
// with a pip package name, so a malformed entry is reported with its project
// before any venv is provisioned. It does not compare projects with each other:
// each DAG resolves its pins in its own venv, so differing pins across DAGs are
// expected (#1679).
func validateDependencies(projects []Project) error {
	for _, p := range projects {
		if p.Config == nil {
			continue
		}
		for _, dep := range p.Config.Dependencies {
			if pipPackageName(dep) == "" {
				return fmt.Errorf("project %q has malformed pip specifier %q in dependencies", p.DagID, dep)
			}
		}
	}
	return nil
}

// pipPackageNamePattern matches the package name at the start of a pip
// specifier. Per PEP 508, a name is composed of letters, digits, `-`, `_`,
// `.`; everything after that is extras (`[...]`), a version specifier
// (`==`, `>=`, `~=`, etc.), an environment marker (`;`), or a URL hint.
var pipPackageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*`)

// pipPackageName extracts the package name from a pip specifier ("pandas" from
// "pandas>=2.0"). Returns "" for malformed input; the caller decides whether to
// error.
func pipPackageName(spec string) string {
	return pipPackageNamePattern.FindString(strings.TrimSpace(spec))
}

// highestPythonVersion returns the highest declared python_version across
// projects (e.g. "3.12" beats "3.11", and "3.10" beats "3.9" — naive string
// compare would get the second case wrong). Empty when no project has it
// pinned to a non-default value (the caller keeps the schema default then).
func highestPythonVersion(projects []Project) string {
	var best string
	for _, p := range projects {
		v := p.Config.PythonVersion
		if v == "" {
			continue
		}
		if best == "" || pythonVersionLess(best, v) {
			best = v
		}
	}
	return best
}

// pythonVersionLess reports whether a < b for python version strings like
// "3.11". Components are compared numerically (so "3.10" > "3.9", which naive
// string comparison gets wrong since '1' < '9'). Non-numeric or malformed
// components fall back to string compare so the function never panics on
// bad input — caller filters to the schema-allowed set (the python_version enum
// in internal/domain/schemas/leoflow-yaml-schema.json, reachable as
// domain.SupportedPythonVersions) before relying on the result. Naming the
// versions here was a fourth copy of that list and had already gone stale.
func pythonVersionLess(a, b string) bool {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	n := len(aParts)
	if len(bParts) < n {
		n = len(bParts)
	}
	for i := 0; i < n; i++ {
		ai, aerr := strconv.Atoi(aParts[i])
		bi, berr := strconv.Atoi(bParts[i])
		if aerr != nil || berr != nil {
			// Malformed component: fall back to string compare for this slot.
			if aParts[i] != bParts[i] {
				return aParts[i] < bParts[i]
			}
			continue
		}
		if ai != bi {
			return ai < bi
		}
	}
	// All shared components equal: the shorter version is "less" (e.g. "3.10"
	// < "3.10.0") so the longer wins.
	return len(aParts) < len(bParts)
}
