// Command imagepromote publishes the images of a release candidate as its GA
// without rebuilding them (ADR 0109).
//
// For each source image (a single manifest or a manifest list) it writes a new
// image whose configuration differs from the rc's only in:
//
//   - the environment variable DEXAFLOW_RELEASE_VERSION, set to the release,
//     which internal/version reads to report the GA version from an rc build;
//   - the label org.opencontainers.image.version, moved from the rc to the
//     release in the label's own form (with or without the leading "v");
//
// and whose manifest carries the annotation org.opencontainers.image.base.digest
// naming the rc manifest it came from. Every layer is referenced, never copied
// or rebuilt, and the command fails unless the promoted image lists exactly the
// rc's layer digests, in order, for every platform. Attestation manifests in a
// list (platform unknown/unknown) are dropped: they describe the rc digests,
// and the release workflow attests the promoted ones.
//
// Usage:
//
//	imagepromote -release v0.6.0 -from ghcr.io/o/img:v0.6.0-rc.2 -to ghcr.io/o/img:v0.6.0 [-to ...] [-dry-run]
//
// It prints one line per destination, "<ref> <digest>", and with -dry-run
// writes nothing and prints the digest the destination would get.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	releaseEnv      = "DEXAFLOW_RELEASE_VERSION"
	versionLabel    = "org.opencontainers.image.version"
	baseDigestAnnot = "org.opencontainers.image.base.digest"
	baseNameAnnot   = "org.opencontainers.image.base.name"
)

var (
	releasePattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	rcPattern      = regexp.MustCompile(`^(v?)(\d+\.\d+\.\d+)-rc\.\d+$`)
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if err := run(os.Args[1:], os.Stdout, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
		fmt.Fprintln(os.Stderr, "imagepromote:", err)
		os.Exit(1)
	}
}

type config struct {
	release string
	src     name.Reference
	dsts    []name.Reference
	dryRun  bool
}

func parseArgs(args []string) (config, error) {
	fs := flag.NewFlagSet("imagepromote", flag.ContinueOnError)
	release := fs.String("release", "", "the GA version the rc is promoted to, vX.Y.Z")
	from := fs.String("from", "", "the rc image (tag or digest)")
	dryRun := fs.Bool("dry-run", false, "build and verify the promoted image without writing it")
	var to multiFlag
	fs.Var(&to, "to", "a destination reference; repeatable")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if !releasePattern.MatchString(*release) {
		return config{}, fmt.Errorf("-release %q is not vX.Y.Z", *release)
	}
	if *from == "" || len(to) == 0 {
		return config{}, errors.New("-from and at least one -to are required")
	}
	src, err := name.ParseReference(*from)
	if err != nil {
		return config{}, fmt.Errorf("-from: %w", err)
	}
	cfg := config{release: *release, src: src, dryRun: *dryRun}
	for _, t := range to {
		d, perr := name.ParseReference(t)
		if perr != nil {
			return config{}, fmt.Errorf("-to %q: %w", t, perr)
		}
		cfg.dsts = append(cfg.dsts, d)
	}
	return cfg, nil
}

func run(args []string, out io.Writer, opts ...remote.Option) error {
	cfg, err := parseArgs(args)
	if err != nil {
		return err
	}
	w, digest, err := promoteRef(cfg.src, cfg.release, opts...)
	if err != nil {
		return err
	}
	for _, d := range cfg.dsts {
		if !cfg.dryRun {
			if werr := writeTaggable(d, w, opts...); werr != nil {
				return fmt.Errorf("write %s: %w", d, werr)
			}
		}
		if _, werr := fmt.Fprintf(out, "%s %s\n", d, digest); werr != nil {
			return werr
		}
	}
	return nil
}

// promoteRef reads src, an image or a list, and returns its promotion and the
// promoted digest.
func promoteRef(src name.Reference, release string, opts ...remote.Option) (remote.Taggable, v1.Hash, error) {
	desc, err := remote.Get(src, opts...)
	if err != nil {
		return nil, v1.Hash{}, fmt.Errorf("read %s: %w", src, err)
	}
	repo := src.Context().Name()
	if desc.MediaType.IsIndex() {
		return promoteRefIndex(desc, src, release, repo)
	}
	img, err := desc.Image()
	if err != nil {
		return nil, v1.Hash{}, err
	}
	p, err := promoteImage(img, release, repo)
	if err != nil {
		return nil, v1.Hash{}, fmt.Errorf("%s: %w", src, err)
	}
	d, err := p.Digest()
	return p, d, err
}

func promoteRefIndex(desc *remote.Descriptor, src name.Reference, release, repo string) (remote.Taggable, v1.Hash, error) {
	idx, err := desc.ImageIndex()
	if err != nil {
		return nil, v1.Hash{}, err
	}
	p, err := promoteIndex(idx, release, repo)
	if err != nil {
		return nil, v1.Hash{}, fmt.Errorf("%s: %w", src, err)
	}
	d, err := p.Digest()
	return p, d, err
}

func writeTaggable(ref name.Reference, t remote.Taggable, opts ...remote.Option) error {
	switch v := t.(type) {
	case v1.ImageIndex:
		return remote.WriteIndex(ref, v, opts...)
	case v1.Image:
		return remote.Write(ref, v, opts...)
	}
	return fmt.Errorf("unexpected %T", t)
}

