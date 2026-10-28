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
const baseCacheFormat = 2

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
	// PipConfig names what in the image tells pip where or what to install,
	// sorted: PIP_* (and XDG_CONFIG_*) variables of the config, and pip
	// configuration files (/etc/pip.conf, ...). The daemon's pip honors
	// them; the dependency layer cannot, so it declines such a base.
	PipConfig []string `json:"pip_config"`
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
func (r Registry) Base(ctx context.Context, base Pinned, platform string) (BaseImage, error) {
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
	img, err := remote.Image(ref.Context().Digest(digest), r.remoteOptions(ctx)...)
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

// pythonVersion matches the PYTHON_VERSION a runtime base may set: X.Y.Z
// and an optional pre-release suffix. Anything else is not used, since the
// version reaches uv's command line and the key.
var pythonVersion = regexp.MustCompile(`^\d+\.\d+\.\d+[A-Za-z0-9]*$`)

// harmlessPipEnv are the PIP_* variables that change how pip runs but not
// where it finds packages or which ones it installs. Any other PIP_*
// variable in a base config is recorded in BaseImage.PipConfig.
var harmlessPipEnv = map[string]bool{
	"PIP_NO_CACHE_DIR": true, "PIP_CACHE_DIR": true, "PIP_DISABLE_PIP_VERSION_CHECK": true, "PIP_ROOT_USER_ACTION": true,
	"PIP_PROGRESS_BAR": true, "PIP_NO_COLOR": true, "PIP_NO_INPUT": true, "PIP_QUIET": true, "PIP_VERBOSE": true,
	"PIP_DEFAULT_TIMEOUT": true, "PIP_TIMEOUT": true, "PIP_RETRIES": true, "PIP_BREAK_SYSTEM_PACKAGES": true,
}

// pipLocationEnv are the variables that move where pip reads its
// configuration files from.
var pipLocationEnv = map[string]bool{"XDG_CONFIG_DIRS": true, "XDG_CONFIG_HOME": true}

// inspectBase reads the config of img and, when it names a Python version,
// its layers.
func inspectBase(img v1.Image, platform string) (BaseImage, error) {
	cfg, err := img.ConfigFile()
	if err != nil {
		return BaseImage{}, fmt.Errorf("reading the image config: %w", err)
	}
	if perr := checkPlatform(cfg, platform); perr != nil {
		return BaseImage{}, perr
	}
	out := fromEnv(cfg.Config.Env)
	if out.Python == "" {
		return out, nil
	}
	layers, err := img.Layers()
	if err != nil {
		return BaseImage{}, fmt.Errorf("reading the layers: %w", err)
	}
	s := baseScan{sp: out.SitePackagesDir(), present: map[string]bool{}}
	for _, l := range layers {
		if err := s.layer(l); err != nil {
			return BaseImage{}, err
		}
	}
	s.collect(&out)
	return out, nil
}

// checkPlatform refuses an image whose config names another platform.
func checkPlatform(cfg *v1.ConfigFile, platform string) error {
	if cfg.OS == "" || cfg.Architecture == "" {
		return nil
	}
	have := cfg.OS + "/" + cfg.Architecture
	if cfg.Variant != "" {
		have += "/" + cfg.Variant
	}
	if !samePlatform(have, platform) {
		return fmt.Errorf("the image is for %s, not %s", have, platform)
	}
	return nil
}

// fromEnv is what the environment of a base config says: its Python
// version, and the variables that tell pip where or what to install.
func fromEnv(env []string) BaseImage {
	out := BaseImage{Format: baseCacheFormat, Dists: []string{}, PipConfig: []string{}}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		switch {
		case k == "PYTHON_VERSION" && pythonVersion.MatchString(v):
			out.Python = v
		case (strings.HasPrefix(k, "PIP_") && !harmlessPipEnv[k]) || pipLocationEnv[k]:
			out.PipConfig = append(out.PipConfig, k)
		}
	}
	return out
}

// isPipConfig reports whether p (a path without its leading slash) is a
// pip configuration file in a place pip reads one from: the global ones
// (/etc/pip.conf, <XDG config dir>/pip/pip.conf), the site one
// (<prefix>/pip.conf) and the user ones (~/.config/pip/pip.conf,
// ~/.pip/pip.conf). Any file named pip.conf or pip.ini outside a
// site-packages directory counts, so a home or prefix this check does not
// know is covered too.
func isPipConfig(p string) bool {
	base := path.Base(p)
	return (base == "pip.conf" || base == "pip.ini") && !strings.Contains(p, "/site-packages/")
}

// baseScan is the state of the layer walk of a base image: the paths the
// layers read so far leave in the image, among those the dependency layer
// looks at (site-packages, its dist-info directories, pip configuration
// files, the dpkg status), and the content of that status.
type baseScan struct {
	sp      string
	present map[string]bool
	status  []byte
}

