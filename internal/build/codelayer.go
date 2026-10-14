package build

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Workdir is where the DAG code lands in the image, the working directory of
// the runtime base and the destination of every COPY of the generated
// Dockerfile.
const Workdir = "/home/leoflow"

// layerTime is the modification time of every entry of a code layer and of
// its history entry, so the same files always make the same layer.
var layerTime = time.Unix(0, 0).UTC()

// codeLayerHistory is the history entry of a code layer in the image config.
const codeLayerHistory = "dexaflow code layer: the DAG project files (ADR 0073)"

// LayerFile is one entry of a layer the CLI packs: the code layer, or the
// dependency layer of path 3.
type LayerFile struct {
	// Path is the slash-separated path under the root of the layer: Workdir
	// for the code layer.
	Path string
	// Source is the file on disk whose bytes the entry carries; empty for a
	// directory or a symlink.
	Source string
	// Dir marks a directory entry.
	Dir bool
	// Link is the target of a symlink entry, kept as written.
	Link string
	// Exec marks a regular file with an execute bit, which keeps it
	// executable in the image.
	Exec bool
}

// layerKind names a layer the CLI packs, in its temporary file and its
// errors.
type layerKind string

// The layers the CLI packs.
const (
	kindCode       layerKind = "code"
	kindDependency layerKind = "dependency"
	// kindContext is the build context a remote builder receives: an
	// archive, not an image layer, so its entries sit at the root and a
	// .wh. name is only a name.
	kindContext layerKind = "build context"
)

// codeLayer packs files as the code layer, under Workdir.
func codeLayer(files []LayerFile, mediaType types.MediaType) (layer v1.Layer, remove func(), err error) {
	return packLayer(kindCode, Workdir, files, mediaType)
}

// packLayer packs files as a deterministic tar layer of mediaType, each
// entry under root (an absolute directory of the image): entries sorted by
// path, owned by root, with a fixed modification time and modes normalized
// to 0755 for directories and executables, 0644 for other files (ADR 0073
// section 2). Only the files listed are emitted, never root or its parents,
// which the base image holds with their own modes. Each source file is read
// once, here, into a temporary tar file that remove deletes; the layer reads
// that file, so a large tree is streamed rather than held in memory.
func packLayer(kind layerKind, root string, files []LayerFile, mediaType types.MediaType) (layer v1.Layer, remove func(), err error) {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b LayerFile) int { return strings.Compare(a.Path, b.Path) })
	f, err := os.CreateTemp("", "dexaflow-"+string(kind)+"-*.tar")
	if err != nil {
		return nil, nil, fmt.Errorf("making the %s layer: %w", kind, err)
	}
	remove = func() { _ = os.Remove(f.Name()) } //nolint:errcheck // best-effort cleanup of a temporary file
	if werr := writeTar(f, kind, root, sorted); werr != nil {
		_ = f.Close() //nolint:errcheck // the write error is the one to report
		remove()
		return nil, nil, werr
	}
	if cerr := f.Close(); cerr != nil {
		remove()
		return nil, nil, fmt.Errorf("writing the %s layer: %w", kind, cerr)
	}
	tarPath := f.Name()
	layer, err = tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return os.Open(tarPath) //nolint:gosec // G304: the temporary file made above.
	}, tarball.WithMediaType(mediaType))
	if err != nil {
		remove()
		return nil, nil, fmt.Errorf("making the %s layer: %w", kind, err)
	}
	return layer, remove, nil
}

// writeTar writes the sorted entries to w as a tar stream, under root.
func writeTar(w io.Writer, kind layerKind, root string, sorted []LayerFile) error {
	tw := tar.NewWriter(w)
	for _, f := range sorted {
		if err := writeEntry(tw, kind, root, f); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing the %s layer: %w", kind, err)
	}
	return nil
}

// writeEntry writes one file of a layer, under root.
func writeEntry(tw *tar.Writer, kind layerKind, root string, f LayerFile) error {
	rel, err := layerPath(kind, root, f.Path)
	if err != nil {
		return err
	}
	h := &tar.Header{Name: path.Join(strings.Trim(root, "/"), rel), ModTime: layerTime, Format: tar.FormatPAX}
	var body []byte
	switch {
	case f.Dir:
		h.Typeflag, h.Mode, h.Name = tar.TypeDir, 0o755, h.Name+"/"
	case f.Link != "":
		h.Typeflag, h.Mode, h.Linkname = tar.TypeSymlink, 0o777, f.Link
	default:
		body, err = os.ReadFile(f.Source)
		if err != nil {
			return fmt.Errorf("reading %s for the %s layer: %w", f.Source, kind, err)
		}
		h.Typeflag, h.Mode, h.Size = tar.TypeReg, 0o644, int64(len(body))
		if f.Exec {
			h.Mode = 0o755
		}
	}
	if err := tw.WriteHeader(h); err != nil {
		return fmt.Errorf("writing %s to the %s layer: %w", rel, kind, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("writing %s to the %s layer: %w", rel, kind, err)
	}
	return nil
}

// layerPath checks that p names a place under root and returns it clean.
// A name starting with .wh. is refused: in a layer it is a whiteout, which
// would delete a file of the image below instead of adding one.
func layerPath(kind layerKind, root, p string) (string, error) {
	clean := path.Clean(p)
	if p == "" || clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("the layer path %q is not under %s", p, root)
	}
	for _, seg := range strings.Split(clean, "/") {
		if kind == kindContext {
			break
		}
		if strings.HasPrefix(seg, ".wh.") && kind == kindCode {
			return "", fmt.Errorf("the project file %q starts with .wh., which an image layer reads as a deletion; rename it or add it to exclude_paths", p)
		}
		if strings.HasPrefix(seg, ".wh.") {
			return "", fmt.Errorf("the installed file %q starts with .wh., which an image layer reads as a deletion", p)
		}
	}
	return clean, nil
}

