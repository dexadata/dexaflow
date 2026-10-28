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

	"github.com/dexadata/dexaflow/internal/build"
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
var gitRepoOverrides = []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=",
	"GIT_COMMON_DIR=", "GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES="}

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
	return defaultDAGVersionOn(ctx, dir, cfg, dockerfile, output, "")
}

// defaultDAGVersionOn is defaultDAGVersion for a source mode deploy on
// runtimeImage (contentHashOn); "" is a build, and gives the same label.
func defaultDAGVersionOn(ctx context.Context, dir string, cfg *domain.LeoflowConfig, dockerfile, output, runtimeImage string) (string, error) {
	sum, err := contentHashOn(dir, cfg, dockerfile, output, runtimeImage)
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
//   - with a project Dockerfile, that Dockerfile (always, even when the
//     .dockerignore lists it) and the .dockerignore.
//
// Files are hashed by their slash-separated path relative to the project and
// their bytes, in sorted order. A root the build names (dag_source,
// include_paths, a dbt directory, the Dockerfile, dbt.manifest) is followed
// through symlinks and hashed as its target's content, since the build copies
// what it points at; it must resolve inside the project, except dbt.manifest,
// which only the CLI reads, and the Dockerfile, which `docker build -f` reads
// from anywhere. A symlink inside a walked directory is hashed as
// its target string and never followed, since COPY keeps links. Modification times and file modes
// never count, so a chmod alone keeps the label. It leaves out what the build
// cannot see or what every compile rewrites: .git, exclude_paths and
// .dockerignore matches, the compiled dag.json, the generated Dockerfile, and
// the files `dbt parse` rewrites (target/manifest.json and its siblings,
// logs/, .user.yml), except a dbt.manifest the project points at. The image
// digest is not an input: it does not exist before the build.
func contentHash(dir string, cfg *domain.LeoflowConfig, dockerfile, output string) (string, error) {
	return contentHashOn(dir, cfg, dockerfile, output, "")
}

// contentHashOn is contentHash for a source mode deploy on runtimeImage
// (#1772): the version's image is the runtime image, not one built from the
// project, so the label hashes it too, and a new runtime image is a new
// version. A build (runtimeImage "") hashes nothing more, so no build label
// moves.
func contentHashOn(dir string, cfg *domain.LeoflowConfig, dockerfile, output, runtimeImage string) (string, error) {
	h := sha256.New()
	cfgJSON, err := json.Marshal(labelConfig(cfg))
	if err != nil {
		return "", fmt.Errorf("encoding project config for the version label: %w", err)
	}
	hashField(h, "config", cfgJSON)
	hashField(h, "cli", []byte(version.Get().Version))
	if runtimeImage != "" {
		hashField(h, "source_mode_image", []byte(runtimeImage))
	}
	if _, err := walkLabelInputs(dir, cfg, dockerfile, output, h, nil); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// labelAuthMethod is the registry.auth_method the content label hashes for
// auto, docker_config and cloud: the value ApplyDefaults filled in before
// auto existed, so that no label moved when the default changed.
const labelAuthMethod = "docker_config"

// labelConfig is cfg as the content label hashes it: where the CLI reads the
// registry credential (registry.auth_method) is not an input of the image,
// so auto, docker_config and cloud hash as one constant, and the remote
// builder is left out. env and iam, which
// were hashed as written before any value was read, keep that, so their
// labels do not move either.
func labelConfig(cfg *domain.LeoflowConfig) *domain.LeoflowConfig {
	if cfg == nil {
		return nil
	}
	c := *cfg
	if cfg.Build != nil && cfg.Build.RemoteBuilder != nil {
		// Where the image is built (build.remote_builder) is not an input
		// of the image either.
		b := *cfg.Build
		b.RemoteBuilder = nil
		c.Build = &b
	}
	if cfg.Registry == nil || cfg.Registry.AuthMethod == "env" || cfg.Registry.AuthMethod == "iam" {
		return &c
	}
	reg := *cfg.Registry
	reg.AuthMethod = labelAuthMethod
	c.Registry = &reg
	return &c
}

// walkLabelInputs hashes into h every file contentHash reads from the project
// directory, calling seen (when not nil) with the path of each one, and
// returns the roots it walked, as project-relative names. It is the one
// definition of which files a DAG's image and version label come from, shared
// by the label and by deploy --changed-since.
func walkLabelInputs(dir string, cfg *domain.LeoflowConfig, dockerfile, output string, h hash.Hash, seen func(string)) ([]hashRootSpec, error) {
	own := projectDockerfile(dir, cfg, dockerfile)
	w, err := newContentWalker(dir, cfg, output, own != "", h)
	if err != nil {
		return nil, err
	}
	w.seen = seen
	roots := w.roots(own)
	for _, root := range roots {
		if werr := w.hashRoot(root); werr != nil {
			return nil, werr
		}
	}
	return roots, nil
}

// labelInputFiles lists the files of the project directory the default
// version label is computed from (walkLabelInputs), without reading them:
// what deploy --watch polls. dockerfile is the --dockerfile flag.
func labelInputFiles(dir string, cfg *domain.LeoflowConfig, dockerfile string) ([]string, error) {
	own := projectDockerfile(dir, cfg, dockerfile)
	w, err := newContentWalker(dir, cfg, "", own != "", sha256.New())
	if err != nil {
		return nil, err
	}
	var files []string
	w.list = true
	w.seen = func(file string) { files = append(files, file) }
	for _, root := range w.roots(own) {
		if werr := w.hashRoot(root); werr != nil {
			return nil, werr
		}
	}
	return files, nil
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
	dir     string // the project directory as given
	realDir string // dir with symlinks resolved, for the containment check
	cfg     *domain.LeoflowConfig
	output  string // absolute, symlink-resolved path of the compiled dag.json, or ""
	// pinned are the slash paths hashed as roots of their own (dbt.manifest
	// and the project Dockerfile): never skipped, never hashed twice.
	pinned   map[string]bool
	manifest string // slash path of dbt.manifest relative to dir, or ""
	ignore   dockerignore
	h        hash.Hash
	// layer collects the entries of the code layer instead of hashing them
	// (codeLayerFiles).
	layer bool
	files []build.LayerFile
	// seen, when set, receives the path of every entry the walk keeps.
	seen func(file string)
	// list walks without reading: seen receives the path of every file the
	// walk keeps, and nothing is hashed (labelInputFiles).
	list bool
}

// hashRootSpec is one root of the hash: a project-relative path.
type hashRootSpec struct {
	name   string
	pinned bool // exempt from every skip rule
	inside bool // must resolve inside the project directory
}

// newContentWalker reads the project's .dockerignore (hashing it when the
// project ships its own Dockerfile, with any block a killed build left behind
// stripped) and resolves the paths the walk treats specially.
func newContentWalker(dir string, cfg *domain.LeoflowConfig, output string, ownDockerfile bool, h hash.Hash) (*contentWalker, error) {
	w := &contentWalker{dir: dir, realDir: dir, cfg: cfg, h: h, pinned: map[string]bool{}}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		w.realDir = resolved
	}
	if output != "" {
		w.output = resolvedAbs(output)
	}
	if cfg.Dbt != nil && cfg.Dbt.Project != "" && cfg.Dbt.Manifest != "" {
		w.manifest = filepath.ToSlash(filepath.Join(filepath.Clean(cfg.Dbt.Project), cfg.Dbt.Manifest))
	}
	ignorePath := filepath.Join(dir, dockerignoreName)
	data, err := os.ReadFile(ignorePath) //nolint:gosec // G304: the project's own .dockerignore.
	switch {
	case err == nil:
		data, _ = stripLeoflowBlock(data)
		if ownDockerfile {
			// The generated Dockerfile COPYs named paths, so there the file
			// only filters them; with a project Dockerfile it decides what
			// `COPY .` takes.
			hashField(h, dockerignoreName, data)
		}
		w.ignore = parseDockerignore(data)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, hashReadError(ignorePath, err)
	}
	return w, nil
}

// resolvedAbs returns p absolute, with the symlinks of its directory resolved
// when it exists, so it compares equal to a path met while walking.
func resolvedAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, rerr := filepath.EvalSymlinks(filepath.Dir(abs)); rerr == nil {
		return filepath.Join(resolved, filepath.Base(abs))
	}
	return abs
}

