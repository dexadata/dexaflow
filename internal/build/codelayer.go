package build

import (
	"archive/tar"
	"bytes"
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

// LayerFile is one entry of a code layer.
type LayerFile struct {
	// Path is the slash-separated path under Workdir.
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

// codeLayer packs files as a deterministic tar layer of mediaType: entries
// sorted by path, owned by root, with a fixed modification time and modes
// normalized to 0755 for directories and executables, 0644 for other files
// (ADR 0073 section 2). Each source file is read once, here.
func codeLayer(files []LayerFile, mediaType types.MediaType) (v1.Layer, error) {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b LayerFile) int { return strings.Compare(a.Path, b.Path) })
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range sorted {
		if err := writeEntry(tw, f); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing the code layer: %w", err)
	}
	data := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}, tarball.WithMediaType(mediaType))
	if err != nil {
		return nil, fmt.Errorf("making the code layer: %w", err)
	}
	return layer, nil
}

// writeEntry writes one file of a code layer.
func writeEntry(tw *tar.Writer, f LayerFile) error {
	rel, err := layerPath(f.Path)
	if err != nil {
		return err
	}
	h := &tar.Header{Name: strings.TrimPrefix(Workdir, "/") + "/" + rel, ModTime: layerTime, Format: tar.FormatPAX}
	var body []byte
	switch {
	case f.Dir:
		h.Typeflag, h.Mode, h.Name = tar.TypeDir, 0o755, h.Name+"/"
	case f.Link != "":
		h.Typeflag, h.Mode, h.Linkname = tar.TypeSymlink, 0o777, f.Link
	default:
		body, err = os.ReadFile(f.Source)
		if err != nil {
			return fmt.Errorf("reading %s for the code layer: %w", f.Source, err)
		}
		h.Typeflag, h.Mode, h.Size = tar.TypeReg, 0o644, int64(len(body))
		if f.Exec {
			h.Mode = 0o755
		}
	}
	if err := tw.WriteHeader(h); err != nil {
		return fmt.Errorf("writing %s to the code layer: %w", rel, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("writing %s to the code layer: %w", rel, err)
	}
	return nil
}

// layerPath checks that p names a place under Workdir and returns it clean.
func layerPath(p string) (string, error) {
	clean := path.Clean(p)
	if p == "" || clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("the code layer path %q is not under %s", p, Workdir)
	}
	return clean, nil
}

// AppendCode pushes image: the dependency image base with files appended as
// one code layer, to every platform image of base when it is an index.
// Attestation manifests of an index are dropped, since they describe the
// images without the layer. The layers of base are not uploaded again: the
// registry already holds them in the repository base and image share.
func (Registry) AppendCode(ctx context.Context, base Pinned, files []LayerFile, image string) error {
	dst, err := name.ParseReference(image)
	if err != nil {
		return fmt.Errorf("image %q is not a valid reference: %w", image, err)
	}
	src, err := name.ParseReference(base.Ref)
	if err != nil {
		return fmt.Errorf("image %q is not a valid reference: %w", base.Ref, err)
	}
	layers := &layerCache{files: files, made: map[types.MediaType]v1.Layer{}}
	opts := remoteOptions(ctx)
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
	files []LayerFile
	made  map[types.MediaType]v1.Layer
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
		if layer, err = codeLayer(c.files, layerType); err != nil {
			return nil, err
		}
		c.made[layerType] = layer
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
