package cli

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
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
	// whole: union of every project's pip dependencies, the highest declared
	// python_version, and otherwise schema defaults. It is the single cfg the
	// venv setup uses; multi-DAG aware code paths should iterate Projects
	// instead.
	RootCfg *domain.LeoflowConfig
}

// ResolveWorkspace scans the workspace directory and returns its full
// WorkspaceSpec. It is the single entry point lite uses to convert a directory
// into "the set of DAGs I am responsible for"; both the watcher and the compile
// loop call it on every reload.
//
// Behavior:
//   - Walks subdirs via DiscoverProjects (max-depth 5, skips exclude_paths
//     defaults + hidden dirs, fails loud on duplicate dag_id).
//   - Synthesizes RootCfg by unioning every project's Dependencies (de-duped)
//     and picking the highest PythonVersion (per-component numeric compare,
//     so "3.10" > "3.9"). Detects per-package version conflicts in the
//     dependency union: two projects pinning the same package to different
//     versions cannot share a venv, so the workspace fails to resolve
//     ([[simple-reliable-then-grow]] — loud reject over silent merge).
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
	projects, err := DiscoverProjects(abs)
	if err != nil {
		return nil, err
	}
	root := &domain.LeoflowConfig{}
	root.ApplyDefaults()
	if len(projects) > 0 {
		deps, derr := unionDependencies(projects)
		if derr != nil {
			return nil, derr
		}
		root.Dependencies = deps
		if py := highestPythonVersion(projects); py != "" {
			root.PythonVersion = py
		}
	}
	return &WorkspaceSpec{Path: abs, Projects: projects, RootCfg: root}, nil
}

// WatchedPaths returns the file paths the mtime-polling watcher should track:
// every project's leoflow.yaml (when present) and dag.py. A save in any of
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

// unionDependencies returns the de-duplicated union of pip specifiers across
// every project. Two projects pinning the same package name to different
// specifiers is a hard error: a shared venv cannot host conflicting versions,
// so we refuse to compile rather than silently picking one ([[simple-reliable-
// then-grow]]). The error names the package and lists every (project,
// specifier) pair so the user can reconcile.
//
// Identity rule: package names are compared case-insensitively (PEP 503
// canonicalization), so "Pandas==2.1.0" and "pandas==2.1.0" are the same
// package. Identical full specifiers (case-insensitive on the name) are
// merged.
func unionDependencies(projects []Project) ([]string, error) {
	// pkg name (lower-cased) -> [observations]. Each observation records the
	// canonical specifier and the project that declared it; we report a
	// conflict iff a single key gathered multiple distinct specifiers.
	type obs struct {
		spec    string
		project string
	}
	byPkg := map[string][]obs{}
	for _, p := range projects {
		for _, dep := range p.Config.Dependencies {
			rawName := pipPackageName(dep)
			if rawName == "" {
				// Malformed specifier — surface in the error message rather
				// than silently dropping.
				return nil, fmt.Errorf("project %q has malformed pip specifier %q in dependencies", p.DagID, dep)
			}
			name := strings.ToLower(rawName)
			// Canonicalize the spec by lower-casing the name portion only —
			// "Pandas==2.1.0" and "pandas==2.1.0" must be treated as the same
			// specifier for conflict detection. Version selectors and extras
			// keep their original casing (they are case-sensitive in pip).
			canonSpec := name + strings.TrimPrefix(strings.TrimSpace(dep), rawName)
			byPkg[name] = append(byPkg[name], obs{spec: canonSpec, project: p.DagID})
		}
	}
	// Detect conflicts: a single package with two distinct specifiers across
	// projects.
	for pkg, obss := range byPkg {
		distinct := map[string][]string{}
		for _, o := range obss {
			distinct[o.spec] = append(distinct[o.spec], o.project)
		}
		if len(distinct) <= 1 {
			continue
		}
		// Build a deterministic error message listing every conflicting
		// (spec, projects) tuple.
		var b strings.Builder
		fmt.Fprintf(&b, "multi-DAG workspace cannot reconcile pip dependency %q across projects (a shared venv cannot host conflicting versions):\n", pkg)
		specs := make([]string, 0, len(distinct))
		for s := range distinct {
			specs = append(specs, s)
		}
		sort.Strings(specs)
		for _, s := range specs {
			ps := append([]string(nil), distinct[s]...)
			sort.Strings(ps)
			fmt.Fprintf(&b, "  - %q in: %s\n", s, strings.Join(ps, ", "))
		}
		b.WriteString("Pin a single version across the projects, or split them into separate workspaces.")
		return nil, fmt.Errorf("%s", b.String())
	}
	// No conflict: collect the canonical (first-seen) specifier for each pkg.
	out := make([]string, 0, len(byPkg))
	for _, obss := range byPkg {
		out = append(out, obss[0].spec)
	}
	sort.Strings(out)
	return out, nil
}

// pipPackageNamePattern matches the package name at the start of a pip
// specifier. Per PEP 508, a name is composed of letters, digits, `-`, `_`,
// `.`; everything after that is extras (`[...]`), a version specifier
// (`==`, `>=`, `~=`, etc.), an environment marker (`;`), or a URL hint.
var pipPackageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*`)

// pipPackageName extracts the package name from a pip specifier so the
// dependency-conflict detector can group "pandas==2.1.0" and "pandas>=2.0"
// under the same key. Returns "" for malformed input — caller decides whether
// to error.
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
