package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func newRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func rcImage(t *testing.T, label string, oci bool) v1.Image {
	t.Helper()
	img, err := random.Image(256, 3)
	if err != nil {
		t.Fatal(err)
	}
	if oci {
		img = mutate.MediaType(img, types.OCIManifestSchema1)
		img = mutate.ConfigMediaType(img, types.OCIConfigJSON)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg := cf.Config
	cfg.Env = []string{"PATH=/usr/bin", releaseEnv + "=stale"}
	cfg.Labels = map[string]string{versionLabel: label, "org.opencontainers.image.revision": "abc"}
	img, err = mutate.Config(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func pushImage(t *testing.T, ref string, img v1.Image) {
	t.Helper()
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
}

func pushIndex(t *testing.T, ref string, idx v1.ImageIndex) {
	t.Helper()
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(r, idx); err != nil {
		t.Fatal(err)
	}
}

func layerDigests(t *testing.T, img v1.Image) []v1.Hash {
	t.Helper()
	m, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	var out []v1.Hash
	for _, l := range m.Layers {
		out = append(out, l.Digest)
	}
	return out
}

func checkPromoted(t *testing.T, rc, ga v1.Image, wantLabel string) {
	t.Helper()
	want, got := layerDigests(t, rc), layerDigests(t, ga)
	if len(want) != len(got) {
		t.Fatalf("layers = %v, want %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("layer %d = %s, want %s", i, got[i], want[i])
		}
	}
	cf, err := ga.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if l := cf.Config.Labels[versionLabel]; l != wantLabel {
		t.Errorf("version label = %q, want %q", l, wantLabel)
	}
	if cf.Config.Labels["org.opencontainers.image.revision"] != "abc" {
		t.Errorf("other labels changed: %v", cf.Config.Labels)
	}
	var n int
	for _, e := range cf.Config.Env {
		if strings.HasPrefix(e, releaseEnv+"=") {
			n++
			if e != releaseEnv+"=v0.6.0" {
				t.Errorf("env %q, want %s=v0.6.0", e, releaseEnv)
			}
		}
	}
	if n != 1 {
		t.Errorf("env %v carries %s %d times, want once", cf.Config.Env, releaseEnv, n)
	}
	if cf.Config.Env[0] != "PATH=/usr/bin" {
		t.Errorf("env %v lost the rc's own variables", cf.Config.Env)
	}
}

func TestPromoteSingleOCIImage(t *testing.T) {
	reg := newRegistry(t)
	rc := rcImage(t, "0.6.0-rc.2", true)
	pushImage(t, reg+"/server:v0.6.0-rc.2", rc)
	rcDigest, _ := rc.Digest()

	var out bytes.Buffer
	err := run([]string{"-release", "v0.6.0", "-from", reg + "/server:v0.6.0-rc.2",
		"-to", reg + "/server:v0.6.0", "-to", reg + "/server:latest"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output %q, want one line per destination", out.String())
	}
	for _, tag := range []string{"v0.6.0", "latest"} {
		ref, _ := name.ParseReference(reg + "/server:" + tag)
		ga, err := remote.Image(ref)
		if err != nil {
			t.Fatal(err)
		}
		checkPromoted(t, rc, ga, "0.6.0")
		m, _ := ga.Manifest()
		if m.Annotations[baseDigestAnnot] != rcDigest.String() {
			t.Errorf("base digest annotation = %q, want %s", m.Annotations[baseDigestAnnot], rcDigest)
		}
		d, _ := ga.Digest()
		if !strings.HasSuffix(lines[0], d.String()) {
			t.Errorf("printed %q, pushed %s", lines[0], d)
		}
	}
}

func TestPromoteIndexKeepsPlatformsAndDropsAttestations(t *testing.T) {
	reg := newRegistry(t)
	amd := rcImage(t, "v0.6.0-rc.1", false)
	arm := rcImage(t, "v0.6.0-rc.1", false)
	att, _ := random.Image(64, 1)
	idx := mutate.IndexMediaType(empty.Index, types.DockerManifestList)
	idx = mutate.AppendManifests(idx,
		mutate.IndexAddendum{Add: amd, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: arm, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
		mutate.IndexAddendum{Add: att, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "unknown", Architecture: "unknown"}}},
	)
	pushIndex(t, reg+"/runtime:py3.12-v0.6.0-rc.1", idx)

	var out bytes.Buffer
	if err := run([]string{"-release", "v0.6.0", "-from", reg + "/runtime:py3.12-v0.6.0-rc.1",
		"-to", reg + "/legacy-runtime:py3.12-v0.6.0"}, &out); err != nil {
		t.Fatal(err)
	}
	ref, _ := name.ParseReference(reg + "/legacy-runtime:py3.12-v0.6.0")
	ga, err := remote.Index(ref)
	if err != nil {
		t.Fatal(err)
	}
	im, _ := ga.IndexManifest()
	if im.MediaType != types.DockerManifestList {
		t.Errorf("media type %s, want the rc's %s", im.MediaType, types.DockerManifestList)
	}
	if len(im.Manifests) != 2 {
		t.Fatalf("manifests %d, want 2 (attestation dropped)", len(im.Manifests))
	}
	for i, rc := range []v1.Image{amd, arm} {
		img, err := ga.Image(im.Manifests[i].Digest)
		if err != nil {
			t.Fatal(err)
		}
		checkPromoted(t, rc, img, "v0.6.0")
	}
	if im.Manifests[1].Platform.Architecture != "arm64" {
		t.Errorf("platforms %v", im.Manifests)
	}
}

func TestPromoteRefusesWrongSource(t *testing.T) {
	reg := newRegistry(t)
	pushImage(t, reg+"/a:other-rc", rcImage(t, "0.5.12-rc.1", true))
	pushImage(t, reg+"/a:ga", rcImage(t, "0.6.0", true))
	cases := map[string][]string{
		"rc of another release": {"-release", "v0.6.0", "-from", reg + "/a:other-rc", "-to", reg + "/a:x"},
		"a GA as source":        {"-release", "v0.6.0", "-from", reg + "/a:ga", "-to", reg + "/a:x"},
		"release not semver":    {"-release", "v0.6.0-rc.3", "-from", reg + "/a:ga", "-to", reg + "/a:x"},
		"no destination":        {"-release", "v0.6.0", "-from", reg + "/a:ga"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if err := run(args, &bytes.Buffer{}); err == nil {
				t.Fatal("run succeeded, want an error")
			}
		})
	}
	ref, _ := nameRef(reg + "/a:x")
	if _, err := remote.Head(ref); err == nil {
		t.Error("a refused promotion wrote the destination")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	reg := newRegistry(t)
	pushImage(t, reg+"/a:rc", rcImage(t, "0.6.0-rc.4", true))
	var out bytes.Buffer
	if err := run([]string{"-dry-run", "-release", "v0.6.0", "-from", reg + "/a:rc", "-to", reg + "/a:v0.6.0"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sha256:") {
		t.Errorf("dry run printed %q, want the digest", out.String())
	}
	ref, _ := nameRef(reg + "/a:v0.6.0")
	if _, err := remote.Head(ref); err == nil {
		t.Error("dry run wrote the destination")
	}
}

func TestSameLayersDetectsAChange(t *testing.T) {
	a := rcImage(t, "0.6.0-rc.1", true)
	l, _ := random.Layer(32, types.DockerLayer)
	b, err := mutate.AppendLayers(a, l)
	if err != nil {
		t.Fatal(err)
	}
	if err := sameLayers(a, b); err == nil {
		t.Error("sameLayers accepted an added layer")
	}
	if err := sameLayers(a, a); err != nil {
		t.Errorf("sameLayers(a, a) = %v", err)
	}
}

func nameRef(s string) (name.Reference, error) { return name.ParseReference(s) }
