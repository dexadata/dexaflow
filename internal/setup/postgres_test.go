package setup

import (
	"strings"
	"testing"
)

// TestResolvePostgres covers the platform/libc → relocatable Postgres asset
// mapping (theseus-rs/postgresql-binaries), including the musl variants that let
// Lite run an own-managed Postgres without Docker on Alpine too.
func TestResolvePostgres(t *testing.T) {
	cases := []struct {
		goos, goarch, libc string
		wantTriple         string
	}{
		{"darwin", "arm64", "", "aarch64-apple-darwin"},
		{"darwin", "amd64", "", "x86_64-apple-darwin"},
		{"linux", "amd64", "", "x86_64-unknown-linux-gnu"},
		{"linux", "arm64", "", "aarch64-unknown-linux-gnu"},
		{"linux", "amd64", "musl", "x86_64-unknown-linux-musl"},
		{"linux", "arm64", "musl", "aarch64-unknown-linux-musl"},
	}
	for _, c := range cases {
		b, err := ResolvePostgres(c.goos, c.goarch, c.libc, "")
		if err != nil {
			t.Errorf("%s/%s libc=%q: unexpected error %v", c.goos, c.goarch, c.libc, err)
			continue
		}
		wantAsset := "postgresql-" + pgVersion + "-" + c.wantTriple + ".tar.gz"
		if got := b.URL; !strings.Contains(got, wantAsset) || !strings.Contains(got, pgBaseURL) {
			t.Errorf("%s/%s libc=%q: URL=%q, want it to contain %q", c.goos, c.goarch, c.libc, got, wantAsset)
		}
		if len(b.SHA256) != 64 {
			t.Errorf("%s/%s libc=%q: SHA256=%q, want a 64-hex digest", c.goos, c.goarch, c.libc, b.SHA256)
		}
		if b.Version != pgVersion {
			t.Errorf("version = %q, want %q", b.Version, pgVersion)
		}
	}

	// Windows is unsupported (WSL2 is the path), as is an unknown arch.
	if _, err := ResolvePostgres("windows", "amd64", "", "2.39"); err == nil {
		t.Error("windows should be unsupported")
	}
	if _, err := ResolvePostgres("linux", "riscv64", "", "2.39"); err == nil {
		t.Error("unknown arch should error")
	}
}

// TestResolvePostgresSelfContained covers the build choice by glibc version
// (#2540): from glibc 2.36 on, glibc Linux gets the build that carries its own
// libxml2, Kerberos and OpenSSL; older or unknown glibc, musl and macOS keep the
// linked build.
func TestResolvePostgresSelfContained(t *testing.T) {
	cases := []struct {
		goos, goarch, libc, glibc string
		want                      string
	}{
		{"linux", "amd64", "glibc", "2.43", pgSelfContainedVersion}, // Ubuntu 26.04
		{"linux", "amd64", "glibc", "2.39", pgSelfContainedVersion}, // Ubuntu 24.04
		{"linux", "arm64", "", "2.36", pgSelfContainedVersion},      // Debian 12
		{"linux", "amd64", "glibc", "3.0", pgSelfContainedVersion},
		{"linux", "amd64", "glibc", "2.35", pgVersion}, // Ubuntu 22.04
		{"linux", "amd64", "glibc", "2.34", pgVersion}, // RHEL 9
		{"linux", "amd64", "glibc", "", pgVersion},
		{"linux", "amd64", "glibc", "unknown", pgVersion},
		{"linux", "amd64", "musl", "2.39", pgVersion},
		{"darwin", "arm64", "", "2.39", pgVersion},
	}
	for _, c := range cases {
		b, err := ResolvePostgres(c.goos, c.goarch, c.libc, c.glibc)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		if b.Version != c.want {
			t.Errorf("%s/%s libc=%q glibc=%q: version %q, want %q", c.goos, c.goarch, c.libc, c.glibc, b.Version, c.want)
		}
		if !strings.Contains(b.URL, "/"+c.want+"/postgresql-"+c.want+"-") {
			t.Errorf("%+v: URL %q does not name version %s", c, b.URL, c.want)
		}
		if len(b.SHA256) != 64 {
			t.Errorf("%+v: SHA256 %q, want a 64-hex digest", c, b.SHA256)
		}
	}
}

func TestParseGlibcVersion(t *testing.T) {
	for in, want := range map[string]string{
		"glibc 2.39\n": "2.39",
		"glibc 2.43":   "2.43",
		"":             "",
		"musl 1.2.5":   "",
		"glibc":        "",
	} {
		if got := parseGlibcVersion(in); got != want {
			t.Errorf("parseGlibcVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGlibcAtLeast(t *testing.T) {
	for _, c := range []struct {
		v    string
		want bool
	}{
		{"2.36", true}, {"2.36.1", true}, {"2.40", true}, {"3.1", true},
		{"2.35", false}, {"1.99", false}, {"", false}, {"2", false}, {"x.36", false}, {"2.y", false},
	} {
		if got := glibcAtLeast(c.v, [2]int{2, 36}); got != c.want {
			t.Errorf("glibcAtLeast(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}
