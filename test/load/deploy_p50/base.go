package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// standInGlibc is the libc6 the stand-in base records, that of Debian 12,
// which the published runtime base is built on.
const standInGlibc = "2.36-9+deb12u10"

// seedStandInBase pushes to the registry at host a stand-in for the runtime
// base of Python pythonMinor, as an index of one image for platform, and
// returns its reference. It holds only what the daemonless dependency layer
// reads from a base (ADR 0078): PYTHON_VERSION in the config, the dpkg
// status naming glibc, and the site-packages directory. It runs nothing: a
// code-only deploy never runs its image, and a run on a cluster needs the
// real base (--base-image).
func seedStandInBase(host, pythonMinor, platform string) (string, error) {
	plat, err := v1.ParsePlatform(platform)
	if err != nil {
		return "", fmt.Errorf("platform %q: %w", platform, err)
	}
	layer, err := standInLayer(pythonMinor)
	if err != nil {
		return "", err
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return "", fmt.Errorf("building the stand-in base: %w", err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return "", fmt.Errorf("reading the stand-in base config: %w", err)
	}
	cf = cf.DeepCopy()
	cf.OS, cf.Architecture, cf.Variant = plat.OS, plat.Architecture, plat.Variant
	cf.Config.Env = []string{"PATH=/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "PYTHON_VERSION=" + pythonMinor + ".0"}
	if img, err = mutate.ConfigFile(img, cf); err != nil {
		return "", fmt.Errorf("configuring the stand-in base: %w", err)
	}
	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: plat}})
	ref := host + "/runtime:py" + pythonMinor
	r, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("stand-in base reference %q: %w", ref, err)
	}
	if err := remote.WriteIndex(r, idx); err != nil {
		return "", fmt.Errorf("pushing the stand-in base: %w", err)
	}
	return ref, nil
}

// standInLayer is the one layer of the stand-in base.
func standInLayer(pythonMinor string) (v1.Layer, error) {
	status := "Package: libc6\nStatus: install ok installed\nVersion: " + standInGlibc + "\n"
	entries := []struct {
		name, body string
		dir        bool
	}{
		{name: "usr/local/lib/python" + pythonMinor + "/site-packages/", dir: true},
		{name: "var/lib/dpkg/status", body: status},
		{name: "home/leoflow/", dir: true},
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	epoch := time.Unix(0, 0)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), ModTime: epoch, Typeflag: tar.TypeReg}
		if e.dir {
			h.Mode, h.Size, h.Typeflag = 0o755, 0, tar.TypeDir
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, fmt.Errorf("writing the stand-in layer: %w", err)
		}
		if _, err := io.WriteString(tw, e.body); err != nil {
			return nil, fmt.Errorf("writing the stand-in layer: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("writing the stand-in layer: %w", err)
	}
	b := buf.Bytes()
	return tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil })
}