// roots lists the roots of the hash, mirroring what generatedDockerfile COPYs
// (copiedRoots), sorted; with a project Dockerfile, the whole directory and
// the Dockerfile. dbt.manifest is always its own root, because the dbt compile
// reads it wherever it lives.
func (w *contentWalker) roots(ownDockerfile string) []hashRootSpec {
	var names []string
	if ownDockerfile != "" {
		names = []string{"."}
	} else {
		names = append([]string{filepath.Clean(w.cfg.DagSource)}, copiedRoots(w.cfg)...)
		slices.Sort(names)
		names = slices.Compact(names)
		if slices.Contains(names, ".") {
			names = []string{"."}
		}
	}
	roots := make([]hashRootSpec, 0, len(names)+2)
	for _, n := range names {
		roots = append(roots, hashRootSpec{name: n, inside: true})
	}
	if ownDockerfile != "" {
		name := filepath.Clean(ownDockerfile)
		w.pinned[filepath.ToSlash(name)] = true
		// Not held to the project: `docker build -f` takes a Dockerfile from
		// anywhere, and only build.dockerfile is confined (at build time).
		roots = append(roots, hashRootSpec{name: name, pinned: true})
	}
	if w.manifest != "" {
		w.pinned[w.manifest] = true
		roots = append(roots, hashRootSpec{name: filepath.FromSlash(w.manifest), pinned: true})
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

// hashRoot hashes one root, followed through symlinks, then walked in lexical
// order with every entry named by its path under the root's own name. A root
// that does not exist (or a link to nothing) is recorded as absent: the
// compile reports it with its own message, and the label still differs from
// the one with the file.
func (w *contentWalker) hashRoot(r hashRootSpec) error {
	resolved, err := w.resolveRoot(r)
	if err != nil || resolved == "" {
		return err
	}
	return filepath.WalkDir(resolved, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return hashReadError(file, err)
		}
		inner, rerr := filepath.Rel(resolved, file)
		if rerr != nil {
			return rerr
		}
		return w.visit(r, file, filepath.Join(r.name, inner), d)
	})
}

