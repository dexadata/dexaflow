package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// dependencyRoot is where the dependency layer lands in the image: the
// prefix of the runtime base's Python, whose site-packages and bin the
// generated Dockerfile's pip install writes to.
const dependencyRoot = "/usr/local"

// scriptsDir is the directory `pip install --target` writes console scripts
// to, relative to the target.
const scriptsDir = "bin"

// recordScriptPrefix is the path of /usr/local/bin relative to
// site-packages, as pip writes it in a RECORD.
const recordScriptPrefix = "../../../bin/"

// Installer installs the resolved Python dependencies of one platform into
// a directory, as `pip install --target` lays them out (ADR 0078).
type Installer interface {
	// Describe names the installer and its version, "uv 0.11.32".
	Describe() string
	// Install installs the dependencies of platform into dir.
	Install(ctx context.Context, platform, dir string) error
}

// DependencyLayer builds the dependency image without a daemon (path 3,
// ADR 0078): for each platform, the Installer installs the wheels into a
// directory, the directory is packed as one deterministic layer under
// /usr/local, and the layer is appended to the runtime base through the
// registry API, with the image configuration of the generated Dockerfile.
type DependencyLayer struct {
	// Base is the runtime base, pinned by digest: an index or one image.
	Base Pinned
	// Python is the X.Y of the base's Python.
	Python string
	// Env are the KEY=VALUE variables the image sets on top of the base's.
	Env []string
	// User is the user the image runs as; empty keeps the base's.
	User string
	// Installer installs the dependencies of each platform.
	Installer Installer
	// Registry is the registry client that reads the base and pushes the
	// image, with its credentials (registry.auth_method).
	Registry Registry
}

// Push builds the dependency image for platforms and pushes it under image:
// an index when the base is one (its attestation manifests dropped, its
// other platforms left out), one image otherwise.
func (d DependencyLayer) Push(ctx context.Context, image string, platforms []string) error {
	dst, err := name.ParseReference(image)
	if err != nil {
		return fmt.Errorf("image %q is not a valid reference: %w", image, err)
	}
	src, err := name.ParseReference(d.Base.Ref)
	if err != nil {
		return fmt.Errorf("image %q is not a valid reference: %w", d.Base.Ref, err)
	}
	tmp, err := os.MkdirTemp("", "dexaflow-wheels-")
	if err != nil {
		return fmt.Errorf("making the directory of the dependency layer: %w", err)
	}
	b := depsBuild{d: d, tmp: tmp}
	defer b.cleanup()
	if !isIndex(d.Base.Manifest) {
		opts := d.Registry.remoteOptions(ctx)
		if len(platforms) != 1 {
			return fmt.Errorf("the base image %s is one image, so it serves one platform, not %s", d.Base.Ref, strings.Join(platforms, ","))
		}
		img, ierr := remote.Image(src, opts...)
		if ierr != nil {
			return fmt.Errorf("reading the base image %s: %w", d.Base.Ref, ierr)
		}
		out, ierr := b.image(ctx, img, platforms[0])
		if ierr != nil {
			return ierr
		}
		if werr := remote.Write(dst, out, opts...); werr != nil {
			return fmt.Errorf("pushing %s: %w", image, werr)
		}
		return nil
	}
	return b.index(ctx, src, dst, platforms)
}

// depsBuild is one push of a dependency image: the temporary directory the
// trees are installed in, and the packed layers to delete after the push.
type depsBuild struct {
	d       DependencyLayer
	tmp     string
	removes []func()
}

// cleanup deletes the installed trees and the packed layers.
func (b *depsBuild) cleanup() {
	for _, r := range b.removes {
		r()
	}
	_ = os.RemoveAll(b.tmp) //nolint:errcheck // best-effort cleanup of a temporary directory
}

