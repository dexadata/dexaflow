package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolvePython3 pins the unified interpreter precedence (#742): the managed
// pinned build wins; otherwise the first host python3.11/python3 that reports
// >= 3.11 is used; a present-but-unsupported interpreter is rejected with the
// `dexaflow setup` hint instead of being returned; and no interpreter at all is
// reported as ("", nil) so callers can decide whether that is fatal.
func TestResolvePython3(t *testing.T) {
	ctx := context.Background()
	notFound := func(string) (string, error) { return "", os.ErrNotExist }
	neverRun := func(context.Context, string) (int, int, error) {
		t.Helper()
		t.Error("runVersion called for a trusted/absent interpreter")
		return 0, 0, nil
	}

	t.Run("managed build is trusted and preferred without exec", func(t *testing.T) {
		managed := filepath.Join(t.TempDir(), "python3.11")
		if err := os.WriteFile(managed, []byte("#!/fake"), 0o700); err != nil {
			t.Fatal(err)
		}
		got, err := resolvePython3(ctx, managed,
			func(string) (string, error) { return "/usr/bin/python3.11", nil },
			neverRun)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != managed {
			t.Errorf("path = %q, want the managed build %q", got, managed)
		}
	})

	t.Run("present but unsupported host interpreter is rejected with setup hint", func(t *testing.T) {
		lookPath := func(name string) (string, error) {
			if name == "python3" {
				return "/usr/bin/python3", nil
			}
			return "", os.ErrNotExist
		}
		got, err := resolvePython3(ctx, "", lookPath,
			func(context.Context, string) (int, int, error) { return 3, 9, nil })
		if err == nil {
			t.Fatal("err = nil, want an unsupported-interpreter rejection")
		}
		if got != "" {
			t.Errorf("path = %q, want empty on rejection", got)
		}
		if !strings.Contains(err.Error(), "dexaflow setup") {
			t.Errorf("error %q, want an actionable `dexaflow setup` hint", err)
		}
	})

	t.Run("supported host interpreter is returned", func(t *testing.T) {
		lookPath := func(name string) (string, error) {
			if name == "python3.11" {
				return "/usr/bin/python3.11", nil
			}
			return "", os.ErrNotExist
		}
		got, err := resolvePython3(ctx, "", lookPath,
			func(context.Context, string) (int, int, error) { return 3, 11, nil })
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "/usr/bin/python3.11" {
			t.Errorf("path = %q, want the host python3.11", got)
		}
	})

	t.Run("newer host interpreter (3.12 as python3) is accepted", func(t *testing.T) {
		lookPath := func(name string) (string, error) {
			if name == "python3" {
				return "/usr/bin/python3", nil
			}
			return "", os.ErrNotExist
		}
		got, err := resolvePython3(ctx, "", lookPath,
			func(context.Context, string) (int, int, error) { return 3, 12, nil })
		if err != nil || got != "/usr/bin/python3" {
			t.Fatalf("got (%q, %v), want the host python3", got, err)
		}
	})

	t.Run("no interpreter at all is reported as empty, no error", func(t *testing.T) {
		got, err := resolvePython3(ctx, "", notFound, neverRun)
		if err != nil {
			t.Fatalf("err = %v, want nil when nothing is found", err)
		}
		if got != "" {
			t.Errorf("path = %q, want empty when no interpreter exists", got)
		}
	})
}

// TestParsePythonVersion covers extraction of major/minor from `--version` output.
func TestParsePythonVersion(t *testing.T) {
	cases := []struct {
		in           string
		major, minor int
		wantErr      bool
	}{
		{"Python 3.11.15\n", 3, 11, false},
		{"Python 3.9.18", 3, 9, false},
		{"Python 3.12.0rc1", 3, 12, false},
		{"garbage", 0, 0, true},
		{"", 0, 0, true},
	}
	for _, tc := range cases {
		maj, mnr, err := parsePythonVersion(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parsePythonVersion(%q): err = nil, want error", tc.in)
			}
			continue
		}
		if err != nil || maj != tc.major || mnr != tc.minor {
			t.Errorf("parsePythonVersion(%q) = (%d,%d,%v), want (%d,%d,nil)", tc.in, maj, mnr, err, tc.major, tc.minor)
		}
	}
}
