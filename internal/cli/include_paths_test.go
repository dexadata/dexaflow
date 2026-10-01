package cli

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestIncludePathsCopiesExtraPaths covers the first half of #1062:
// `include_paths` was declared, defaulted, documented, and read by nothing. A
// project could set it and believe files were being copied; the generated
// Dockerfile only ever COPYed the DAG source (plus dbt group directories).
//
// It is implemented as ADDITIVE — paths copied ALONGSIDE the DAG source, not an
// allowlist that replaces it. An allowlist would have to define precedence
// against exclude_paths and dag_source, and would silently shrink images for
// anyone who set the field expecting the documented "files copied into the
// image". Adding is the reading that cannot break a working build.
func TestIncludePathsCopiesExtraPaths(t *testing.T) {
	base := func() *domain.LeoflowConfig {
		c := &domain.LeoflowConfig{DagID: "d"}
		c.ApplyDefaults()
		return c
	}

	t.Run("declared paths are copied alongside the DAG source", func(t *testing.T) {
		cfg := base()
		cfg.IncludePaths = []string{"lib", "conf/settings.yaml"}
		df, err := generatedDockerfile(cfg, "dag.py")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"COPY dag.py /home/leoflow/dag.py",
			"COPY lib /home/leoflow/lib",
			"COPY conf/settings.yaml /home/leoflow/conf/settings.yaml",
		} {
			if !strings.Contains(df, want) {
				t.Errorf("missing %q in:\n%s", want, df)
			}
		}
	})

	t.Run("the default copies nothing extra", func(t *testing.T) {
		// Every existing project carries the default, so it must produce the
		// Dockerfile it produces today — byte for byte.
		cfg := base()
		withDefault, err := generatedDockerfile(cfg, "dag.py")
		if err != nil {
			t.Fatal(err)
		}
		cfg2 := base()
		cfg2.IncludePaths = nil
		withNone, err := generatedDockerfile(cfg2, "dag.py")
		if err != nil {
			t.Fatal(err)
		}
		if withDefault != withNone {
			t.Errorf("the default must not change the image:\n--- default ---\n%s\n--- none ---\n%s", withDefault, withNone)
		}
	})

	t.Run("a path escaping the build context is refused", func(t *testing.T) {
		// `COPY ../secrets` is not buildable (Docker refuses paths outside the
		// context), and an absolute path silently means something else. Refusing
		// at compile names the entry; failing at build names a Docker error.
		for _, bad := range []string{"../secrets", "/etc/passwd", "lib/../../etc"} {
			cfg := base()
			cfg.IncludePaths = []string{bad}
			if _, err := generatedDockerfile(cfg, "dag.py"); err == nil {
				t.Errorf("include_paths %q should be refused", bad)
			} else if !strings.Contains(err.Error(), bad) {
				t.Errorf("the refusal must name the entry; got %v", err)
			}
		}
	})

	t.Run("the DAG source is not copied twice", func(t *testing.T) {
		// Listing dag.py in include_paths is a natural mistake, and a duplicate
		// COPY of the same path is wasted layer work at best.
		cfg := base()
		cfg.IncludePaths = []string{"dag.py"}
		df, err := generatedDockerfile(cfg, "dag.py")
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(df, "COPY dag.py "); n != 1 {
			t.Errorf("dag.py copied %d times, want 1:\n%s", n, df)
		}
	})
}

// TestIncludePathsAreScannedForSecrets pins the mirror. copiedRoots feeds the
// build's secret warning, and it is only useful while it agrees with what the
// generated Dockerfile actually COPYs. A path that ships but is not scanned is
// where a credential leaves unnoticed — the precise failure that warning exists
// to prevent.
func TestIncludePathsAreScannedForSecrets(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.IncludePaths = []string{"conf"}
	roots := copiedRoots(cfg)
	found := false
	for _, r := range roots {
		if r == "conf" {
			found = true
		}
	}
	if !found {
		t.Errorf("copiedRoots = %v, want it to include the include_paths entry — otherwise a credential under conf/ ships unscanned", roots)
	}
}
