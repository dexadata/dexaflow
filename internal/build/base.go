package build

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// baseCacheFormat versions the layout of a cached base listing; a file of
// another format is read again from the registry.
const baseCacheFormat = 1

// dpkgStatusPath is the dpkg database a Debian base records glibc in.
const dpkgStatusPath = "var/lib/dpkg/status"

// maxStatusSize bounds the dpkg status read from a base layer.
const maxStatusSize = 16 << 20

// BaseImage is what the daemonless dependency layer (ADR 0078) needs to know
// about one platform image of the runtime base.
type BaseImage struct {
	// Format is the layout of a cached listing (baseCacheFormat).
	Format int `json:"format"`
	// Digest is the digest of the platform image, sha256:<hex>.
	Digest string `json:"digest"`
	// Python is PYTHON_VERSION of the image config, X.Y.Z; empty when the
	// config sets none, which no Dexaflow runtime base does.
	Python string `json:"python"`
	// Glibc is the version of the libc6 package dpkg installed, X.Y; empty
	// when the image records none (a musl or distroless image).
	Glibc string `json:"glibc"`
	// SitePackages reports that the image has the site-packages directory
	// of Python, usr/local/lib/pythonX.Y/site-packages.
	SitePackages bool `json:"site_packages"`
	// Dists are the distributions installed in that site-packages, as
	// name==version with the name normalized, sorted.
	Dists []string `json:"dists"`
}