// index builds the image of every platform the base index lists among
// platforms, in the order of the index, and pushes the new index to dst.
func (b *depsBuild) index(ctx context.Context, src, dst name.Reference, platforms []string) error {
	opts := b.d.Registry.remoteOptions(ctx)
	idx, err := remote.Index(src, opts...)
	if err != nil {
		return fmt.Errorf("reading the base image %s: %w", src, err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return fmt.Errorf("reading the index of %s: %w", src, err)
	}
	if missing := missingPlatforms(b.d.Base.Manifest, platforms); len(missing) > 0 {
		return fmt.Errorf("the base image %s has no image for %s", src, strings.Join(missing, ","))
	}
	out := mutate.IndexMediaType(empty.Index, im.MediaType)
	for _, desc := range im.Manifests {
		want := wanted(desc, platforms)
		if want == "" {
			continue
		}
		img, ierr := idx.Image(desc.Digest)
		if ierr != nil {
			return fmt.Errorf("reading %s of %s: %w", desc.Digest, src, ierr)
		}
		withDeps, ierr := b.image(ctx, img, want)
		if ierr != nil {
			return ierr
		}
		out = mutate.AppendManifests(out, mutate.IndexAddendum{Add: withDeps, Descriptor: v1.Descriptor{Platform: desc.Platform}})
	}
	if err := remote.WriteIndex(dst, out, opts...); err != nil {
		return fmt.Errorf("pushing %s: %w", dst, err)
	}
	return nil
}

// wanted is the platform of platforms that the index entry desc serves, or
// "" when it serves none (an attestation, a platform not asked for).
func wanted(desc v1.Descriptor, platforms []string) string {
	if isAttestation(desc) || desc.MediaType.IsIndex() {
		return ""
	}
	for _, p := range platforms {
		if samePlatform(desc.Platform.String(), p) {
			return p
		}
	}
	return ""
}

// image installs the dependencies of platform and returns base with them
// appended as a layer and the image configuration set.
func (b *depsBuild) image(ctx context.Context, base v1.Image, platform string) (v1.Image, error) {
	dir := filepath.Join(b.tmp, strings.ReplaceAll(platform, "/", "-"))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("making the directory of the dependency layer: %w", err)
	}
	if err := b.d.Installer.Install(ctx, platform, dir); err != nil {
		return nil, fmt.Errorf("installing the dependencies for %s: %w", platform, err)
	}
	files, err := dependencyFiles(dir, b.d.Python)
	if err != nil {
		return nil, err
	}
	img := base
	if len(files) > 0 {
		mt, merr := base.MediaType()
		if merr != nil {
			return nil, fmt.Errorf("reading the media type of the base image: %w", merr)
		}
		layer, remove, perr := packLayer(kindDependency, dependencyRoot, files, layerTypeFor(mt))
		if perr != nil {
			return nil, perr
		}
		b.removes = append(b.removes, remove)
		img, err = mutate.Append(base, mutate.Addendum{Layer: layer, History: v1.History{
			CreatedBy: "dexaflow dependency layer: Python dependencies installed from wheels by " + b.d.Installer.Describe() + " (ADR 0078)",
			Created:   v1.Time{Time: layerTime},
		}})
		if err != nil {
			return nil, fmt.Errorf("appending the dependency layer: %w", err)
		}
	}
	return b.d.configure(img)
}

// configure sets the environment and the user of the dependency image.
func (d DependencyLayer) configure(img v1.Image) (v1.Image, error) {
	cf, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("reading the base image config: %w", err)
	}
	cfg := *cf.Config.DeepCopy()
	cfg.Env = setEnv(cfg.Env, d.Env)
	if d.User != "" {
		cfg.User = d.User
	}
	out, err := mutate.Config(img, cfg)
	if err != nil {
		return nil, fmt.Errorf("setting the dependency image config: %w", err)
	}
	return out, nil
}

