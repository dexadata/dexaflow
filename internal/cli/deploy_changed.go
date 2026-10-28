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
// merge base: uncommitted and untracked files count, and a change made on
// the ref's side since the branch point counts too. A file git ignores
// cannot be compared, so a project built from one counts as changed. That is
// the safe reading for a deploy: a project is skipped only when every file it
// is built from is the same as at the ref.
type changeSet struct {
	ref    string
	commit string
	top    string          // the repository's top level, symlink-resolved
	paths  map[string]bool // changed paths, absolute
	sorted []string        // paths, sorted
	// submodules are the absolute paths of the repository's submodules:
	// the diff compares the commit each is on (and its dirty state), and each
	// answers for its own ignored files.
	submodules []string
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
	return gitCaptureIn(ctx, dir, "", args...)
}

// gitCaptureIn is gitCapture with stdin.
func gitCaptureIn(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: fixed git binary; the one user value is a ref checked by readChangeSet.
	c.Dir = dir
	c.Env = gitEnv(os.Environ())
	c.Stdin = strings.NewReader(stdin)
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
	// Repository settings must not hide a change: a submodule ignored by
	// .gitmodules or diff.ignoreSubmodules, or diff.relative.
	diff, err := gitCapture(ctx, top, "diff", "--name-only", "--no-renames", "--no-relative",
		"--ignore-submodules=none", "-z", commit, "--")
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
	if cs.submodules, err = listSubmodules(ctx, top); err != nil {
		return nil, err
	}
	slices.Sort(cs.sorted)
	return cs, nil
}

// listSubmodules returns the absolute paths of the submodules the index of
// the repository at top records (mode 160000).
func listSubmodules(ctx context.Context, top string) ([]string, error) {
	out, err := gitCapture(ctx, top, "ls-files", "-z", "--stage")
	if err != nil {
		return nil, fmt.Errorf("listing the submodules: %w", err)
	}
	var subs []string
	for _, entry := range strings.Split(out, "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		if ok && strings.HasPrefix(meta, "160000 ") {
			subs = append(subs, filepath.Join(top, filepath.FromSlash(name)))
		}
	}
	return subs, nil
}

