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

// baseRegistry reads the digest of the base image for the dependency key.
// It is a variable only so the tests of this package keep the CLI off the
// network (split_seam_test.go); nothing else assigns it.
var baseRegistry build.Resolver = build.Registry{}

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
// (the key covers them), every Python dependency pinned to one version (the
// dependency image is reused for as long as its key holds, and a range would
// keep the version it resolved first) and the digest of the base image. A
// reason is empty when nothing is worth printing: no push, no platform, or a
// project the build itself will refuse with its own message.
func splitFor(ctx context.Context, dir, dockerfile string, o compileOptions, cfg *domain.LeoflowConfig, image string) (split *build.Split, why string) {
	platforms := buildPlatforms(cfg)
	if !o.push || len(platforms) == 0 {
		return nil, ""
	}
	if _, err := os.Stat(filepath.Join(dir, dockerfile)); err == nil {
		return nil, "the project ships its own Dockerfile, which needs a real build"
	}
	if cfg.Dbt == nil && filepath.Clean(cfg.DagSource) != filepath.Base(cfg.DagSource) {
		return nil, fmt.Sprintf("dag_source %q is in a subdirectory, which the code layer does not place yet", cfg.DagSource)
	}
	deps, err := cfg.EffectiveDependencies()
	if err != nil {
		return nil, ""
	}
	if loose := unpinned(deps); len(loose) > 0 {
		return nil, fmt.Sprintf("%s %s not pinned to one version (name==version), so the dependency image cannot be reused safely",
			quoteAll(loose), plural(len(loose), "is", "are"))
	}
	base := resolveBaseImage(cfg)
	pinned, err := baseRegistry.Resolve(ctx, base)
	if err != nil {
		return nil, fmt.Sprintf("the digest of the base image %s could not be read (%v)", base, unwrapped(err))
	}
	content, err := dependencyDockerfile(cfg, pinned.Ref)
	if err != nil {
		return nil, ""
	}
	return &build.Split{DepsImage: depsImage(image, depsKey(content, platforms)), Dockerfile: content}, ""
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
// base image pinned by digest. The image configuration is the same, since
// COPY changes none of it, so the dependency image plus the code layer is the
// image the generated Dockerfile builds.
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
// the generated Dockerfile take from dir, walked as the version label walks
// it (contentWalker), with the same .dockerignore, exclude_paths and symlink
// rules. Two differences, both what the COPY does: the files `dbt parse`
// writes under target/ ship, and dbt.manifest is one more file of its project
// rather than a root of its own. A root that does not exist fails, as the
// COPY would. Every directory above a nested root is listed too, as the COPY
// creates it.
func codeLayerFiles(dir string, cfg *domain.LeoflowConfig, output string) ([]build.LayerFile, error) {
	w, err := newContentWalker(dir, cfg, output, false, sha256.New())
	if err != nil {
		return nil, err
	}
	w.layer = true
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
		if werr := w.hashRoot(root); werr != nil {
			return nil, werr
		}
	}
	return withParents(w.files), nil
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
