package build

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sha256Digest is the only digest form a tool's report is trusted in.
var sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// pushDigestLine matches the line docker push ends with,
// `<tag>: digest: sha256:<hex> size: <n>`.
var pushDigestLine = regexp.MustCompile(`digest: (sha256:[0-9a-f]{64}) size:`)

// pushImage builds and pushes req.Image with the tool, each tool the way it
// pushes several platforms under one manifest list and reports the digest it
// pushed. It returns that digest, or "" when the tool reported none in a form
// to trust; the caller then reads the registry at the tag.
//
//   - docker with buildx: one `docker buildx build --push --metadata-file`;
//     the digest is containerimage.digest of the metadata file.
//   - docker without buildx, and any other tool, which is given docker's
//     argv: `<tool> build`, then `<tool> push`, whose last line names the
//     digest. Only a tool named docker is asked for buildx, so a wrapper
//     script keeps the two calls it was written for.
//   - podman: one platform is built with -t and pushed with --digestfile;
//     several are built into a manifest list (--manifest) and pushed with
//     `podman manifest push --all --digestfile`.
//   - nerdctl: `nerdctl build`, then `nerdctl push`, with --all-platforms for
//     several platforms. It reports no digest.
func (b LocalDaemon) pushImage(ctx context.Context, req Request) (string, error) {
	dir, err := os.MkdirTemp("", "dexaflow-push-")
	if err != nil {
		return "", fmt.Errorf("creating a directory for the push report: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // best-effort cleanup of a temporary directory
	report := filepath.Join(dir, "report")
	switch filepath.Base(b.tool()) {
	case "podman":
		return b.pushPodman(ctx, req, report)
	case "nerdctl":
		return "", b.buildThenPush(ctx, req, io.Discard, nerdctlPushArgs(req.Image, req.Platforms)...)
	}
	if filepath.Base(b.tool()) == "docker" && b.hasBuildx(ctx) {
		if err := b.runEnv(ctx, req.Stdout, req.Stderr, req.Env, buildxPushArgs(req, report)...); err != nil {
			return "", b.buildError(req, err)
		}
		return metadataDigest(report), nil
	}
	var out bytes.Buffer
	if err := b.buildThenPush(ctx, req, &out, "push", req.Image); err != nil {
		return "", err
	}
	return parsePushDigest(out.String()), nil
}

// buildThenPush runs the plain build, then the push with pushArgs, copying
// the push's stdout to capture as well as to req.Stdout.
func (b LocalDaemon) buildThenPush(ctx context.Context, req Request, capture io.Writer, pushArgs ...string) error {
	if err := b.runEnv(ctx, req.Stdout, req.Stderr, req.Env, buildArgs(req.Image, req.Dockerfile, req.ContextDir, req.Platforms, inputArgs(req)...)...); err != nil {
		return b.buildError(req, err)
	}
	if err := b.run(ctx, teeWriter(req.Stdout, capture), req.Stderr, pushArgs...); err != nil {
		return b.pushError(req, err)
	}
	return nil
}

// pushPodman builds and pushes with podman. Several platforms go into a
// manifest list named after the image, removed first: `podman build
// --manifest` adds to a list that already exists, so one left by an earlier
// build would push its old images too. The removal fails when there is no
// such list, which is the usual case and not an error.
func (b LocalDaemon) pushPodman(ctx context.Context, req Request, report string) (string, error) {
	if len(req.Platforms) < 2 {
		if err := b.buildThenPush(ctx, req, io.Discard, "push", "--digestfile", report, req.Image); err != nil {
			return "", err
		}
		return fileDigest(report), nil
	}
	_ = b.run(ctx, io.Discard, io.Discard, "manifest", "rm", req.Image) //nolint:errcheck // see above
	args := append([]string{"build", "--platform", strings.Join(req.Platforms, ",")}, inputArgs(req)...)
	args = append(args, "--manifest", req.Image, "-f", req.Dockerfile, req.ContextDir)
	if err := b.runEnv(ctx, req.Stdout, req.Stderr, req.Env, args...); err != nil {
		return "", b.buildError(req, err)
	}
	if err := b.run(ctx, req.Stdout, req.Stderr, "manifest", "push", "--all", "--digestfile", report, req.Image, "docker://"+req.Image); err != nil {
		return "", b.pushError(req, err)
	}
	return fileDigest(report), nil
}

// hasBuildx reports whether the docker CLI has the buildx plugin.
func (b LocalDaemon) hasBuildx(ctx context.Context) bool {
	return b.run(ctx, io.Discard, io.Discard, "buildx", "version") == nil
}

// buildxPushArgs assembles the argv of a build that buildx pushes itself and
// whose digest it writes to the metadata file, with the build args and
// secrets of req (inputArgs).
func buildxPushArgs(req Request, metadata string) []string {
	args := []string{"buildx", "build"}
	if len(req.Platforms) > 0 {
		args = append(args, "--platform", strings.Join(req.Platforms, ","))
	}
	args = append(args, inputArgs(req)...)
	return append(args, "--push", "--metadata-file", metadata, "-t", req.Image, "-f", req.Dockerfile, req.ContextDir)
}

// nerdctlPushArgs is the argv of a nerdctl push, of every platform the image
// holds when several were built: without --all-platforms nerdctl pushes only
// the host's.
func nerdctlPushArgs(image string, platforms []string) []string {
	if len(platforms) > 1 {
		return []string{"push", "--all-platforms", image}
	}
	return []string{"push", image}
}

// metadataDigest reads containerimage.digest from a buildx metadata file, or
// "" when the file holds none.
func metadataDigest(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a file this process created
	if err != nil {
		return ""
	}
	var meta struct {
		Digest string `json:"containerimage.digest"` //nolint:tagliatelle // the key buildx writes
	}
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	return trustedDigest(meta.Digest)
}

// fileDigest reads the digest podman writes to --digestfile, or "".
func fileDigest(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a file this process created
	if err != nil {
		return ""
	}
	return trustedDigest(strings.TrimSpace(string(data)))
}

// parsePushDigest returns the digest of the last digest line of a docker
// push output, or "".
func parsePushDigest(out string) string {
	m := pushDigestLine.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// trustedDigest returns d when it is a sha256 digest, "" otherwise.
func trustedDigest(d string) string {
	if sha256Digest.MatchString(d) {
		return d
	}
	return ""
}

// teeWriter writes to both w and capture; w may be nil.
func teeWriter(w, capture io.Writer) io.Writer {
	if w == nil {
		return capture
	}
	return io.MultiWriter(w, capture)
}

// notef writes a note to w, which may be nil.
func notef(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format, args...) //nolint:errcheck // a note is best-effort
}
