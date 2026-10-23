package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// baseReader reads the base image: its digest for the dependency key, and
// what the dependency layer needs to know about each platform image.
type baseReader interface {
	build.Resolver
	Base(ctx context.Context, base build.Pinned, platform string) (build.BaseImage, error)
}

// baseReaderFor reads the base image of cfg's project, for the dependency
// key and the dependency layer, with the project's registry credentials
// (registryFor, registry.auth_method). The tests of this package keep the
// CLI off public registries (cliDeps.baseReader) while still reading
// through the project's client.
func baseReaderFor(ctx context.Context, cfg *domain.LeoflowConfig) baseReader {
	return depsFrom(ctx).baseReader(registryFor(ctx, cfg))
}

// depsKeyFormat versions the layout of the dependency image and of the code
// layer. Bumping it gives every project a new dependency key.
const depsKeyFormat = 1

// depsTagPrefix starts the tag of a dependency image in the DAG's repository.
const depsTagPrefix = "deps-"

// depsKeyLen is how many hex characters of the key the tag carries: 128
// bits.
const depsKeyLen = 32

// exactPin matches a dependency pinned to one version, `name==1.2.3` or
// `name[extra]==1.2.3`, with no wildcard, marker, URL or path.
var exactPin = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(\[[A-Za-z0-9._,-]+\])?==[A-Za-z0-9][A-Za-z0-9.+!_-]*$`)

// splitFor decides whether a build of image divides into a dependency image
// and a code layer (ADR 0077) and returns that split, or nil and why not. The
// split needs a push (the code layer goes through the registry API), the
// generated Dockerfile (a project Dockerfile can do anything), the platforms
// (the key covers them) and the digest of the base image. When the
// dependency image can be built from wheels (planWheels, ADR 0078), its key
// covers the resolved set, so any Python dependency takes the split;
// otherwise the daemon builds it, and every Python dependency must be pinned
// to one version (the dependency image is reused for as long as its key
// holds, and a range would keep the version it resolved first). A reason is
// empty when nothing is worth printing: no push, no platform, or a project
// the build itself will refuse with its own message.
func splitFor(ctx context.Context, dir, dockerfile string, o compileOptions, cfg *domain.LeoflowConfig, image string) (split *build.Split, why string) {
	platforms := buildPlatforms(cfg)
	if !o.push || len(platforms) == 0 {
		return nil, ""
	}
	if why := splitBlocker(dir, dockerfile, cfg); why != "" {
		return nil, why
	}
	deps, err := cfg.EffectiveDependencies()
	if err != nil || cfg.ValidateDockerfileValues() != nil {
		return nil, ""
	}
	loose := unpinned(deps)
	uv, noWheels := findUV(ctx)
	uv, noWheels = withIndex(uv, noWheels, o, cfg)
	if len(loose) > 0 && uv == nil {
		return nil, looseReason(loose, noWheels)
	}
	base := resolveBaseImage(cfg)
	reader := baseReaderFor(ctx, cfg)
	pinned, err := reader.Resolve(ctx, base)
	if err != nil {
		return nil, fmt.Sprintf("the digest of the base image %s could not be read (%v)", base, unwrapped(err))
	}
	content, err := dependencyDockerfile(cfg, pinned.Ref)
	if err != nil {
		return nil, ""
	}
	if uv != nil {
		var plan *wheelPlan
		if plan, noWheels = planWheels(ctx, uv, cfg, deps, reader, pinned, platforms); plan != nil {
			wheels := plan.layer()
			wheels.Registry = registryFor(ctx, cfg)
			split := &build.Split{DepsImage: depsImage(image, plan.key()), Dockerfile: content, Wheels: wheels, Note: plan.note()}
			if len(loose) == 0 {
				split.DaemonDepsImage = depsImage(image, depsKey(depsKeyContent(content, cfg), platforms))
			}
			return split, ""
		}
	}
	if len(loose) > 0 {
		return nil, looseReason(loose, noWheels)
	}
	return &build.Split{DepsImage: depsImage(image, depsKey(depsKeyContent(content, cfg), platforms)), Dockerfile: content, NoWheels: noWheels}, ""
}

// splitBlocker says why the project cannot split whatever its dependencies:
// its own Dockerfile, a dag_source the code layer does not place, a pattern
// in include_paths; "" when nothing blocks.
func splitBlocker(dir, dockerfile string, cfg *domain.LeoflowConfig) string {
	if _, err := os.Stat(filepath.Join(dir, dockerfile)); err == nil {
		return "the project ships its own Dockerfile, which needs a real build"
	}
	if cfg.Dbt == nil && filepath.Clean(cfg.DagSource) != filepath.Base(cfg.DagSource) {
		return fmt.Sprintf("dag_source %q is in a subdirectory, which the code layer does not place yet", cfg.DagSource)
	}
	for _, p := range cfg.IncludePaths {
		if strings.ContainsAny(p, "*?[") {
			return fmt.Sprintf("include_paths entry %q is a pattern, which the code layer does not expand", p)
		}
	}
	return ""
}

// looseReason says why a project with dependencies not pinned to one
// version keeps the whole build: the daemon's dependency image would keep
// the versions it resolved first, and noWheels says why the dependency
// layer, whose key covers the resolution, does not apply.
func looseReason(loose []string, noWheels string) string {
	return fmt.Sprintf("%s %s not pinned to one version (name==version), so the dependency image cannot be reused safely "+
		"without the dependency layer, which does not apply: %s", quoteAll(loose), plural(len(loose), "is", "are"), noWheels)
}

// unwrapped returns the cause of err without the context Resolve adds, or
// err when it wraps nothing.
func unwrapped(err error) error {
	if cause := errors.Unwrap(err); cause != nil {
		return cause
	}
	return err
}

// requirementName matches the distribution name a requirement starts with.
var requirementName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*`)

