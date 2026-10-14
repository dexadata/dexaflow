package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/dexadata/dexaflow/internal/domain"
)

// changeSet is what git says differs between a ref and the working tree of
// the repository holding a workspace: deploy --changed-since.
//
// The ref is compared with the working tree, not with HEAD and not from the
// merge base: uncommitted and untracked (not ignored) files count, and a
// change made on the ref's side since the branch point counts too. That is
// the safe reading for a deploy: a project is skipped only when every file it
// is built from is the same as at the ref.
type changeSet struct {
	ref    string
	commit string
	top    string          // the repository's top level, symlink-resolved
	paths  map[string]bool // changed paths, absolute
	sorted []string        // paths, sorted
}

// projectChange is the verdict on one project: changed, and why.
type projectChange struct {
	changed bool
	reason  string
}

// removedProject is a project the ref has and the working tree no longer
// does. Nothing is deployed or deregistered for it.
type removedProject struct {
	Path   string `json:"path"`
	DagID  string `json:"dag_id,omitempty"`
	Reason string `json:"reason"`
}

// gitCapture runs git in dir and returns its stdout, or an error carrying its
// stderr.
func gitCapture(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: fixed git binary; the one user value is a ref checked by readChangeSet.
	c.Dir = dir
	c.Env = gitEnv(os.Environ())
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// readChangeSet resolves ref in the repository around dir and lists every
// path that differs between it and the working tree.
func readChangeSet(ctx context.Context, dir, ref string) (*changeSet, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return nil, fmt.Errorf("--changed-since %q is not a git ref; name a commit, branch or tag", ref)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("--changed-since compares with git, which is not on PATH: %w", err)
	}
	out, err := gitCapture(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("--changed-since compares with git, but %s is not in a git repository: %w", dir, err)
	}
	top, err := resolvedDir(strings.TrimSpace(out))
	if err != nil {
		return nil, err
	}
	commit, err := resolveChangedRef(ctx, top, ref)
	if err != nil {
		return nil, err
	}
	cs := &changeSet{ref: ref, commit: commit, top: top, paths: map[string]bool{}}
	diff, err := gitCapture(ctx, top, "diff", "--name-only", "--no-renames", "-z", commit, "--")
	if err != nil {
		return nil, fmt.Errorf("listing the files changed since %s: %w", ref, err)
	}
	untracked, err := gitCapture(ctx, top, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("listing the untracked files: %w", err)
	}
	for _, rel := range strings.Split(diff+untracked, "\x00") {
		if rel != "" {
			cs.paths[filepath.Join(top, filepath.FromSlash(rel))] = true
		}
	}
	for p := range cs.paths {
		cs.sorted = append(cs.sorted, p)
	}
	slices.Sort(cs.sorted)
	return cs, nil
}