// resolveChangedRef resolves ref to a commit, and says how to get it when the
// clone does not have it: a shallow clone (the CI default) needs its history.
func resolveChangedRef(ctx context.Context, top, ref string) (string, error) {
	out, err := gitCapture(ctx, top, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
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
// contentHash, so the same files its image is built from), its config file,
// .dockerignore or Dockerfile, a file that no longer exists under one of the
// roots that walk starts from, or a directory holding the project (a
// submodule). A project git cannot compare counts as changed: one outside the
// repository, one built from a file git ignores or from outside the
// repository, and one whose inputs cannot be listed (its deploy then reports
// the error).
func (c *changeSet) projectChanged(ctx context.Context, p Project, dockerfile string) projectChange {
	dir, err := resolvedDir(p.Path)
	if err != nil {
		return projectChange{true, "its directory could not be resolved: " + err.Error()}
	}
	if !holds(c.top, dir) {
		return projectChange{true, "its directory " + dir + " is outside the repository at " + c.top + ", so git cannot compare it"}
	}
	inputs := map[string]bool{}
	roots, err := walkLabelInputs(dir, p.Config, dockerfile, filepath.Join(dir, "dag.json"), sha256.New(),
		func(f string) { inputs[f] = true })
	if err != nil {
		return projectChange{true, "its inputs could not be listed (" + err.Error() + "), so it is deployed"}
	}
	if why := c.uncomparable(ctx, inputs); why != "" {
		return projectChange{true, why}
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

// uncomparable names the first label input git cannot compare with the ref:
// a file outside the repository, or one git ignores (a generated file, dbt
// deps output) that the image ships all the same. "" when every input is
// tracked or untracked and visible.
func (c *changeSet) uncomparable(ctx context.Context, inputs map[string]bool) string {
	files := make([]string, 0, len(inputs))
	for f := range inputs {
		if st, err := os.Lstat(f); err == nil && !st.IsDir() {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	// Each file is asked of the repository that holds it: the innermost
	// submodule, whose own .gitignore decides, else the top.
	groups := map[string][]string{}
	var order []string
	for _, f := range files {
		if !holds(c.top, f) {
			return "built from " + f + ", which is outside the repository at " + c.top
		}
		repo := c.repoOf(f)
		if _, ok := groups[repo]; !ok {
			order = append(order, repo)
		}
		groups[repo] = append(groups[repo], f)
	}
	for _, repo := range order {
		if why := c.ignoredIn(ctx, repo, groups[repo]); why != "" {
			return why
		}
	}
	return ""
}

// repoOf returns the directory of the repository that tracks f: the
// innermost submodule holding it, else the top.
func (c *changeSet) repoOf(f string) string {
	repo := c.top
	for _, s := range c.submodules {
		if f != s && holds(s, f) && len(s) > len(repo) {
			repo = s
		}
	}
	return repo
}

// ignoredIn asks git, in the repository at repo, whether it ignores any of
// files, and names the first one it does. check-ignore exits 1 when nothing
// is ignored; a tracked file is never reported, since git compares it
// whatever the ignore rules say.
func (c *changeSet) ignoredIn(ctx context.Context, repo string, files []string) string {
	var stdin strings.Builder
	for _, f := range files {
		r, err := filepath.Rel(repo, f)
		if err != nil {
			return "git could not say whether it ignores " + f + ", so it is deployed"
		}
		stdin.WriteString(filepath.ToSlash(r) + "\x00")
	}
	out, err := gitCaptureIn(ctx, repo, stdin.String(), "check-ignore", "-z", "--stdin")
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return "git could not say which of its inputs it ignores (" + err.Error() + "), so it is deployed"
	}
	ignored := strings.Split(out, "\x00")
	slices.Sort(ignored)
	for _, f := range ignored {
		if f != "" {
			return "built from " + c.rel(filepath.Join(repo, filepath.FromSlash(f))) +
				", which git ignores, so it cannot be compared with " + c.ref
		}
	}
	return ""
}

// holds reports whether directory parent holds child (or is it).
func holds(parent, child string) bool {
	r, err := filepath.Rel(parent, child)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// removedUnder reports whether changed is a path that no longer exists (or a
// root itself, a symlink that was moved) under one of the walk's roots, as
// named or with its symlinks resolved, and not left out by the walk's skip
// rules: the walk cannot see a deleted file, but the label changed with it.
// The .dockerignore is not applied here, which errs toward deploying.
func removedUnder(w *contentWalker, dir string, roots []hashRootSpec, changed string) bool {
	_, statErr := os.Lstat(changed)
	gone := errors.Is(statErr, fs.ErrNotExist)
	for _, r := range roots {
		root := filepath.Join(dir, r.name)
		if changed == root {
			return true
		}
		if !gone {
			continue
		}
		if removedBelow(w, root, root, changed) {
			return true
		}
		if target := linkTarget(root); target != root && removedBelow(w, target, root, changed) {
			return true
		}
	}
	return false
}

// linkTarget resolves the symlinks of p, following a link whose target no
// longer exists by its text (a root left dangling by the very deletion being
// judged). It returns p when there is nothing to follow.
func linkTarget(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	cur := p
	for range 40 {
		target, err := os.Readlink(cur)
		if err != nil {
			return cur
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(cur), target)
		}
		cur = filepath.Clean(target)
	}
	return cur
}

// removedBelow reports whether the deleted path changed lies under base (a
// root, or the root with its symlinks resolved) and the walk would have kept
// it, judged by its path under the root's name in the project.
func removedBelow(w *contentWalker, base, root, changed string) bool {
	if !holds(base, changed) {
		return false
	}
	under, err := filepath.Rel(base, changed)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(w.dir, filepath.Join(root, under))
	return err == nil && !skippedPath(w, w.dir, rel)
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

// movedProject is a removed project whose dag_id a current project has: it
// moved, and is deployed from its new folder.
type movedProject struct {
	DagID string `json:"dag_id"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// removedProjects lists the projects the ref has and the working tree does
// not: a deleted config file or DAG source inside the workspace whose
// directory is no longer a project. One whose dag_id a current project still
// has (current maps dag_id to its directory) moved, and is returned apart.
func (c *changeSet) removedProjects(ctx context.Context, wsDir string, current map[string]string) ([]removedProject, []movedProject) {
	seen := map[string]bool{}
	var out []removedProject
	var moved []movedProject
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
		id := c.removedDagID(ctx, dir)
		if to, ok := current[id]; ok {
			moved = append(moved, movedProject{DagID: id, From: c.rel(dir), To: c.rel(to)})
			continue
		}
		out = append(out, removedProject{Path: c.rel(dir), DagID: id,
			Reason: "removed since " + c.ref + "; nothing is deployed or deregistered for it"})
	}
	return out, moved
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
