package main

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/build"
)

// The stand-in base the local registry serves is one the daemonless
// dependency layer accepts (ADR 0078): the CLI's own base reader finds its
// Python version, its glibc and its site-packages directory.
func TestStandInBaseIsADependencyLayerBase(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host, closeReg, _, err := serveCountingRegistry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeReg()
	ref, err := seedStandInBase(host, "3.12", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if ref != host+"/runtime:py3.12" {
		t.Errorf("ref = %q", ref)
	}
	ctx := context.Background()
	pinned, err := build.Registry{}.Resolve(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	base, err := build.Registry{}.Base(ctx, pinned, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if base.MinorPython() != "3.12" || base.Glibc == "" || !base.SitePackages {
		t.Errorf("base = %+v, want Python 3.12, a glibc and site-packages", base)
	}
	if len(base.PipConfig) != 0 {
		t.Errorf("the stand-in base configures pip: %v", base.PipConfig)
	}
}

// The local registry counts the requests it serves, so a measurement can
// say how many round trips a code-only deploy makes to a registry.
func TestLocalRegistryCountsRequests(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host, closeReg, count, err := serveCountingRegistry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeReg()
	if count() != 0 {
		t.Fatalf("count = %d before any request", count())
	}
	if _, err := seedStandInBase(host, "3.12", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if count() == 0 {
		t.Error("pushing the base made no counted request")
	}
}
