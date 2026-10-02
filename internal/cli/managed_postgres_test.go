package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagedPGConfLinesQuotesDataDir: the socket/TCP settings go into
// postgresql.conf (not the space-split pg_ctl -o), so a data dir with spaces is
// single-quoted and parsed correctly; TCP is off and the socket lives in dataDir.
func TestManagedPGConfLinesQuotesDataDir(t *testing.T) {
	conf := managedPGConfLines("/home/jo doe/.leoflow/pgdata")
	for _, want := range []string{
		"listen_addresses = ''",
		"unix_socket_directories = '/home/jo doe/.leoflow/pgdata'",
		"port = 5432",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf missing %q:\n%s", want, conf)
		}
	}
}

// TestCheckSocketPathLen fails loud (clear message) when the Unix socket path
// would exceed the OS limit (~108), instead of letting Postgres emit a cryptic
// "socket path too long" — e.g. a deeply nested HOME (service accounts).
func TestCheckSocketPathLen(t *testing.T) {
	if err := checkSocketPathLen("/home/u/.leoflow/pgdata"); err != nil {
		t.Errorf("normal path should pass, got %v", err)
	}
	long := "/" + strings.Repeat("nested/", 20) + ".leoflow/pgdata"
	err := checkSocketPathLen(long)
	if err == nil || !strings.Contains(err.Error(), "socket path") {
		t.Errorf("a too-long path must fail with a clear 'socket path' error, got %v", err)
	}
}

// TestPGLocaleEnvForcesValidLocale guards the initdb locale regression: a macOS
// SSH session forwards LC_CTYPE=UTF-8, which is NOT a valid locale on Linux and
// made `initdb` fail ("invalid locale settings"), breaking the managed-PG default
// on a fresh `leoflow lite`. pgLocaleEnv must strip any inherited LANG/LC_* and
// force a deterministic, valid LANG=C / LC_ALL=C, while preserving other vars.
func TestPGLocaleEnvForcesValidLocale(t *testing.T) {
	got := pgLocaleEnv([]string{"PATH=/usr/bin", "LC_CTYPE=UTF-8", "LANG=C.UTF-8", "LC_ALL=", "HOME=/h"})
	has := func(s string) bool {
		for _, kv := range got {
			if kv == s {
				return true
			}
		}
		return false
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "LC_CTYPE=") || kv == "LANG=C.UTF-8" {
			t.Errorf("inherited locale var must be stripped, got %q", kv)
		}
	}
	if !has("LANG=C") || !has("LC_ALL=C") {
		t.Errorf("must force LANG=C and LC_ALL=C, got %v", got)
	}
	if !has("PATH=/usr/bin") || !has("HOME=/h") {
		t.Errorf("non-locale vars must be preserved, got %v", got)
	}
}

// TestManagedPGPaths pins the per-user managed-Postgres layout the rest of the
// lifecycle depends on: the bin dir and data dir both live under ~/.leoflow so
// root and an unprivileged user never share a cluster.
func TestManagedPGPaths(t *testing.T) {
	binDir, dataDir, err := managedPGPaths()
	if err != nil {
		t.Fatalf("managedPGPaths: %v", err)
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		t.Fatalf("UserHomeDir: %v", herr)
	}
	if want := filepath.Join(home, ".dexaflow", "postgres", "bin"); binDir != want {
		t.Errorf("binDir = %q, want %q", binDir, want)
	}
	if want := filepath.Join(home, ".dexaflow", "pgdata"); dataDir != want {
		t.Errorf("dataDir = %q, want %q", dataDir, want)
	}
}

// TestDetectLibcReturnsKnownValue asserts libc detection yields a value the
// relocatable-PostgreSQL resolver understands: "" on darwin, glibc/musl on linux.
func TestDetectLibcReturnsKnownValue(t *testing.T) {
	switch got := detectLibc(); got {
	case "", "glibc", "musl":
		// expected
	default:
		t.Errorf("detectLibc() = %q, want one of {\"\", \"glibc\", \"musl\"}", got)
	}
}