// unpinned returns the dependencies not pinned to one version. One the list
// pins elsewhere is pinned: pip installs `pkg` next to `pkg==1.2` as 1.2,
// which is how a connector's provider package is pinned.
func unpinned(deps []string) []string {
	pinned := map[string]bool{}
	for _, d := range deps {
		if d = strings.TrimSpace(d); exactPin.MatchString(d) {
			pinned[normalizedName(d)] = true
		}
	}
	var out []string
	for _, d := range deps {
		if !pinned[normalizedName(strings.TrimSpace(d))] {
			out = append(out, d)
		}
	}
	return out
}

// normalizedName is the distribution name of a requirement as PyPI compares
// names: lower case, with runs of -, _ and . as one -; "" when the
// requirement starts with no name (a URL or a path).
func normalizedName(req string) string {
	n := strings.ToLower(requirementName.FindString(req))
	return strings.Join(strings.FieldsFunc(n, func(r rune) bool { return r == '-' || r == '_' || r == '.' }), "-")
}

// depsKey is the dependency key (ADR 0073 section 4, ADR 0077): the SHA-256
// of the key format, the dependency image's Dockerfile and the platforms,
// sorted. The Dockerfile carries the base image by digest, the system
// packages, the pinned dependencies with the installer command, and the
// image configuration, so every input of the dependency image is in it.
func depsKey(dockerfile string, platforms []string) string {
	sorted := slices.Clone(platforms)
	for i := range sorted {
		sorted[i] = strings.ToLower(sorted[i])
	}
	slices.Sort(sorted)
	doc, _ := json.Marshal(struct { //nolint:errcheck // strings and ints cannot fail to marshal
		Format     int      `json:"format"`
		Dockerfile string   `json:"dockerfile"`
		Platforms  []string `json:"platforms"`
	}{depsKeyFormat, dockerfile, sorted})
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:])
}

// depsImage is the dependency image of key in the repository of image.
func depsImage(image, key string) string {
	repo := image
	if i := strings.Index(repo, "@"); i >= 0 {
		repo = repo[:i]
	}
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	return repo + ":" + depsTagPrefix + key[:depsKeyLen]
}

// dependencyDockerfile renders the Dockerfile of the dependency image: the
// generated Dockerfile (generatedDockerfile) without its COPY lines, FROM the
// base image pinned by digest. COPY changes none of the image configuration,
// so the dependency image plus the code layer has the configuration of the
// image the generated Dockerfile builds; codeLayerFiles says how the files
// differ.
func dependencyDockerfile(cfg *domain.LeoflowConfig, from string) (string, error) {
	if err := cfg.ValidateDockerfileValues(); err != nil {
		return "", err
	}
	deps, err := cfg.EffectiveDependencies()
	if err != nil {
		return "", err
	}
	from, err = fromOperand("base_image", from)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", from)
	rootForInstall := len(cfg.SystemPackages) > 0 || len(deps) > 0
	if rootForInstall {
		b.WriteString("USER root\n")
	}
	if err := writeInstallLayers(&b, cfg, deps); err != nil {
		return "", err
	}
	if cfg.Dbt == nil {
		b.WriteString("ENV PYTHONPATH=" + build.Workdir + "\n")
	}
	if rootForInstall {
		b.WriteString("USER 65532:65532\n")
	}
	return b.String(), nil
}