// MinorPython is the X.Y of Python, or "" when the image has none.
func (b BaseImage) MinorPython() string {
	parts := strings.SplitN(b.Python, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// SitePackagesDir is the site-packages directory of the image, without a
// leading slash.
func (b BaseImage) SitePackagesDir() string {
	return "usr/local/lib/python" + b.MinorPython() + "/site-packages"
}

// Base reads what the platform image of base (an index or a single image,
// pinned by digest) holds for the daemonless dependency layer: the Python
// version of its config and, for a runtime base, the glibc version and the
// distributions of its site-packages, from the layer tar headers (and the
// dpkg status file). The listing is cached by the platform image digest
// under the user cache directory, so a redeploy reads no layer again.
func (Registry) Base(ctx context.Context, base Pinned, platform string) (BaseImage, error) {
	digest, err := platformDigest(base, platform)
	if err != nil {
		return BaseImage{}, err
	}
	if cached, ok := readBaseCache(digest); ok {
		return cached, nil
	}
	ref, err := name.ParseReference(base.Ref)
	if err != nil {
		return BaseImage{}, fmt.Errorf("image %q is not a valid reference: %w", base.Ref, err)
	}
	img, err := remote.Image(ref.Context().Digest(digest), remoteOptions(ctx)...)
	if err != nil {
		return BaseImage{}, fmt.Errorf("reading the base image %s for %s: %w", base.Ref, platform, err)
	}
	got, err := inspectBase(img, platform)
	if err != nil {
		return BaseImage{}, fmt.Errorf("reading the base image %s for %s: %w", base.Ref, platform, err)
	}
	got.Digest = digest
	writeBaseCache(got)
	return got, nil
}

// platformDigest is the digest of the image for platform in base: the entry
// of an index, attestations skipped, or base itself when it is one image.
func platformDigest(base Pinned, platform string) (string, error) {
	if !isIndex(base.Manifest) {
		return base.Ref[strings.LastIndex(base.Ref, "@")+1:], nil
	}
	idx, err := v1.ParseIndexManifest(bytes.NewReader(base.Manifest))
	if err != nil {
		return "", fmt.Errorf("reading the index of the base image %s: %w", base.Ref, err)
	}
	for _, d := range idx.Manifests {
		if isAttestation(d) || d.MediaType.IsIndex() {
			continue
		}
		if samePlatform(d.Platform.String(), platform) {
			return d.Digest.String(), nil
		}
	}
	return "", fmt.Errorf("the base image %s has no image for %s", base.Ref, platform)
}

// inspectBase reads the config of img and, when it names a Python version,
// its layers.
func inspectBase(img v1.Image, platform string) (BaseImage, error) {
	cfg, err := img.ConfigFile()
	if err != nil {
		return BaseImage{}, fmt.Errorf("reading the image config: %w", err)
	}
	if cfg.OS != "" && cfg.Architecture != "" {
		have := cfg.OS + "/" + cfg.Architecture
		if cfg.Variant != "" {
			have += "/" + cfg.Variant
		}
		if !samePlatform(have, platform) {
			return BaseImage{}, fmt.Errorf("the image is for %s, not %s", have, platform)
		}
	}
	out := BaseImage{Format: baseCacheFormat, Dists: []string{}}
	for _, e := range cfg.Config.Env {
		if v, ok := strings.CutPrefix(e, "PYTHON_VERSION="); ok {
			out.Python = v
		}
	}
	if out.MinorPython() == "" {
		out.Python = ""
		return out, nil
	}
	layers, err := img.Layers()
	if err != nil {
		return BaseImage{}, fmt.Errorf("reading the layers: %w", err)
	}
	s := baseScan{sp: out.SitePackagesDir(), dists: map[string]bool{}}
	for _, l := range layers {
		if err := s.layer(l); err != nil {
			return BaseImage{}, err
		}
	}
	out.Glibc, out.SitePackages = parseGlibc(s.status), s.hasSP
	for d := range s.dists {
		if dist, ok := distOf(d); ok {
			out.Dists = append(out.Dists, dist)
		}
	}
	slices.Sort(out.Dists)
	return out, nil
}

// baseScan is the state of the layer walk of a base image: what the layers
// read so far leave in site-packages, and the last dpkg status.
type baseScan struct {
	sp     string
	hasSP  bool
	dists  map[string]bool
	status []byte
}

// layerScan is what one layer adds and removes.
type layerScan struct {
	added   map[string]bool
	deleted map[string]bool
	clear   bool
	hasSP   bool
}

// layer applies one layer to the scan: its whiteouts hide what the layers
// below put there, then what it adds is added.
func (s *baseScan) layer(l v1.Layer) error {
	rc, err := l.Uncompressed()
	if err != nil {
		return fmt.Errorf("reading a layer: %w", err)
	}
	defer rc.Close() //nolint:errcheck // a read-only stream
	ls := layerScan{added: map[string]bool{}, deleted: map[string]bool{}}
	tr := tar.NewReader(rc)
	for {
		h, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			return fmt.Errorf("reading a layer: %w", nerr)
		}
		if err := s.entry(&ls, tr, h); err != nil {
			return err
		}
	}
	if ls.clear {
		s.hasSP, s.dists = false, map[string]bool{}
	}
	for d := range ls.deleted {
		delete(s.dists, d)
	}
	for d := range ls.added {
		s.dists[d] = true
	}
	s.hasSP = s.hasSP || ls.hasSP
	return nil
}

// entry records one tar entry of a layer.
func (s *baseScan) entry(ls *layerScan, tr io.Reader, h *tar.Header) error {
	p := strings.TrimSuffix(strings.TrimPrefix(path.Clean("/"+h.Name), "/"), "/")
	if p == dpkgStatusPath && h.Typeflag == tar.TypeReg {
		body, err := io.ReadAll(io.LimitReader(tr, maxStatusSize))
		if err != nil {
			return fmt.Errorf("reading %s: %w", dpkgStatusPath, err)
		}
		s.status = body
		return nil
	}
	dir, base := path.Split(p)
	dir = strings.TrimSuffix(dir, "/")
	if hidden, ok := strings.CutPrefix(base, ".wh."); ok {
		s.whiteout(ls, dir, hidden)
		return nil
	}
	if p == s.sp || strings.HasPrefix(p, s.sp+"/") {
		ls.hasSP = true
	}
	if rest, ok := strings.CutPrefix(p, s.sp+"/"); ok {
		if top, _, _ := strings.Cut(rest, "/"); strings.HasSuffix(top, ".dist-info") {
			ls.added[top] = true
		}
	}
	return nil
}

// whiteout records the whiteout of hidden in dir: an opaque directory
// (.wh..wh..opq) hides all of dir from the layers below, any other the one
// name.
func (s *baseScan) whiteout(ls *layerScan, dir, hidden string) {
	gone := path.Join(dir, hidden)
	if hidden == ".wh..opq" {
		gone = dir
	}
	switch {
	case gone == s.sp || strings.HasPrefix(s.sp, gone+"/"):
		ls.clear = true
	case path.Dir(gone) == s.sp:
		ls.deleted[path.Base(gone)] = true
	}
}

// distName and distVersion match the two parts of a dist-info directory
// name; anything else is not listed, so the listing can only ever hold
// name==version lines.
var (
	distName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._]*$`)
	distVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+!_]*$`)
)

