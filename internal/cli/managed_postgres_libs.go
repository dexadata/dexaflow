package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// The relocatable PostgreSQL bundles only its own libraries (libpq, ecpg) and
// links the rest from the host: libxml2, Kerberos, OpenSSL 3, zstd, lz4 and
// zlib on glibc, plus ICU on musl. The glibc build has no ICU of its own; the
// libicuuc an `ldd` shows there is the host libxml2's, so it always matches the
// distribution (#279). A bare container lacks some of these (ubuntu:24.04 has
// neither libxml2 nor Kerberos), so the preflight names the missing ones and
// the packages that carry them instead of a generic loader error.

// pgLibPackages maps a soname the managed Postgres needs to the package that
// ships it, per package manager. An empty name means "not packaged under one
// name there": the soname is still listed, just not in the install command.
var pgLibPackages = map[string]struct{ apt, apk string }{
	"libxml2.so.2":        {"libxml2", "libxml2"},
	"libgssapi_krb5.so.2": {"libgssapi-krb5-2", "krb5-libs"},
	"libssl.so.3":         {"libssl3", "libssl3"},
	"libcrypto.so.3":      {"libssl3", "libcrypto3"},
	"libzstd.so.1":        {"libzstd1", "zstd-libs"},
	"liblz4.so.1":         {"liblz4-1", "lz4-libs"},
	"libz.so.1":           {"zlib1g", "zlib"},
	"libreadline.so.8":    {"libreadline8", "readline"},
	"libxslt.so.1":        {"libxslt1.1", "libxslt"},
}

// loaderMissingRe matches the glibc and musl dynamic loader's report of the
// first library it could not find.
var loaderMissingRe = regexp.MustCompile(`(?:error while loading shared libraries|Error loading shared library):? ([^\s:]+)`)

// lddMissingRe matches one "libfoo.so.1 => not found" line of ldd's output.
var lddMissingRe = regexp.MustCompile(`(?m)^\s*(\S+)\s+=>\s+not found`)

// missingPGLibs lists the shared libraries the binary at path cannot load,
// sorted. ldd reports all of them at once; when it is absent or reports
// nothing, the loader's own message (stderr of the failed run) names the first.
func missingPGLibs(ctx context.Context, path, loaderStderr string) []string {
	seen := map[string]bool{}
	// ldd exits 0 on glibc even with libraries missing, and is absent on some
	// hosts, so its output is read whatever the exit status.
	out, _ := exec.CommandContext(ctx, "ldd", path).CombinedOutput() //nolint:errcheck,gosec // output is parsed either way; managed binary path
	for _, m := range lddMissingRe.FindAllStringSubmatch(string(out), -1) {
		seen[m[1]] = true
	}
	if len(seen) == 0 {
		for _, m := range loaderMissingRe.FindAllStringSubmatch(loaderStderr, -1) {
			seen[m[1]] = true
		}
	}
	libs := make([]string, 0, len(seen))
	for l := range seen {
		libs = append(libs, l)
	}
	sort.Strings(libs)
	return libs
}

// hostPackageManager names the package manager of the Linux distribution
// described by an /etc/os-release body: "apt", "apk", or "" when unknown.
func hostPackageManager(osRelease string) string {
	ids := ""
	for _, line := range strings.Split(osRelease, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && (k == "ID" || k == "ID_LIKE") {
			ids += " " + strings.Trim(v, `"'`)
		}
	}
	for _, id := range strings.Fields(ids) {
		switch id {
		case "debian", "ubuntu":
			return "apt"
		case "alpine":
			return "apk"
		}
	}
	return ""
}

// pgLibInstallCommand returns the command that installs the packages carrying
// libs with the given package manager, or "" when none of them is known there.
// sudo is prefixed unless the caller is root.
func pgLibInstallCommand(pm string, libs []string, root bool) string {
	var pkgs []string
	seen := map[string]bool{}
	for _, l := range libs {
		p := pgLibPackages[l]
		name := p.apt
		if pm == "apk" {
			name = p.apk
		}
		if name != "" && !seen[name] {
			seen[name] = true
			pkgs = append(pkgs, name)
		}
	}
	if len(pkgs) == 0 || pm == "" {
		return ""
	}
	cmd := "apt-get install -y " + strings.Join(pkgs, " ")
	if pm == "apk" {
		cmd = "apk add " + strings.Join(pkgs, " ")
	}
	if !root {
		cmd = "sudo " + cmd
	}
	return cmd
}

// managedPGLibsError is the preflight failure for a managed Postgres that
// cannot start on this host, naming what is missing and how to install it when
// that is known.
func managedPGLibsError(libs []string, pm string, root bool, cause error) error {
	if len(libs) == 0 {
		return fmt.Errorf("the managed Postgres can't run on this host: %w\n"+
			"  it links system libraries (libxml2, Kerberos, OpenSSL 3, zstd, lz4) that may be missing here.\n"+
			"  use `dexaflow lite --postgres docker` (works everywhere)", cause)
	}
	var b strings.Builder
	if cmd := pgLibInstallCommand(pm, libs, root); cmd != "" {
		fmt.Fprintf(&b, "\n  install them: %s", cmd)
	}
	for _, l := range libs {
		if strings.HasPrefix(l, "libicu") {
			b.WriteString("\n  the build needs exactly this ICU version; another ICU release does not satisfy it")
			break
		}
	}
	b.WriteString("\n  or use `dexaflow lite --postgres docker` (works everywhere)")
	return fmt.Errorf("the managed Postgres can't run on this host: system libraries are missing: %s: %w%s",
		strings.Join(libs, ", "), cause, b.String())
}

// readOSRelease returns /etc/os-release, or "" where there is none (macOS).
func readOSRelease() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	return string(b)
}
