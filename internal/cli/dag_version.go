package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/version"
)

// noGitBase is the base of the default label for a project outside git.
const noGitBase = "dev"

// maxImageTag is the longest tag an OCI registry accepts.
const maxImageTag = 128

// contentHashLen is how many hex characters of the content hash the default
// label carries: 48 bits, which makes two contents of one DAG sharing a
// label vanishingly unlikely.
const contentHashLen = 12

// gitRevision is what git says about the commit a project directory is on.
// Every field is empty when the directory is not in a git work tree.
type gitRevision struct {
	sha      string // git rev-parse --short HEAD
	exactTag string // the tag on HEAD itself, for tag_strategy git_tag
}

// readGitRevision runs git in the project directory, never in the process
// working directory: `dexaflow deploy ../etl` from another checkout, or from a
// folder outside git, labeled the DAG with the wrong repository's commit.
func readGitRevision(ctx context.Context, dir string) gitRevision {
	return gitRevision{
		sha:      gitOutput(ctx, dir, "rev-parse", "--short", "HEAD"),
		exactTag: gitOutput(ctx, dir, "describe", "--tags", "--exact-match", "HEAD"),
	}
}

// gitOutput runs one git command in dir and returns its trimmed output, or ""
// when git is missing, dir is not in a work tree, or the command fails.
func gitOutput(ctx context.Context, dir string, args ...string) string {
	c := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: fixed git binary; args are literals from this file.
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// defaultDAGVersion derives the version label used when --dag-version is not
// given: `<git describe --tags --always>-<content hash>`, run in the project
// directory, or `dev-<content hash>` outside git.
//
// The server treats a re-registered identical spec as a no-op and refuses a
// known label with different content (409). A label from git alone collided
// on every edit of a dirty tree, on every deploy from a folder outside git
// ("dev"), and across every DAG of a monorepo. The content hash makes a label
// change exactly when what the DAG is built from changes; see contentHash for
// what that covers. The base carries no --dirty mark: the hash already tells
// an edited tree apart, and a mark would move the label when only an untracked
// dag.json changed. The result is also a valid image tag, because
// tag_strategy version tags the image with it.
func defaultDAGVersion(ctx context.Context, dir string, cfg *domain.LeoflowConfig, dockerfile, output string) (string, error) {
	sum, err := contentHash(dir, cfg, dockerfile, output)
	if err != nil {
		return "", err
	}
	return composeDefaultLabel(gitOutput(ctx, dir, "describe", "--tags", "--always"), sum[:contentHashLen]), nil
}

// composeDefaultLabel joins a base and the short content hash into a valid
// image tag, trimming the base so the hash always survives the length limit.
func composeDefaultLabel(base, short string) string {
	base = sanitizeImageTag(base)
	if base == "" {
		base = noGitBase
	}
	if room := maxImageTag - len(short) - 1; len(base) > room {
		base = strings.TrimRight(base[:room], ".-")
	}
	return base + "-" + short
}

// sanitizeImageTag maps s onto the OCI tag grammar [A-Za-z0-9_][A-Za-z0-9_.-]{0,127}:
// every other character becomes '-', leading '.' and '-' are dropped, and the
// result is cut at 128 characters. A git describe of a tag like release/v1 or
// v1+build produced a reference no registry accepts.
func sanitizeImageTag(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !isTagChar(c) {
			b[i] = '-'
		}
	}
	out := strings.TrimLeft(string(b), ".-")
	if len(out) > maxImageTag {
		out = out[:maxImageTag]
	}
	return out
}

func isTagChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '.' || c == '-'
}

