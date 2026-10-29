package cli

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildxBuilder stands in for docker with buildx: `buildx version` succeeds,
// and a `buildx build --push --metadata-file F` writes digest to F as
// containerimage.digest, which is how buildx reports what it pushed.
func buildxBuilder(t *testing.T, digest string) (builder, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	builder = filepath.Join(dir, "docker")
	log = filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2\" = \"buildx version\" ]; then exit 0; fi\n" +
		"echo \"$*\" >> \"" + log + "\"\n" +
		"prev=\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$prev\" = --metadata-file ]; then printf '{\"containerimage.digest\":\"" + digest + "\"}' > \"$a\"; fi\n" +
		"  prev=\"$a\"\n" +
		"done\n" +
		"exit 0\n"
	writeExecutable(t, builder, script)
	return builder, log
}

// The deploy registers the digest docker buildx reported for its own push,
// not what the registry holds under the tag: there the tag names another
// image, as it would after a second push of the same tag landed between the
// push and a read (#1885).
func TestDeployRegistersTheDigestBuildxReported(t *testing.T) {
	dir, reg := platformProjectWith(t, amd64AndArm64, "build:\n  platforms: [linux/amd64]\n")
	reported := "sha256:" + strings.Repeat("ab", 32)
	builder, log := buildxBuilder(t, reported)
	fake := &deployServer{}

	out, _, err := deployDir(t, fake, dir, builder, "--rebuild")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	calls := argvLines(t, log)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "buildx build --platform linux/amd64 --push --metadata-file ") {
		t.Fatalf("builder calls = %q, want one buildx build that pushes", calls)
	}
	if fake.registers != 1 || !strings.Contains(out, "/team/etl@"+reported) {
		t.Errorf("registers=%d, want the reported digest %s registered:\n%s", fake.registers, reported, out)
	}
	if strings.Contains(out, reg.digest) {
		t.Errorf("the deploy registered what the registry holds at the tag, %s:\n%s", reg.digest, out)
	}
}
