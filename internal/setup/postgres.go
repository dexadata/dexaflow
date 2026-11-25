package setup

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Pinned relocatable PostgreSQL build (theseus-rs/postgresql-binaries). These are
// the datastore Dexaflow Lite manages itself so it needs no Docker (Fase 2). The
// major is pinned to 16 to match the typical managed Postgres in Production.
// Bumping the version means refreshing every SHA-256 below from the release's
// per-asset .sha256 files.
//
// Two builds are pinned for glibc Linux. pgVersion links libxml2, Kerberos,
// OpenSSL 3, zstd and lz4 from the host and runs on glibc 2.34 and later.
// pgSelfContainedVersion carries all of those (and ICU) under its own lib/, so
// it needs nothing from the host but glibc, and it needs glibc 2.36: its
// bundled libstdc++ does. A host from glibc 2.36 on gets the self-contained
// build. That covers Ubuntu 26.04, whose libxml2 moved to the libxml2.so.16
// soname and no longer ships the libxml2.so.2 the linked build needs (#2540),
// and lets a bare Debian 12 or Ubuntu 24.04 start without installing a
// package. Ubuntu 22.04 (glibc 2.35) and RHEL 9 (2.34) keep the linked build.
const (
	pgVersion              = "16.13.0"
	pgSelfContainedVersion = "16.16.0"
	pgBaseURL              = "https://github.com/theseus-rs/postgresql-binaries/releases/download"
)

// pgSelfContainedMinGlibc is the oldest glibc (major, minor) the self-contained
// build runs on.
var pgSelfContainedMinGlibc = [2]int{2, 36}

// PostgresBuild is a relocatable PostgreSQL release asset and its checksum.
type PostgresBuild struct {
	URL     string
	SHA256  string
	Version string
}

// pgTriple maps a (GOOS, GOARCH, libc) host to the theseus-rs target triple. libc
// is honored only on linux; "" defaults to glibc. Mirrors pythonTriple, since both
// projects use Rust target-triple naming.
func pgTriple(goos, goarch, libc string) (string, error) {
	switch goos {
	case "darwin":
		switch goarch {
		case "arm64":
			return "aarch64-apple-darwin", nil
		case "amd64":
			return "x86_64-apple-darwin", nil
		}
	case "linux":
		suffix := "gnu"
		if libc == "musl" {
			suffix = "musl"
		}
		switch goarch {
		case "amd64":
			return "x86_64-unknown-linux-" + suffix, nil
		case "arm64":
			return "aarch64-unknown-linux-" + suffix, nil
		}
	}
	return "", fmt.Errorf("no relocatable PostgreSQL build for %s/%s (libc %q)", goos, goarch, libc)
}

// postgresSHA256 holds the pinned checksum for each supported triple (PG 16.13.0).
var postgresSHA256 = map[string]string{
	"aarch64-apple-darwin":       "770cb9985aa903a8207ec98a3dfe762bf40234486ee5950c3809ddf6b1c9f921",
	"x86_64-apple-darwin":        "cff3bdaae703162b36c7622d5a30486df498ad72c2b0dc4a62173ec0d17df29a",
	"x86_64-unknown-linux-gnu":   "bb8dba9ca0f04e5cc6e21a13a41a3f111e030ef6c3b41a872e32c6f9e5a39331",
	"aarch64-unknown-linux-gnu":  "d4fe7f1fd8762d837d4091438fb139321ea7cb11abce95f51616ae1434fffd77",
	"x86_64-unknown-linux-musl":  "7a2cfa7515765933d6b8c112f9dd68890147af6631fbc1dc5cabca7416ecd363",
	"aarch64-unknown-linux-musl": "f6348a9bf53b84d5995336f4a9f059fec2ae9cf2dc6ad0e800c8c0d0d20172d9",
}

// postgresSelfContainedSHA256 holds the checksum of the self-contained build
// (PG 16.16.0) for each glibc triple it is used on.
var postgresSelfContainedSHA256 = map[string]string{
	"x86_64-unknown-linux-gnu":  "f668fbc685cdcb71d6a82f57035bed7d97b059e2d8a1d3adf317ec36c5e861d2",
	"aarch64-unknown-linux-gnu": "05f13472e1da9df11e801fc7b1ad92408ba02fb6af7f7cbaffd346934a5e5b0a",
}

// ResolvePostgres returns the pinned relocatable PostgreSQL asset for the host, or
// an error if the platform is unsupported (e.g. Windows, where WSL2 is the path).
// glibc is the host's glibc version ("2.39"), or "" when unknown; it selects the
// self-contained build on glibc Linux from pgSelfContainedMinGlibc on.
func ResolvePostgres(goos, goarch, libc, glibc string) (PostgresBuild, error) {
	triple, err := pgTriple(goos, goarch, libc)
	if err != nil {
		return PostgresBuild{}, err
	}
	version, sums := pgVersion, postgresSHA256
	if _, ok := postgresSelfContainedSHA256[triple]; ok && glibcAtLeast(glibc, pgSelfContainedMinGlibc) {
		version, sums = pgSelfContainedVersion, postgresSelfContainedSHA256
	}
	sha, ok := sums[triple]
	if !ok {
		return PostgresBuild{}, fmt.Errorf("no pinned checksum for triple %q", triple)
	}
	asset := fmt.Sprintf("postgresql-%s-%s.tar.gz", version, triple)
	return PostgresBuild{
		URL:     fmt.Sprintf("%s/%s/%s", pgBaseURL, version, asset),
		SHA256:  sha,
		Version: version,
	}, nil
}

// glibcAtLeast reports whether the glibc version v ("2.39", "2.36.1") is at
// least min. An empty or unparsable version is not: the linked build is the
// one that ran before the version was known.
func glibcAtLeast(v string, minVer [2]int) bool {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return major > minVer[0] || (major == minVer[0] && minor >= minVer[1])
}

// HostGlibcVersion returns the version of the glibc this host runs ("2.39"),
// or "" when it cannot tell (musl, macOS, no getconf). Go links no libc here,
// so it asks getconf, which every glibc distribution ships with the library.
func HostGlibcVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "getconf", "GNU_LIBC_VERSION").Output()
	if err != nil {
		return ""
	}
	return parseGlibcVersion(string(out))
}

// parseGlibcVersion reads getconf's "glibc 2.39" answer.
func parseGlibcVersion(s string) string {
	f := strings.Fields(s)
	if len(f) != 2 || f[0] != "glibc" {
		return ""
	}
	return f[1]
}