// promoteIndex promotes every platform image of idx and assembles a list of
// the same media type, platforms and per-manifest annotations. Attestation
// manifests (platform unknown/unknown) are left out.
func promoteIndex(idx v1.ImageIndex, release, repo string) (v1.ImageIndex, error) {
	im, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	srcDigest, err := idx.Digest()
	if err != nil {
		return nil, err
	}
	out := mutate.IndexMediaType(empty.Index, im.MediaType)
	n := 0
	for _, m := range im.Manifests {
		if isAttestation(m) {
			continue
		}
		if !m.MediaType.IsImage() {
			return nil, fmt.Errorf("manifest %s has media type %s, not an image", m.Digest, m.MediaType)
		}
		img, err := idx.Image(m.Digest)
		if err != nil {
			return nil, err
		}
		p, err := promoteImage(img, release, repo)
		if err != nil {
			return nil, fmt.Errorf("platform %s: %w", platformString(m.Platform), err)
		}
		add := mutate.IndexAddendum{Add: p, Descriptor: v1.Descriptor{
			MediaType:   m.MediaType,
			Platform:    m.Platform,
			Annotations: m.Annotations,
		}}
		out = mutate.AppendManifests(out, add)
		n++
	}
	if n == 0 {
		return nil, errors.New("the list holds no platform image")
	}
	annotations := map[string]string{}
	for k, v := range im.Annotations {
		annotations[k] = v
	}
	annotations[baseDigestAnnot] = srcDigest.String()
	annotations[baseNameAnnot] = repo
	if im.MediaType == "application/vnd.oci.image.index.v1+json" {
		annotated, ok := mutate.Annotations(out, annotations).(v1.ImageIndex)
		if !ok {
			return nil, errors.New("annotating the list did not return a list")
		}
		out = annotated
	}
	return out, nil
}

// promoteImage returns img with the release in its configuration and the rc
// digest in its annotations, after checking the rc really is an rc of release
// and that the layers did not change.
func promoteImage(img v1.Image, release, repo string) (v1.Image, error) {
	srcDigest, err := img.Digest()
	if err != nil {
		return nil, err
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	cfg := *cf.Config.DeepCopy()

	label, ok := cfg.Labels[versionLabel]
	if !ok {
		return nil, fmt.Errorf("no %s label: cannot tell which version the image was built as", versionLabel)
	}
	newLabel, err := promotedLabel(label, release)
	if err != nil {
		return nil, err
	}
	labels := make(map[string]string, len(cfg.Labels))
	for k, v := range cfg.Labels {
		labels[k] = v
	}
	labels[versionLabel] = newLabel
	cfg.Labels = labels
	cfg.Env = setEnv(cfg.Env, releaseEnv, release)

	out, err := mutate.Config(img, cfg)
	if err != nil {
		return nil, err
	}
	mt, err := img.MediaType()
	if err != nil {
		return nil, err
	}
	// Docker schema 2 manifests carry no annotations; the base digest is then
	// only on the list and in the attestation.
	if mt == "application/vnd.oci.image.manifest.v1+json" {
		annotated, ok := mutate.Annotations(out, map[string]string{
			baseDigestAnnot: srcDigest.String(),
			baseNameAnnot:   repo,
		}).(v1.Image)
		if !ok {
			return nil, errors.New("annotating the image did not return an image")
		}
		out = annotated
	}
	if err := sameLayers(img, out); err != nil {
		return nil, err
	}
	return out, nil
}

// promotedLabel moves an rc version label to its release, keeping the label's
// form. It fails when the label is not an rc of release, which means the
// source image is not the rc this GA promotes.
func promotedLabel(label, release string) (string, error) {
	m := rcPattern.FindStringSubmatch(label)
	if m == nil {
		return "", fmt.Errorf("label %s=%q is not a release candidate", versionLabel, label)
	}
	if strings.TrimPrefix(release, "v") != m[2] {
		return "", fmt.Errorf("label %s=%q is an rc of v%s, not of %s", versionLabel, label, m[2], release)
	}
	return m[1] + m[2], nil
}

func setEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, key+"="+value)
}

func sameLayers(a, b v1.Image) error {
	am, err := a.Manifest()
	if err != nil {
		return err
	}
	bm, err := b.Manifest()
	if err != nil {
		return err
	}
	if len(am.Layers) != len(bm.Layers) {
		return fmt.Errorf("promoted image has %d layers, the rc %d", len(bm.Layers), len(am.Layers))
	}
	for i := range am.Layers {
		if am.Layers[i].Digest != bm.Layers[i].Digest {
			return fmt.Errorf("layer %d is %s, the rc's is %s", i, bm.Layers[i].Digest, am.Layers[i].Digest)
		}
	}
	ac, err := a.ConfigFile()
	if err != nil {
		return err
	}
	bc, err := b.ConfigFile()
	if err != nil {
		return err
	}
	if len(ac.RootFS.DiffIDs) != len(bc.RootFS.DiffIDs) {
		return errors.New("promoted image changed the rootfs diff ids")
	}
	for i := range ac.RootFS.DiffIDs {
		if ac.RootFS.DiffIDs[i] != bc.RootFS.DiffIDs[i] {
			return errors.New("promoted image changed the rootfs diff ids")
		}
	}
	return nil
}

func isAttestation(d v1.Descriptor) bool {
	if d.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
		return true
	}
	return d.Platform != nil && d.Platform.OS == "unknown" && d.Platform.Architecture == "unknown"
}

func platformString(p *v1.Platform) string {
	if p == nil {
		return "unknown"
	}
	return p.String()
}