// contentHash is the SHA-256 of everything the compiled dag.json and the image
// are derived from, in a form that is the same on every machine:
//
//   - the resolved dexaflow.yaml (dependencies, connectors, python_version,
//     base_image, overlays), canonical JSON rather than raw bytes;
//   - the CLI version, which picks the default base image and the parser;
//   - the content of every file the generated Dockerfile COPYs: the DAG source,
//     dbt project directories, include_paths and dbt.manifest; with a project
//     Dockerfile, which can COPY anything, the whole project directory;
//   - the project Dockerfile itself.
//
// Files are hashed by their slash-separated path relative to the project and
// their bytes, in sorted order; modification times never count. It leaves out
// what the build cannot see or what every compile rewrites: .git, exclude_paths
// matches, the compiled dag.json, the generated Dockerfile, and the target/ and
// logs/ that `dbt parse` writes. The image digest is not an input: it does not
// exist before the build.
func contentHash(dir string, cfg *domain.LeoflowConfig, dockerfile, output string) (string, error) {
	h := sha256.New()
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encoding project config for the version label: %w", err)
	}
	hashField(h, "config", cfgJSON)
	hashField(h, "cli", []byte(version.Get().Version))
	w := contentWalker{dir: dir, cfg: cfg, output: output, h: h}
	roots := w.roots(dockerfile)
	for _, root := range roots {
		if werr := w.hashRoot(root); werr != nil {
			return "", werr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashField writes a length-prefixed field, so two fields can never run
// together into the same byte stream.
func hashField(h hash.Hash, name string, data []byte) {
	h.Write([]byte(name + "\x00" + strconv.Itoa(len(data)) + "\x00")) //nolint:errcheck // hash.Hash.Write never fails
	h.Write(data)                                                     //nolint:errcheck // hash.Hash.Write never fails
}

// contentWalker hashes the files of one project directory.
type contentWalker struct {
	dir    string
	cfg    *domain.LeoflowConfig
	output string
	h      hash.Hash
}

// roots lists the project-relative paths whose content shapes the image,
// mirroring what generatedDockerfile COPYs (copiedRoots), sorted.
func (w contentWalker) roots(dockerfile string) []string {
	own := projectDockerfile(w.dir, w.cfg, dockerfile)
	if own != "" {
		return []string{"."}
	}
	roots := []string{filepath.Clean(w.cfg.DagSource)}
	roots = append(roots, copiedRoots(w.cfg)...)
	if w.cfg.Dbt != nil && w.cfg.Dbt.Project != "" && w.cfg.Dbt.Manifest != "" {
		roots = append(roots, filepath.Join(filepath.Clean(w.cfg.Dbt.Project), w.cfg.Dbt.Manifest))
	}
	slices.Sort(roots)
	roots = slices.Compact(roots)
	if slices.Contains(roots, ".") {
		return []string{"."}
	}
	return roots
}

// projectDockerfile returns the project's own Dockerfile name when one exists
// (the one the build would honor verbatim), else "".
func projectDockerfile(dir string, cfg *domain.LeoflowConfig, dockerfile string) string {
	name := dockerfile
	if cfg.Build != nil && cfg.Build.Dockerfile != "" && (name == "" || name == "Dockerfile") {
		name = cfg.Build.Dockerfile
	}
	if name == "" {
		name = "Dockerfile"
	}
	if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.Mode().IsRegular() {
		return name
	}
	return ""
}

// hashRoot hashes one root, a file or a directory walked in lexical order. A
// root that does not exist is recorded as absent: the compile reports it with
// its own message, and the label still differs from the one with the file.
func (w contentWalker) hashRoot(root string) error {
	start := filepath.Join(w.dir, root)
	if _, err := os.Stat(start); errors.Is(err, fs.ErrNotExist) {
		hashField(w.h, "absent", []byte(filepath.ToSlash(root)))
		return nil
	}
	err := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(w.dir, path)
		if rerr != nil {
			return rerr
		}
		if w.skip(rel, path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		data, ferr := os.ReadFile(path) //nolint:gosec // G304: a file inside the operator's own project directory.
		if ferr != nil {
			return ferr
		}
		hashField(w.h, filepath.ToSlash(rel), data)
		return nil
	})
	if err != nil {
		return fmt.Errorf("hashing %s for the version label: %w", start, err)
	}
	return nil
}

// skip reports whether a project-relative path stays out of the hash.
func (w contentWalker) skip(rel, abs string) bool {
	if rel == "." {
		return false
	}
	slash := filepath.ToSlash(rel)
	base := filepath.Base(rel)
	if base == ".git" || strings.HasPrefix(base, ".leoflow") || base == dockerignoreName {
		return true
	}
	if slash == "dag.json" || (w.output != "" && filepath.Clean(abs) == filepath.Clean(w.output)) {
		return true
	}
	if w.dbtArtifact(slash) {
		return true
	}
	return matchesExclude(w.cfg.ExcludePaths, slash, base)
}

// dbtArtifact reports whether slash is a `dbt parse` output inside a dbt
// project directory (target/, logs/, .user.yml). The configured dbt.manifest
// is a separate root, so it still counts when it lives under target/.
func (w contentWalker) dbtArtifact(slash string) bool {
	dirs := dbtGroupProjectDirs(w.cfg)
	if w.cfg.Dbt != nil && w.cfg.Dbt.Project != "" {
		dirs = append(dirs, filepath.Clean(w.cfg.Dbt.Project))
	}
	for _, d := range dirs {
		prefix := filepath.ToSlash(d) + "/"
		if d == "." {
			prefix = ""
		}
		for _, a := range []string{"target", "logs", ".user.yml"} {
			if slash == prefix+a {
				return true
			}
		}
	}
	return false
}

// matchesExclude applies exclude_paths the simple way the build context sees
// most of them: a pattern matches the path or its base name, with a trailing
// slash ignored.
func matchesExclude(patterns []string, slash, base string) bool {
	for _, raw := range patterns {
		p := strings.TrimSuffix(strings.TrimSpace(raw), "/")
		if p == "" {
			continue
		}
		if ok, _ := filepath.Match(p, slash); ok { //nolint:errcheck // a malformed pattern matches nothing
			return true
		}
		if ok, _ := filepath.Match(p, base); ok { //nolint:errcheck // a malformed pattern matches nothing
			return true
		}
	}
	return false
}