// AppendCode pushes image: the dependency image base with files appended as
// one code layer, to every platform image of base when it is an index.
// Attestation manifests of an index are dropped, since they describe the
// images without the layer. The layers of base are not uploaded again: the
// registry already holds them in the repository base and image share.
func (r Registry) AppendCode(ctx context.Context, base Pinned, files []LayerFile, image string) error {
	dst, err := name.ParseReference(image)
	if err != nil {
		return fmt.Errorf("image %q is not a valid reference: %w", image, err)
	}
	src, err := name.ParseReference(base.Ref)
	if err != nil {
		return fmt.Errorf("image %q is not a valid reference: %w", base.Ref, err)
	}
	layers := &layerCache{files: files, made: map[types.MediaType]v1.Layer{}}
	defer layers.remove()
	opts := r.remoteOptions(ctx)
	if isIndex(base.Manifest) {
		return appendToIndex(src, dst, layers, opts)
	}
	img, err := remote.Image(src, opts...)
	if err != nil {
		return fmt.Errorf("reading the dependency image %s: %w", base.Ref, err)
	}
	out, err := layers.appendTo(img)
	if err != nil {
		return err
	}
	if err := remote.Write(dst, out, opts...); err != nil {
		return fmt.Errorf("pushing %s: %w", image, err)
	}
	return nil
}

// appendToIndex appends the code layer to every platform image of the index
// src and pushes the new index to dst.
func appendToIndex(src, dst name.Reference, layers *layerCache, opts []remote.Option) error {
	idx, err := remote.Index(src, opts...)
	if err != nil {
		return fmt.Errorf("reading the dependency image %s: %w", src, err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return fmt.Errorf("reading the index of %s: %w", src, err)
	}
	out := mutate.IndexMediaType(empty.Index, im.MediaType)
	images := 0
	for _, d := range im.Manifests {
		if isAttestation(d) {
			continue
		}
		if d.MediaType.IsIndex() {
			return fmt.Errorf("the dependency image %s nests an index (%s), which the code layer does not support", src, d.Digest)
		}
		img, ierr := idx.Image(d.Digest)
		if ierr != nil {
			return fmt.Errorf("reading %s of %s: %w", d.Digest, src, ierr)
		}
		withCode, aerr := layers.appendTo(img)
		if aerr != nil {
			return aerr
		}
		out = mutate.AppendManifests(out, mutate.IndexAddendum{Add: withCode, Descriptor: v1.Descriptor{Platform: d.Platform}})
		images++
	}
	if images == 0 {
		return fmt.Errorf("the dependency image %s lists no image for a platform", src)
	}
	if err := remote.WriteIndex(dst, out, opts...); err != nil {
		return fmt.Errorf("pushing %s: %w", dst, err)
	}
	return nil
}

// isAttestation reports whether d is an attestation manifest docker adds to
// an index, rather than an image for a platform.
func isAttestation(d v1.Descriptor) bool {
	if d.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
		return true
	}
	return d.Platform == nil || d.Platform.OS == "unknown"
}

// layerCache makes the code layer once per media type: a Docker v2 image
// takes a Docker layer, an OCI image an OCI one.
type layerCache struct {
	files   []LayerFile
	made    map[types.MediaType]v1.Layer
	removes []func()
}

// remove deletes the temporary files of the layers made.
func (c *layerCache) remove() {
	for _, r := range c.removes {
		r()
	}
}

// appendTo returns img with the code layer appended.
func (c *layerCache) appendTo(img v1.Image) (v1.Image, error) {
	mt, err := img.MediaType()
	if err != nil {
		return nil, fmt.Errorf("reading the media type of the dependency image: %w", err)
	}
	layerType := types.OCILayer
	if mt == types.DockerManifestSchema2 {
		layerType = types.DockerLayer
	}
	layer, ok := c.made[layerType]
	if !ok {
		var remove func()
		if layer, remove, err = codeLayer(c.files, layerType); err != nil {
			return nil, err
		}
		c.made[layerType] = layer
		c.removes = append(c.removes, remove)
	}
	out, err := mutate.Append(img, mutate.Addendum{
		Layer:   layer,
		History: v1.History{CreatedBy: codeLayerHistory, Created: v1.Time{Time: layerTime}},
	})
	if err != nil {
		return nil, fmt.Errorf("appending the code layer: %w", err)
	}
	return out, nil
}