// distOf turns a dist-info directory name, name-version.dist-info, into
// name==version with the name normalized as PyPI compares names.
func distOf(dir string) (string, bool) {
	stem := strings.TrimSuffix(dir, ".dist-info")
	n, v, ok := strings.Cut(stem, "-")
	if !ok || !distName.MatchString(n) || !distVersion.MatchString(v) {
		return "", false
	}
	return NormalizeDistName(n) + "==" + v, true
}

// NormalizeDistName is a distribution name as PyPI compares names (PEP 503):
// lower case, with runs of -, _ and . as one -.
func NormalizeDistName(n string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(n), func(r rune) bool { return r == '-' || r == '_' || r == '.' }), "-")
}

// parseGlibc returns the X.Y version of the installed libc6 package in a
// dpkg status file, or "" when it lists none.
func parseGlibc(status []byte) string {
	for _, stanza := range strings.Split(string(status), "\n\n") {
		fields := map[string]string{}
		sc := bufio.NewScanner(strings.NewReader(stanza))
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), ": "); ok {
				fields[k] = v
			}
		}
		if fields["Package"] != "libc6" || !strings.HasSuffix(fields["Status"], " installed") {
			continue
		}
		parts := strings.SplitN(fields["Version"], ".", 3)
		if len(parts) < 2 {
			return ""
		}
		minor, _, _ := strings.Cut(parts[1], "-")
		minor, _, _ = strings.Cut(minor, "+")
		return parts[0] + "." + minor
	}
	return ""
}

// baseCacheFile is where the listing of the platform image digest is cached,
// or "" when the user has no cache directory.
func baseCacheFile(digest string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "dexaflow", "base", strings.ReplaceAll(digest, ":", "-")+".json")
}

// readBaseCache returns the cached listing of digest, if one of this format
// is there.
func readBaseCache(digest string) (BaseImage, bool) {
	file := baseCacheFile(digest)
	if file == "" {
		return BaseImage{}, false
	}
	data, err := os.ReadFile(file) //nolint:gosec // G304: a file of the user cache named by a digest.
	if err != nil {
		return BaseImage{}, false
	}
	var b BaseImage
	if json.Unmarshal(data, &b) != nil || b.Format != baseCacheFormat || b.Digest != digest {
		return BaseImage{}, false
	}
	return b, true
}

// writeBaseCache caches a listing. A cache that cannot be written only
// means the next build reads the layers again, so the failure is logged.
func writeBaseCache(b BaseImage) {
	file := baseCacheFile(b.Digest)
	if file == "" {
		return
	}
	data, err := json.Marshal(b)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(file), 0o700)
	}
	if err == nil {
		err = os.WriteFile(file, data, 0o600)
	}
	if err != nil {
		slog.Debug("caching the base image listing", "file", file, "err", err)
	}
}
