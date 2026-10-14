package build

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Copy copies the image src names, pinned by digest, into the repository
// dstRepo of another registry (or the same one), with every layer and, for an
// index, every platform's image, and returns dstRepo@digest. The manifest is
// copied byte for byte, so the copy has the source's digest: `dexaflow
// promote` registers the same bytes on another control plane (#1708). With a
// tag the copy is pushed under it too, so a registry that collects untagged
// images keeps it; without one it is pushed by digest. Credentials and
// transport are those of Resolve, read per registry.
func (r Registry) Copy(ctx context.Context, src, dstRepo, tag string) (string, error) {
	from, err := name.NewDigest(src)
	if err != nil {
		return "", fmt.Errorf("image %q is not pinned by digest (<repo>@sha256:<hex>), so it cannot be copied as the same bytes: %w", src, err)
	}
	repo, err := name.NewRepository(dstRepo)
	if err != nil {
		return "", fmt.Errorf("destination %q is not a repository (<registry>/<path>, no tag or digest): %w", dstRepo, err)
	}
	pinned := repo.Digest(from.DigestStr())
	var to name.Reference = pinned
	if tag != "" {
		to = repo.Tag(tag)
	}
	opts := r.remoteOptions(ctx)
	desc, err := remote.Get(from, opts...)
	if err != nil {
		return "", fmt.Errorf("reading %q from its registry: %w", src, err)
	}
	if err := writeCopy(desc, to, opts); err != nil {
		return "", fmt.Errorf("copying %q to %q: %w", src, to.String(), err)
	}
	return dstRepo + "@" + from.DigestStr(), nil
}

// writeCopy writes the manifest or index desc describes, with what it
// references, to to.
func writeCopy(desc *remote.Descriptor, to name.Reference, opts []remote.Option) error {
	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		idx, err := desc.ImageIndex()
		if err != nil {
			return fmt.Errorf("reading the index: %w", err)
		}
		return remote.WriteIndex(to, idx, opts...)
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		img, err := desc.Image()
		if err != nil {
			return fmt.Errorf("reading the image: %w", err)
		}
		return remote.Write(to, img, opts...)
	default:
		return fmt.Errorf("media type %q is not an OCI or Docker v2 manifest or index", desc.MediaType)
	}
}