// resolveChangedRef resolves ref to a commit, and says how to get it when the
// clone does not have it: a shallow clone (the CI default) needs its history.
func resolveChangedRef(ctx context.Context, top, ref string) (string, error) {
	out, err := gitCapture(ctx, top, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err == nil {
		return strings.TrimSpace(out), nil
	}
	if shallow, _ := gitCapture(ctx, top, "rev-parse", "--is-shallow-repository"); strings.TrimSpace(shallow) == "true" { //nolint:errcheck // only words the error
		return "", fmt.Errorf("--changed-since %q: the ref is not in this clone, which is shallow. "+
			"Fetch the history it needs (actions/checkout with fetch-depth: 0, or git fetch --unshallow), then deploy again", ref)
	}
	return "", fmt.Errorf("--changed-since %q: no such commit, branch or tag in %s; fetch it first (for example git fetch origin main) or name a commit", ref, top)
}

// rel names p relative to the repository, slash separated.
func (c *changeSet) rel(p string) string {
	if r, err := filepath.Rel(c.top, p); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}

// sharedChange names the first changed workspace-level file, which changes
// every project, or "" when none changed.
func (c *changeSet) sharedChange(wsDir string) string {
	for _, name := range []string{workspaceFileName, legacyWorkspaceFileName} {
		if p := filepath.Join(wsDir, name); c.paths[p] {
			return c.rel(p)
		}
	}
	return ""
}

// projectChanged decides whether a project changed since the ref: a changed
// path is one of the files its version label is computed from (the walk of
// contentHash, so the same files the image is built from), its config file,
// .dockerignore or Dockerfile, a file that no longer exists under one of the
// roots that walk starts from, or a directory holding the project (a
// submodule). A project whose inputs cannot be listed counts as changed, so
// its deploy reports the error.
func (c *changeSet) projectChanged(p Project, dockerfile string) projectChange {
	dir, err := resolvedDir(p.Path)
	if err != nil {
		return projectChange{true, "its directory could not be resolved: " + err.Error()}
	}
	inputs := map[string]bool{}
	roots, err := walkLabelInputs(dir, p.Config, dockerfile, filepath.Join(dir, "dag.json"), sha256.New(),
		func(f string) { inputs[f] = true })
	if err != nil {
		return projectChange{true, "its inputs could not be listed (" + err.Error() + "), so it is deployed"}
	}
	fixed := []string{projectConfigFile, legacyProjectConfigFile, dockerignoreName, "Dockerfile", dockerfile}
	if p.Config.Build != nil && p.Config.Build.Dockerfile != "" {
		fixed = append(fixed, p.Config.Build.Dockerfile)
	}
	for _, f := range fixed {
		inputs[filepath.Join(dir, filepath.Clean(f))] = true
	}
	w := &contentWalker{dir: dir, cfg: p.Config}
	for _, changed := range c.sorted {
		if inputs[changed] || holds(changed, dir) || removedUnder(w, dir, roots, changed) {
			return projectChange{true, c.rel(changed) + " changed since " + c.ref}
		}
	}
	return projectChange{false, "unchanged since " + c.ref}
}

// holds reports whether directory parent holds child (or is it).
func holds(parent, child string) bool {
	r, err := filepath.Rel(parent, child)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// removedUnder reports whether changed is a path that no longer exists (or a
// root itself, a symlink that was moved) under one of the walk's roots, and
// not left out by the walk's skip rules: the walk cannot see a deleted file,
// but the label changed with it.
func removedUnder(w *contentWalker, dir string, roots []hashRootSpec, changed string) bool {
	_, statErr := os.Lstat(changed)
	gone := errors.Is(statErr, fs.ErrNotExist)
	for _, r := range roots {
		root := filepath.Join(dir, r.name)
		if changed == root {
			return true
		}
		if !gone || !holds(root, changed) {
			continue
		}
		rel, err := filepath.Rel(dir, changed)
		if err != nil || skippedPath(w, dir, rel) {
			continue
		}
		return true
	}
	return false
}

// skippedPath reports whether the walk skips rel or one of its parent
// directories.
func skippedPath(w *contentWalker, dir, rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := range parts {
		sub := filepath.FromSlash(path.Join(parts[:i+1]...))
		if w.skip(sub, filepath.Join(dir, sub)) {
			return true
		}
	}
	return false
}

// removedProjects lists the projects the ref has and the working tree does
// not: a deleted config file or DAG source inside the workspace whose
// directory is no longer a project.
func (c *changeSet) removedProjects(ctx context.Context, wsDir string) []removedProject {
	seen := map[string]bool{}
	var out []removedProject
	for _, p := range c.sorted {
		base := filepath.Base(p)
		if base != projectConfigFile && base != legacyProjectConfigFile && base != "dag.py" {
			continue
		}
		dir := filepath.Dir(p)
		if seen[dir] || !holds(wsDir, dir) || fileExists(p) || isProjectDir(dir) {
			continue
		}
		seen[dir] = true
		out = append(out, removedProject{Path: c.rel(dir), DagID: c.removedDagID(ctx, dir),
			Reason: "removed or moved since " + c.ref + "; nothing is deployed or deregistered for it"})
	}
	return out
}

// removedDagID reads the dag_id of a removed project from its config file at
// the ref, else names it after its directory, as discovery does.
func (c *changeSet) removedDagID(ctx context.Context, dir string) string {
	for _, name := range []string{projectConfigFile, legacyProjectConfigFile} {
		data, err := gitCapture(ctx, c.top, "show", c.commit+":"+c.rel(filepath.Join(dir, name)))
		if err != nil {
			continue
		}
		var cfg domain.LeoflowConfig
		if yaml.Unmarshal([]byte(data), &cfg) == nil && cfg.DagID != "" {
			return cfg.DagID
		}
	}
	return filepath.Base(dir)
}