// setEnv sets each KEY=VALUE of set in env, replacing a variable of the
// same name in place, as ENV does.
func setEnv(env, set []string) []string {
	out := append([]string(nil), env...)
	for _, kv := range set {
		key, _, _ := strings.Cut(kv, "=")
		replaced := false
		for i, e := range out {
			if k, _, _ := strings.Cut(e, "="); k == key {
				out[i], replaced = kv, true
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	return out
}

// layerTypeFor is the layer media type that goes with an image of
// mediaType: a Docker layer in a Docker v2 image, an OCI one otherwise.
func layerTypeFor(mediaType types.MediaType) types.MediaType {
	if mediaType == types.DockerManifestSchema2 {
		return types.DockerLayer
	}
	return types.OCILayer
}

// dependencyFiles lists the layer files of a tree `pip install --target`
// wrote into dir, relative to /usr/local: the scripts of bin/ in bin, the
// rest in lib/pythonX.Y/site-packages, where pip installs as root in the
// image. A script whose shebang names a Python is made to name the image's
// (/usr/local/bin/pythonX.Y), and the RECORD lines of the scripts are made
// to point at where they land, with their new hash and size, so `pip show
// -f` and `pip uninstall` work in the image. Neither site-packages nor bin
// is an entry: the base has them. The installer's lock file is left out.
func dependencyFiles(dir, python string) ([]LayerFile, error) {
	scripts, err := rewriteScripts(filepath.Join(dir, scriptsDir), python)
	if err != nil {
		return nil, err
	}
	if rerr := rewriteRecords(dir, scripts); rerr != nil {
		return nil, rerr
	}
	sp := "lib/python" + python + "/site-packages/"
	var files []LayerFile
	err = filepath.WalkDir(dir, func(file string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(dir, file)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == scriptsDir || rel == ".lock" {
			return nil
		}
		dst := sp + rel
		script := false
		if s, ok := strings.CutPrefix(rel, scriptsDir+"/"); ok {
			dst, script = scriptsDir+"/"+s, true
		}
		f, ferr := layerFileOf(file, dst, d)
		if ferr != nil || f == nil {
			return ferr
		}
		// pip makes every script executable; the host's mode bits (a
		// filesystem without them, a umask) decide nothing.
		f.Exec = f.Exec || (script && f.Source != "")
		files = append(files, *f)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing the installed dependencies: %w", err)
	}
	return files, nil
}

// layerFileOf is the layer file of one walked entry at dst, or nil for a
// special file, which an install never writes.
func layerFileOf(file, dst string, d fs.DirEntry) (*LayerFile, error) {
	switch {
	case d.IsDir():
		return &LayerFile{Path: dst, Dir: true}, nil
	case d.Type()&fs.ModeSymlink != 0:
		target, err := os.Readlink(file)
		if err != nil {
			return nil, err
		}
		return &LayerFile{Path: dst, Link: target}, nil
	case d.Type().IsRegular():
		info, err := d.Info()
		if err != nil {
			return nil, err
		}
		return &LayerFile{Path: dst, Source: file, Exec: info.Mode()&0o111 != 0}, nil
	}
	return nil, nil //nolint:nilnil // a special file has no entry and is no error
}

// scriptRecord is the RECORD hash and size of a script after its rewrite.
type scriptRecord struct {
	hash string
	size int
}

// rewriteScripts makes every script of bin whose shebang names a Python
// name the image's, and returns the hash and size of every script by name.
func rewriteScripts(bin, python string) (map[string]scriptRecord, error) {
	out := map[string]scriptRecord{}
	entries, err := os.ReadDir(bin)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the installed scripts: %w", err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		file := filepath.Join(bin, e.Name())
		body, rerr := os.ReadFile(file) //nolint:gosec // G304: a script the installer wrote to our directory.
		if rerr != nil {
			return nil, fmt.Errorf("reading the script %s: %w", e.Name(), rerr)
		}
		if fixed, ok := withShebang(body, "#!/usr/local/bin/python"+python); ok {
			info, ierr := e.Info()
			if ierr != nil {
				return nil, fmt.Errorf("reading the script %s: %w", e.Name(), ierr)
			}
			if werr := os.WriteFile(file, fixed, info.Mode().Perm()); werr != nil { //nolint:gosec // G703: a script the installer wrote to our directory.
				return nil, fmt.Errorf("rewriting the script %s: %w", e.Name(), werr)
			}
			body = fixed
		}
		sum := sha256.Sum256(body)
		out[e.Name()] = scriptRecord{hash: "sha256=" + base64.RawURLEncoding.EncodeToString(sum[:]), size: len(body)}
	}
	return out, nil
}

// withShebang returns body with its first line replaced by shebang when
// that line is a shebang naming a Python interpreter. uv's /bin/sh
// trampoline, which it writes when the interpreter path is too long or has
// spaces (`#!/bin/sh`, `”'exec' <python> "$0" "$@"`, `' ”'`), is replaced as
// a whole.
func withShebang(body []byte, shebang string) ([]byte, bool) {
	if rest, ok := trampolineBody(body); ok {
		return append([]byte(shebang+"\n"), rest...), true
	}
	first, rest, _ := bytes.Cut(body, []byte("\n"))
	if !bytes.HasPrefix(first, []byte("#!")) || !bytes.Contains(first, []byte("python")) {
		return nil, false
	}
	return append([]byte(shebang+"\n"), rest...), true
}

// trampolineBody returns what follows uv's three-line /bin/sh trampoline,
// when body starts with one.
func trampolineBody(body []byte) ([]byte, bool) {
	lines := bytes.SplitN(body, []byte("\n"), 4)
	if len(lines) < 4 || string(lines[0]) != "#!/bin/sh" || !bytes.HasPrefix(lines[1], []byte("'''exec' ")) || string(lines[2]) != "' '''" {
		return nil, false
	}
	return lines[3], true
}

// rewriteRecords rewrites the lines of every RECORD of dir that list a
// script of bin/: the path relative to site-packages in the image, and the
// hash and size of the rewritten script.
func rewriteRecords(dir string, scripts map[string]scriptRecord) error {
	records, err := filepath.Glob(filepath.Join(dir, "*.dist-info", "RECORD"))
	if err != nil {
		return fmt.Errorf("finding the RECORD files: %w", err)
	}
	for _, file := range records {
		if err := rewriteRecord(file, scripts); err != nil {
			return err
		}
	}
	return nil
}

// rewriteRecord rewrites the script lines of one RECORD file.
func rewriteRecord(file string, scripts map[string]scriptRecord) error {
	data, err := os.ReadFile(file) //nolint:gosec // G304: a RECORD the installer wrote to our directory.
	if err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}
	changed := false
	for _, row := range rows {
		s, ok := strings.CutPrefix(path.Clean(row[0]), scriptsDir+"/")
		if !ok {
			continue
		}
		row[0], changed = recordScriptPrefix+s, true
		if rec, known := scripts[s]; known && len(row) >= 3 {
			row[1], row[2] = rec.hash, strconv.Itoa(rec.size)
		}
	}
	if !changed {
		return nil
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.WriteAll(rows); err != nil {
		return fmt.Errorf("writing %s: %w", file, err)
	}
	if err := os.WriteFile(file, buf.Bytes(), 0o644); err != nil { //nolint:gosec // G306: a RECORD is read by every user, as pip writes it.
		return fmt.Errorf("writing %s: %w", file, err)
	}
	return nil
}