// layerScan is what one layer adds and removes.
type layerScan struct {
	added   map[string]bool
	deleted []string
	opaque  []string
	status  []byte
}

// collect records in out what the walk left in the image: glibc,
// site-packages, the distributions and the pip configuration files.
func (s *baseScan) collect(out *BaseImage) {
	out.Glibc = parseGlibc(s.status)
	for p := range s.present {
		switch {
		case p == s.sp:
			out.SitePackages = true
		case isPipConfig(p):
			out.PipConfig = append(out.PipConfig, "/"+p)
		default:
			if dist, ok := distOf(strings.TrimPrefix(p, s.sp+"/")); ok {
				out.Dists = append(out.Dists, dist)
			}
		}
	}
	slices.Sort(out.Dists)
	slices.Sort(out.PipConfig)
}

// layer applies one layer to the scan: its whiteouts hide what the layers
// below put there, then what it adds is added.
func (s *baseScan) layer(l v1.Layer) error {
	rc, err := l.Uncompressed()
	if err != nil {
		return fmt.Errorf("reading a layer: %w", err)
	}
	defer rc.Close() //nolint:errcheck // a read-only stream
	ls := layerScan{added: map[string]bool{}}
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
	for p := range s.present {
		if hidden(p, ls.deleted, ls.opaque) {
			delete(s.present, p)
		}
	}
	if !s.present[dpkgStatusPath] {
		s.status = nil
	}
	for p := range ls.added {
		s.present[p] = true
	}
	if ls.status != nil {
		s.status = ls.status
	}
	return nil
}

// hidden reports whether a whiteout of this layer hides p: p or one of its
// parents is deleted, or p is under an opaque directory.
func hidden(p string, deleted, opaque []string) bool {
	for _, d := range deleted {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	for _, d := range opaque {
		if strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// entry records one tar entry of a layer.
func (s *baseScan) entry(ls *layerScan, tr io.Reader, h *tar.Header) error {
	p := strings.TrimSuffix(strings.TrimPrefix(path.Clean("/"+h.Name), "/"), "/")
	dir, base := path.Split(p)
	dir = strings.TrimSuffix(dir, "/")
	if gone, ok := strings.CutPrefix(base, ".wh."); ok {
		if gone == ".wh..opq" {
			ls.opaque = append(ls.opaque, dir)
		} else {
			ls.deleted = append(ls.deleted, path.Join(dir, gone))
		}
		return nil
	}
	switch {
	case p == dpkgStatusPath && h.Typeflag == tar.TypeReg:
		body, err := io.ReadAll(io.LimitReader(tr, maxStatusSize))
		if err != nil {
			return fmt.Errorf("reading %s: %w", dpkgStatusPath, err)
		}
		ls.added[p], ls.status = true, body
	case isPipConfig(p) && h.Typeflag != tar.TypeDir:
		ls.added[p] = true
	case p == s.sp || strings.HasPrefix(p, s.sp+"/"):
		ls.added[s.sp] = true
		if rest, ok := strings.CutPrefix(p, s.sp+"/"); ok {
			if top, _, _ := strings.Cut(rest, "/"); strings.HasSuffix(top, ".dist-info") {
				ls.added[s.sp+"/"+top] = true
			}
		}
	}
	return nil
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
		version := fields["Version"]
		if _, rest, epoch := strings.Cut(version, ":"); epoch {
			version = rest
		}
		parts := strings.SplitN(version, ".", 3)
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
	if json.Unmarshal(data, &b) != nil || b.Format != baseCacheFormat || b.Digest != digest ||
		(b.Python != "" && !pythonVersion.MatchString(b.Python)) {
		return BaseImage{}, false
	}
	for _, d := range b.Dists {
		if !cachedDist.MatchString(d) {
			return BaseImage{}, false
		}
	}
	return b, true
}

// cachedDist matches a distribution of a cached listing, as distOf writes
// it; a listing holding anything else is read again from the registry.
var cachedDist = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*==[A-Za-z0-9][A-Za-z0-9.+!_]*$`)

// writeBaseCache caches a listing, through a temporary file renamed into
// place, so a concurrent build never reads half a listing. A cache that
// cannot be written only means the next build reads the layers again, so
// the failure is logged.
func writeBaseCache(b BaseImage) {
	file := baseCacheFile(b.Digest)
	if file == "" {
		return
	}
	if err := writeFileAtomic(file, b); err != nil {
		slog.Debug("caching the base image listing", "file", file, "err", err)
	}
}

// writeFileAtomic writes v as JSON to file through a temporary file in the
// same directory.
func writeFileAtomic(file string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", file, err)
	}
	if merr := os.MkdirAll(filepath.Dir(file), 0o700); merr != nil {
		return fmt.Errorf("making the directory of %s: %w", file, merr)
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".tmp*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", file, err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), file)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name()) //nolint:errcheck // best-effort cleanup; the write error is the one to report
		return fmt.Errorf("writing %s: %w", file, werr)
	}
	return nil
}
