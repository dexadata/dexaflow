package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
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
	c.Env = gitEnv(os.Environ())
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitRepoOverrides are the variables that point git at a repository other
// than the one around cmd.Dir. A hook or CI step that exports them would
// otherwise label the project with that repository's commit.
var gitRepoOverrides = []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE="}

// gitEnv returns env without gitRepoOverrides.
func gitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !slices.ContainsFunc(gitRepoOverrides, func(p string) bool { return strings.HasPrefix(kv, p) }) {
			out = append(out, kv)
		}
	}
	return out
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
// v1+build produced a reference no registry accepts. The mapping is not
// one-to-one: with tag_strategy git_tag, the git tags release/v1 and
// release-v1 both become the image tag release-v1.
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
//   - the project Dockerfile and the .dockerignore, which both shape the image.
//
// Files are hashed by their slash-separated path relative to the project and
// their bytes, in sorted order; a symlink is hashed as its target string and
// never followed, since COPY keeps links. Modification times and file modes
// never count, so a chmod alone keeps the label. It leaves out what the build
// cannot see or what every compile rewrites: .git, exclude_paths and
// .dockerignore matches, the compiled dag.json, the generated Dockerfile, and
// the files `dbt parse` rewrites (target/manifest.json and its siblings,
// logs/, .user.yml), except a dbt.manifest the project points at. The image
// digest is not an input: it does not exist before the build.
func contentHash(dir string, cfg *domain.LeoflowConfig, dockerfile, output string) (string, error) {
	h := sha256.New()
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encoding project config for the version label: %w", err)
	}
	hashField(h, "config", cfgJSON)
	hashField(h, "cli", []byte(version.Get().Version))
	w, err := newContentWalker(dir, cfg, output, h)
	if err != nil {
		return "", err
	}
	for _, root := range w.roots(dockerfile) {
		if werr := w.hashRoot(root); werr != nil {
			return "", werr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashField writes a length-prefixed field, so two fields can never run
// together into the same byte stream.
func hashField(h hash.Hash, name string, data []byte) {
	hashHeader(h, name, int64(len(data)))
	h.Write(data) //nolint:errcheck // hash.Hash.Write never fails
}

func hashHeader(h hash.Hash, name string, size int64) {
	h.Write([]byte(name + "\x00" + strconv.FormatInt(size, 10) + "\x00")) //nolint:errcheck // hash.Hash.Write never fails
}

// hashReadError names the file the label could not read and the two ways out.
func hashReadError(file string, err error) error {
	return fmt.Errorf("reading %s for the default version label: %w; add it to exclude_paths or pass --dag-version", file, err)
}

// dbtParseOutputs are the files `dbt parse` rewrites under target/ on every
// run; everything else under target/ ships as the author left it.
var dbtParseOutputs = []string{
	"manifest.json", "semantic_manifest.json", "partial_parse.msgpack",
	"perf_info.json", "graph.gpickle", "graph_summary.json",
}

// contentWalker hashes the files of one project directory.
type contentWalker struct {
	dir      string
	cfg      *domain.LeoflowConfig
	output   string // absolute path of the compiled dag.json, or ""
	manifest string // slash path of dbt.manifest relative to dir, or ""
	ignore   dockerignore
	h        hash.Hash
}

// newContentWalker reads the project's .dockerignore (hashing it, with any
// block a killed build left behind stripped) and resolves the paths the walk
// treats specially.
func newContentWalker(dir string, cfg *domain.LeoflowConfig, output string, h hash.Hash) (*contentWalker, error) {
	w := &contentWalker{dir: dir, cfg: cfg, h: h}
	if output != "" {
		abs, err := filepath.Abs(output)
		if err != nil {
			return nil, fmt.Errorf("resolving %s: %w", output, err)
		}
		w.output = abs
	}
	if cfg.Dbt != nil && cfg.Dbt.Project != "" && cfg.Dbt.Manifest != "" {
		w.manifest = filepath.ToSlash(filepath.Join(filepath.Clean(cfg.Dbt.Project), cfg.Dbt.Manifest))
	}
	ignorePath := filepath.Join(dir, dockerignoreName)
	data, err := os.ReadFile(ignorePath) //nolint:gosec // G304: the project's own .dockerignore.
	switch {
	case err == nil:
		data, _ = stripLeoflowBlock(data)
		hashField(h, dockerignoreName, data)
		w.ignore = parseDockerignore(data)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, hashReadError(ignorePath, err)
	}
	return w, nil
}

// roots lists the project-relative paths whose content shapes the image,
// mirroring what generatedDockerfile COPYs (copiedRoots), sorted. dbt.manifest
// is always its own root, because the dbt compile reads it even when its
// directory is otherwise walked.
func (w *contentWalker) roots(dockerfile string) []string {
	var roots []string
	if projectDockerfile(w.dir, w.cfg, dockerfile) != "" {
		roots = []string{"."}
	} else {
		roots = append([]string{filepath.Clean(w.cfg.DagSource)}, copiedRoots(w.cfg)...)
		slices.Sort(roots)
		roots = slices.Compact(roots)
		if slices.Contains(roots, ".") {
			roots = []string{"."}
		}
	}
	if w.manifest != "" {
		roots = append(roots, filepath.FromSlash(w.manifest))
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
func (w *contentWalker) hashRoot(root string) error {
	start := filepath.Join(w.dir, root)
	if _, err := os.Lstat(start); errors.Is(err, fs.ErrNotExist) {
		hashField(w.h, "absent", []byte(filepath.ToSlash(root)))
		return nil
	}
	return filepath.WalkDir(start, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return hashReadError(file, err)
		}
		rel, rerr := filepath.Rel(w.dir, file)
		if rerr != nil {
			return rerr
		}
		slash := filepath.ToSlash(rel)
		if w.skip(rel, file) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// A directory the .dockerignore excludes is still walked when a later
		// `!` line may bring part of it back, as the build does.
		if rel != "." && slash != w.manifest && w.ignore.excluded(slash) {
			if d.IsDir() && !w.ignore.hasNegation {
				return filepath.SkipDir
			}
			return nil
		}
		return w.hashEntry(file, slash, d)
	})
}

// hashEntry hashes one walked entry: a symlink as its target, a regular file
// streamed through the hash; directories and special files add nothing.
func (w *contentWalker) hashEntry(file, slash string, d fs.DirEntry) error {
	switch {
	case d.Type()&fs.ModeSymlink != 0:
		target, err := os.Readlink(file)
		if err != nil {
			return hashReadError(file, err)
		}
		hashField(w.h, "symlink:"+slash, []byte(filepath.ToSlash(target)))
		return nil
	case !d.Type().IsRegular():
		return nil
	}
	f, err := os.Open(file) //nolint:gosec // G304: a file inside the operator's own project directory.
	if err != nil {
		return hashReadError(file, err)
	}
	defer f.Close() //nolint:errcheck // read-only file
	st, err := f.Stat()
	if err != nil {
		return hashReadError(file, err)
	}
	hashHeader(w.h, slash, st.Size())
	n, err := io.Copy(w.h, f)
	if err != nil {
		return hashReadError(file, err)
	}
	if n != st.Size() {
		return hashReadError(file, fmt.Errorf("file changed while it was read"))
	}
	return nil
}

// skip reports whether a project-relative path stays out of the hash.
func (w *contentWalker) skip(rel, abs string) bool {
	if rel == "." {
		return false
	}
	slash := filepath.ToSlash(rel)
	if slash == w.manifest {
		return false
	}
	base := path.Base(slash)
	if base == ".git" || strings.HasPrefix(base, ".leoflow") || slash == dockerignoreName {
		return true
	}
	if slash == "dag.json" || (w.output != "" && filepath.Clean(abs) == w.output) {
		return true
	}
	if w.dbtArtifact(slash) {
		return true
	}
	return matchesExclude(w.cfg.ExcludePaths, slash, base)
}

// dbtArtifact reports whether slash is a file `dbt parse` rewrites inside a
// dbt project directory: logs/, .user.yml, and dbtParseOutputs under target/.
func (w *contentWalker) dbtArtifact(slash string) bool {
	dirs := dbtGroupProjectDirs(w.cfg)
	if w.cfg.Dbt != nil && w.cfg.Dbt.Project != "" {
		dirs = append(dirs, filepath.Clean(w.cfg.Dbt.Project))
	}
	for _, d := range dirs {
		prefix := filepath.ToSlash(d) + "/"
		if d == "." {
			prefix = ""
		}
		if slash == prefix+"logs" || slash == prefix+".user.yml" {
			return true
		}
		for _, f := range dbtParseOutputs {
			if slash == prefix+"target/"+f {
				return true
			}
		}
	}
	return false
}

// matchesExclude applies exclude_paths the simple way the build context sees
// most of them: a pattern matches the slash path or its base name, with a
// trailing slash ignored.
func matchesExclude(patterns []string, slash, base string) bool {
	for _, raw := range patterns {
		p := strings.TrimSuffix(strings.TrimSpace(raw), "/")
		if p == "" {
			continue
		}
		if ok, _ := path.Match(p, slash); ok { //nolint:errcheck // a malformed pattern matches nothing
			return true
		}
		if ok, _ := path.Match(p, base); ok { //nolint:errcheck // a malformed pattern matches nothing
			return true
		}
	}
	return false
}