// codeLayerFiles lists the entries of the code layer: what the COPY lines of
// the generated Dockerfile take from dir. It walks the roots of those lines as
// the version label walks them (contentWalker), with the same symlink rules,
// and filters with the .dockerignore the build reads: the project's own
// lines, then exclude_paths and the dbt artifacts in the forms
// ensureDockerignore writes, matched with Docker's rules (buildIgnore). So the
// files `dbt parse` writes under target/ ship, as the COPY ships them, and
// dbt.manifest is one more file of its project. What the layer leaves out on
// top of the build: .git, .leoflow* files, .dockerignore and the compiled
// dag.json, which no task reads. A root that does not exist or that the
// .dockerignore excludes fails, as the COPY would. Every directory above a
// nested root is listed too, as the COPY creates it.
func codeLayerFiles(dir string, cfg *domain.LeoflowConfig, output string) ([]build.LayerFile, error) {
	w, err := newContentWalker(dir, cfg, output, false, sha256.New())
	if err != nil {
		return nil, err
	}
	w.layer = true
	if w.ignore, err = buildIgnore(dir, cfg); err != nil {
		return nil, err
	}
	// The roots of generatedDockerfile: a dbt project copies its project
	// directory alone; a dag.py its file, its dbt groups and include_paths.
	names := append([]string{filepath.Base(cfg.DagSource)}, copiedRoots(cfg)...)
	if cfg.Dbt != nil {
		names = []string{filepath.Clean(cfg.Dbt.Project)}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if slices.Contains(names, ".") {
		names = []string{"."}
	}
	for _, n := range names {
		root := hashRootSpec{name: n, inside: true}
		if _, serr := os.Stat(filepath.Join(dir, n)); serr != nil {
			return nil, fmt.Errorf("%s: the build copies it into the image: %w", filepath.Join(dir, n), serr)
		}
		if n != "." && w.ignore.excluded(filepath.ToSlash(n)) {
			return nil, fmt.Errorf("%s is excluded by .dockerignore or exclude_paths, so the build cannot copy it into the image", n)
		}
		if werr := w.hashRoot(root); werr != nil {
			return nil, werr
		}
	}
	return withParents(w.files), nil
}

// buildFiles lists what builder b reads from dir besides the Dockerfile: the
// code layer of a split build, or the build context of a whole build the
// remote builder runs, which leaves this machine and so is listed here, with
// the .dockerignore applied, rather than sent as a directory.
func buildFiles(dir string, cfg *domain.LeoflowConfig, output, dockerfile string, b build.Builder) (code, ctxFiles []build.LayerFile, err error) {
	if _, split := b.(build.CodeLayer); split {
		code, err = codeLayerFiles(dir, cfg, output)
		return code, nil, err
	}
	if b.Path() == build.PathRemote {
		ctxFiles, err = contextFiles(dir, cfg, dockerfile)
		return nil, ctxFiles, err
	}
	return nil, nil, nil
}

// contextFiles lists the build context of dir as a container daemon reads
// it for dockerfile: every entry the ignore file the build reads keeps
// (contextIgnore), with Docker's rules, descending into an excluded
// directory only when a `!` line may bring something under it back.
// Symlinks stay links, special files are left out, and so is the generated
// Dockerfile, which the remote builder adds under its own name.
func contextFiles(dir string, cfg *domain.LeoflowConfig, dockerfile string) ([]build.LayerFile, error) {
	ignore, err := contextIgnore(dir, cfg, dockerfile)
	if err != nil {
		return nil, err
	}
	files := []build.LayerFile{}
	werr := filepath.WalkDir(dir, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("listing the build context: %w", err)
		}
		rel, rerr := filepath.Rel(dir, file)
		if rerr != nil || rel == "." {
			return rerr //nolint:wrapcheck // Rel of a path WalkDir found under dir
		}
		slash := filepath.ToSlash(rel)
		if slash == generatedDockerfileName || ignore.excluded(slash) {
			if d.IsDir() && !ignore.mayReinclude(slash) {
				return filepath.SkipDir
			}
			return nil
		}
		return contextEntry(&files, file, slash, d)
	})
	if werr != nil {
		return nil, werr
	}
	return withParents(files), nil
}

