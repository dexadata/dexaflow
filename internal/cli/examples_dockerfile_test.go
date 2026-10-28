package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExamplesBuildFromGeneratedDockerfile drives the build's Dockerfile choice
// (ensureDockerfile, what `deploy` and `compile --build` call) on a copy of
// every example and checks that the image would come from dexaflow.yaml.
//
// The examples used to ship a Dockerfile `FROM leoflow-base:py3.11`, an image
// that only exists on a host that ran `dexaflow lite`. A shipped Dockerfile is
// used verbatim, so `dexaflow deploy examples/<x>` failed on every CI runner
// and cluster builder, and the provider packages that `connectors:` declares
// never reached the gcp examples' images (#1709). A Dockerfile back in an
// example fails here, as does an example whose yaml the generator refuses.
func TestExamplesBuildFromGeneratedDockerfile(t *testing.T) {
	t.Parallel()
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	werr := filepath.WalkDir(filepath.Join(repo, "examples"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "dexaflow.yaml" {
			dirs = append(dirs, filepath.Dir(p))
		}
		return nil
	})
	if werr != nil {
		t.Fatalf("walking examples: %v", werr)
	}
	if len(dirs) < 20 {
		t.Fatalf("found %d examples; the walk is not reaching examples/", len(dirs))
	}
	for _, src := range dirs {
		rel, _ := filepath.Rel(repo, src) //nolint:errcheck // src is under repo by construction
		t.Run(strings.ReplaceAll(filepath.ToSlash(rel), "/", "_"), func(t *testing.T) {
			checkExampleBuildsFromYAML(t, src)
		})
	}
}

// checkExampleBuildsFromYAML asserts that one example declares nothing that
// would take the build off the generated Dockerfile (build.dockerfile,
// base_image) and that the Dockerfile the build would use is the generated one,
// on the published base, installing every effective dependency.
func checkExampleBuildsFromYAML(t *testing.T, src string) {
	t.Helper()
	dir := copyExampleDir(t, src)
	cfg, cerr := loadProjectConfig(dir)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if cfg.Build != nil && cfg.Build.Dockerfile != "" {
		t.Fatalf("build.dockerfile is %q; examples build from the generated Dockerfile", cfg.Build.Dockerfile)
	}
	if cfg.BaseImage != "" {
		t.Fatalf("base_image is %q; examples build on the published runtime base", cfg.BaseImage)
	}
	cfg.ApplyDefaults()
	path, cleanup, derr := ensureDockerfile(dir, "Dockerfile", cfg, dagSourcePath(dir, cfg))
	if derr != nil {
		t.Fatalf("generating the Dockerfile: %v", derr)
	}
	defer cleanup()
	if filepath.Base(path) != generatedDockerfileName {
		t.Fatalf("the build would use %s verbatim instead of generating one from dexaflow.yaml", filepath.Base(path))
	}
	data, rerr := os.ReadFile(path) //nolint:gosec // the file ensureDockerfile just wrote
	if rerr != nil {
		t.Fatal(rerr)
	}
	df := string(data)
	if !strings.HasPrefix(df, "FROM "+publishedBaseRepo+":py"+cfg.PythonVersion) {
		t.Errorf("not built on the published base:\n%s", df)
	}
	deps, eerr := cfg.EffectiveDependencies()
	if eerr != nil {
		t.Fatal(eerr)
	}
	for _, dep := range deps {
		if !strings.Contains(df, shellQuote(dep)) {
			t.Errorf("dependency %q is not installed by:\n%s", dep, df)
		}
	}
}

// copyExampleDir copies the regular files of one example into a temporary
// directory, so generating a Dockerfile never writes into the source tree.
func copyExampleDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(src, e.Name())) //nolint:gosec // a file of this repository
		if rerr != nil {
			t.Fatal(rerr)
		}
		if werr := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	return dst
}
