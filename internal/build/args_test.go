package build

import (
	"strings"
	"testing"
)

func argIndex(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func TestBuildArgsIncludesPlatform(t *testing.T) {
	// The Mac-safe default: a single linux/amd64 platform must reach the builder
	// so a macOS/arm64 dev build runs on an amd64 cluster (ADR 0041).
	args := buildArgs("ghcr.io/org/etl:v1", "Dockerfile", ".", []string{"linux/amd64"})

	if args[0] != "build" {
		t.Errorf("args[0] = %q, want build", args[0])
	}
	i := argIndex(args, "--platform")
	if i < 0 || i+1 >= len(args) || args[i+1] != "linux/amd64" {
		t.Fatalf("want --platform linux/amd64 in %v", args)
	}
	// The image, dockerfile and context must still be present.
	if argIndex(args, "ghcr.io/org/etl:v1") < 0 || argIndex(args, "Dockerfile") < 0 {
		t.Errorf("args missing image/dockerfile: %v", args)
	}
}

func TestBuildArgsOmitsPlatformWhenEmpty(t *testing.T) {
	args := buildArgs("img", "Dockerfile", ".", nil)
	if argIndex(args, "--platform") != -1 {
		t.Errorf("expected no --platform when platforms empty: %v", args)
	}
}

func TestBuildArgsJoinsMultiplePlatforms(t *testing.T) {
	args := buildArgs("img", "Dockerfile", ".", []string{"linux/amd64", "linux/arm64"})
	i := argIndex(args, "--platform")
	if i < 0 || args[i+1] != "linux/amd64,linux/arm64" {
		t.Errorf("want joined platforms, got %v", args)
	}
	if !strings.HasPrefix(args[i+1], "linux/amd64") {
		t.Errorf("platform list order changed: %v", args)
	}
}

func TestInspectDigestArgs(t *testing.T) {
	args := inspectDigestArgs("ghcr.io/org/etl:v1")
	if args[0] != "inspect" {
		t.Errorf("args[0] = %q, want inspect", args[0])
	}
	if argIndex(args, "ghcr.io/org/etl:v1") < 0 {
		t.Errorf("image missing from args: %v", args)
	}
	// It must ask the builder for the repo digest, not the local image ID.
	if argIndex(args, "--format") < 0 {
		t.Errorf("want a --format flag selecting RepoDigests: %v", args)
	}
}

func TestParseDigestRefExtractsPinnedRef(t *testing.T) {
	got, err := parseDigestRef("ghcr.io/org/etl@sha256:deadbeefcafe\n")
	if err != nil {
		t.Fatalf("parseDigestRef: %v", err)
	}
	if got != "ghcr.io/org/etl@sha256:deadbeefcafe" {
		t.Errorf("ref = %q, want the trimmed repo@sha256 ref", got)
	}
}

func TestParseDigestRefRejectsEmpty(t *testing.T) {
	// An image with no RepoDigests (never pushed) yields an empty inspect result.
	if _, err := parseDigestRef("  \n"); err == nil {
		t.Error("expected an error when the image has no repo digest (not pushed)")
	}
}

func TestParseDigestRefRejectsUnpinned(t *testing.T) {
	if _, err := parseDigestRef("ghcr.io/org/etl:v1\n"); err == nil {
		t.Error("expected an error when the ref carries no @sha256 digest")
	}
}
