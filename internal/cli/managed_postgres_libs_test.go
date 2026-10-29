package cli

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// fakeLdd puts an `ldd` on PATH that prints out, so missingPGLibs reads a
// known report instead of the host's.
func fakeLdd(t *testing.T, out string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script ldd stub")
	}
	dir := t.TempDir()
	// printf is a shell builtin: PATH holds only the stub, so no cat.
	script := "#!/bin/sh\nprintf '%s\\n' '" + out + "'\n"
	writeExecutable(t, filepath.Join(dir, "ldd"), script)
	t.Setenv("PATH", dir)
}

// TestMissingPGLibsReadsLdd: every "not found" line of ldd is reported, not
// only the first library the loader gave up on. This is a bare ubuntu:24.04
// running the glibc bundle (#279).
func TestMissingPGLibsReadsLdd(t *testing.T) {
	fakeLdd(t, "\tlinux-vdso.so.1 (0x00007ffd)\n"+
		"\tlibzstd.so.1 => /lib/x86_64-linux-gnu/libzstd.so.1 (0x00007f)\n"+
		"\tlibxml2.so.2 => not found\n"+
		"\tlibgssapi_krb5.so.2 => not found\n"+
		"\tlibc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x00007f)")
	got := missingPGLibs(context.Background(), "/x/bin/postgres", "")
	want := []string{"libgssapi_krb5.so.2", "libxml2.so.2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("missingPGLibs = %v, want %v", got, want)
	}
}

// TestMissingPGLibsFallsBackToLoaderMessage: with no ldd report, the loader's
// own message names the library, for glibc and musl alike.
func TestMissingPGLibsFallsBackToLoaderMessage(t *testing.T) {
	fakeLdd(t, "")
	for stderr, want := range map[string]string{
		"/x/bin/postgres: error while loading shared libraries: libxml2.so.2: cannot open shared object file: No such file or directory": "libxml2.so.2",
		"Error loading shared library libicuuc.so.74: No such file or directory (needed by /x/bin/postgres)":                             "libicuuc.so.74",
	} {
		got := missingPGLibs(context.Background(), "/x/bin/postgres", stderr)
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("missingPGLibs(%q) = %v, want [%s]", stderr, got, want)
		}
	}
	if got := missingPGLibs(context.Background(), "/x/bin/postgres", "Illegal instruction"); len(got) != 0 {
		t.Errorf("a failure that is not a missing library must name none, got %v", got)
	}
}

func TestHostPackageManager(t *testing.T) {
	for body, want := range map[string]string{
		"PRETTY_NAME=\"Ubuntu 24.04.5 LTS\"\nID=ubuntu\nID_LIKE=debian\n": "apt",
		"ID=debian\n": "apt",
		"ID=linuxmint\nID_LIKE=\"ubuntu debian\"\n": "apt",
		"ID=alpine\n": "apk",
		"ID=fedora\n": "",
		"":            "",
	} {
		if got := hostPackageManager(body); got != want {
			t.Errorf("hostPackageManager(%q) = %q, want %q", body, got, want)
		}
	}
}

// TestPGLibInstallCommand: the packages are named per manager, de-duplicated
// (libssl.so.3 and libcrypto.so.3 are one Debian package), and sudo is added
// only for a non-root user.
func TestPGLibInstallCommand(t *testing.T) {
	libs := []string{"libcrypto.so.3", "libgssapi_krb5.so.2", "libssl.so.3", "libxml2.so.2"}
	if got, want := pgLibInstallCommand("apt", libs, false), "sudo apt-get install -y libssl3 libgssapi-krb5-2 libxml2"; got != want {
		t.Errorf("apt = %q, want %q", got, want)
	}
	if got, want := pgLibInstallCommand("apk", libs, true), "apk add libcrypto3 krb5-libs libssl3 libxml2"; got != want {
		t.Errorf("apk = %q, want %q", got, want)
	}
	if got := pgLibInstallCommand("", libs, true); got != "" {
		t.Errorf("an unknown distribution gets no command, got %q", got)
	}
	if got := pgLibInstallCommand("apt", []string{"libunknown.so.9"}, true); got != "" {
		t.Errorf("an unmapped library gets no command, got %q", got)
	}
}

// TestManagedPGLibsError: the message names the missing libraries and the
// exact install command, no longer the ICU packages that never fixed a bare
// Ubuntu 24.04 (#279), and keeps the Docker fallback and the cause.
func TestManagedPGLibsError(t *testing.T) {
	cause := errors.New("exit status 127")
	err := managedPGLibsError([]string{"libgssapi_krb5.so.2", "libxml2.so.2"}, "apt", true, cause)
	msg := err.Error()
	for _, want := range []string{
		"system libraries are missing: libgssapi_krb5.so.2, libxml2.so.2",
		"install them: apt-get install -y libgssapi-krb5-2 libxml2",
		"--postgres docker",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "libicu") || strings.Contains(msg, "sudo") {
		t.Errorf("message must not suggest ICU packages or sudo for root:\n%s", msg)
	}
	if !errors.Is(err, cause) {
		t.Error("the cause must stay wrapped")
	}

	icu := managedPGLibsError([]string{"libicuuc.so.74"}, "apk", true, cause).Error()
	if !strings.Contains(icu, "exactly this ICU version") {
		t.Errorf("a missing ICU must say the version has to match:\n%s", icu)
	}

	generic := managedPGLibsError(nil, "", false, cause).Error()
	if !strings.Contains(generic, "--postgres docker") || !strings.Contains(generic, "exit status 127") {
		t.Errorf("with nothing identified the message still gives the cause and the fallback:\n%s", generic)
	}
}