// contextEntry adds one kept entry of the build context to files.
func contextEntry(files *[]build.LayerFile, file, slash string, d fs.DirEntry) error {
	switch {
	case d.IsDir():
		*files = append(*files, build.LayerFile{Path: slash, Dir: true})
	case d.Type()&fs.ModeSymlink != 0:
		target, err := os.Readlink(file)
		if err != nil {
			return fmt.Errorf("reading the link %s: %w", file, err)
		}
		*files = append(*files, build.LayerFile{Path: slash, Link: target})
	case d.Type().IsRegular():
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("reading %s: %w", file, err)
		}
		*files = append(*files, build.LayerFile{Path: slash, Source: file, Exec: info.Mode()&0o111 != 0})
	}
	return nil
}

// contextIgnore is the ignore file a BuildKit build of dockerfile reads:
// <dockerfile>.dockerignore when it exists next to the Dockerfile, which
// BuildKit prefers to .dockerignore, merged with exclude_paths and the dbt
// artifacts as ensureDockerignore merges .dockerignore; buildIgnore
// otherwise.
func contextIgnore(dir string, cfg *domain.LeoflowConfig, dockerfile string) (dockerignore, error) {
	data, err := os.ReadFile(dockerfile + dockerignoreName) //nolint:gosec // G304: the ignore file next to the build's Dockerfile.
	if errors.Is(err, fs.ErrNotExist) || dockerfile == "" {
		return buildIgnore(dir, cfg)
	}
	if err != nil {
		return dockerignore{}, fmt.Errorf("reading %s: %w", dockerfile+dockerignoreName, err)
	}
	merged, _ := mergeDockerignore(data, slices.Concat(cfg.ExcludePaths, dbtBuildArtifacts(cfg)))
	return parseDockerignore(merged), nil
}

// buildIgnore parses the .dockerignore the build reads: the project's own
// file, without a block an interrupted build left, merged with exclude_paths
// and dbtBuildArtifacts as ensureDockerignore merges them.
func buildIgnore(dir string, cfg *domain.LeoflowConfig) (dockerignore, error) {
	file := filepath.Join(dir, dockerignoreName)
	data, err := os.ReadFile(file) //nolint:gosec // G304: the project's own .dockerignore.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return dockerignore{}, fmt.Errorf("reading %s: %w", file, err)
	}
	data, _ = stripLeoflowBlock(data)
	merged, _ := mergeDockerignore(data, slices.Concat(cfg.ExcludePaths, dbtBuildArtifacts(cfg)))
	return parseDockerignore(merged), nil
}

// collect records one walked entry of the code layer: a directory, a
// symlink as its target, a regular file with its execute bit. Special files
// are left out, as they are from the label.
func (w *contentWalker) collect(file, slash string, d fs.DirEntry) error {
	switch {
	case d.IsDir():
		if slash != "." {
			w.files = append(w.files, build.LayerFile{Path: slash, Dir: true})
		}
	case d.Type()&fs.ModeSymlink != 0:
		target, err := os.Readlink(file)
		if err != nil {
			return hashReadError(file, err)
		}
		w.files = append(w.files, build.LayerFile{Path: slash, Link: target})
	case d.Type().IsRegular():
		info, err := d.Info()
		if err != nil {
			return hashReadError(file, err)
		}
		w.files = append(w.files, build.LayerFile{Path: slash, Source: file, Exec: info.Mode()&0o111 != 0})
	}
	return nil
}

// withParents drops repeated paths (roots can overlap) and adds a directory
// entry for every parent of an entry that has none.
func withParents(files []build.LayerFile) []build.LayerFile {
	seen := map[string]bool{}
	out := make([]build.LayerFile, 0, len(files))
	add := func(f build.LayerFile) {
		if !seen[f.Path] {
			seen[f.Path] = true
			out = append(out, f)
		}
	}
	for _, f := range files {
		for p := path.Dir(f.Path); p != "."; p = path.Dir(p) {
			add(build.LayerFile{Path: p, Dir: true})
		}
		add(f)
	}
	return out
}