// resolveRoot follows a root through its symlinks and checks it stays in the
// project when it must. It returns "" (and records the root as absent) when
// there is nothing there.
func (w *contentWalker) resolveRoot(r hashRootSpec) (string, error) {
	start := filepath.Join(w.dir, r.name)
	resolved, err := filepath.EvalSymlinks(start)
	if errors.Is(err, fs.ErrNotExist) {
		hashField(w.h, "absent", []byte(filepath.ToSlash(r.name)))
		return "", nil
	}
	if err != nil {
		return "", hashReadError(start, err)
	}
	if r.inside {
		if rel, rerr := filepath.Rel(w.realDir, resolved); rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%s resolves to %s, outside the project directory %s; the build cannot COPY it", start, resolved, w.realDir)
		}
	}
	return resolved, nil
}

// visit decides one walked entry, named rel under its root's name.
func (w *contentWalker) visit(r hashRootSpec, file, rel string, d fs.DirEntry) error {
	slash := filepath.ToSlash(rel)
	if !r.pinned && (w.skip(rel, file) || w.pinned[slash]) {
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	// A directory the .dockerignore excludes is still walked when a `!` line
	// may bring part of it back, as the build does.
	if !r.pinned && rel != "." && w.ignore.excluded(slash) {
		if d.IsDir() && !w.ignore.mayReinclude(slash) {
			return filepath.SkipDir
		}
		return nil
	}
	if w.list {
		if !d.IsDir() {
			w.seen(file)
		}
		return nil
	}
	if w.seen != nil {
		w.seen(file)
	}
	if w.layer {
		return w.collect(file, slash, d)
	}
	return w.hashEntry(file, slash, d)
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
	base := path.Base(slash)
	if base == ".git" || strings.HasPrefix(base, ".leoflow") || slash == dockerignoreName {
		return true
	}
	if slash == "dag.json" || (w.output != "" && filepath.Clean(abs) == w.output) {
		return true
	}
	if w.layer {
		// The code layer reads exclude_paths and the dbt artifacts through
		// the merged .dockerignore, with Docker's rules (codeLayerFiles).
		return false
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
