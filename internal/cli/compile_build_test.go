package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestResolveBaseImageExplicitWins verifies an explicit base_image in the config
// is used verbatim, overriding the python_version-derived default.
func TestResolveBaseImageExplicitWins(t *testing.T) {
	cfg := &domain.LeoflowConfig{BaseImage: "ghcr.io/acme/base:custom", PythonVersion: "3.12"}
	if got := resolveBaseImage(cfg); got != "ghcr.io/acme/base:custom" {
		t.Errorf("resolveBaseImage() = %q, want the explicit base_image", got)
	}
}

// TestResolveBaseImageDefaultsToPublishedBase verifies the base image defaults to
// the PUBLISHED runtime base (ghcr.io/dexadata/leoflow-runtime:py<version>) when
// base_image is unset, so a yaml-driven build produces an image that builds
// anywhere — no locally-built leoflow-base needed.
func TestResolveBaseImageDefaultsToPublishedBase(t *testing.T) {
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11"}
	if got := resolveBaseImage(cfg); got != "ghcr.io/dexadata/leoflow-runtime:py3.11" {
		t.Errorf("resolveBaseImage() = %q, want ghcr.io/dexadata/leoflow-runtime:py3.11", got)
	}
}

// TestResolveBuildImageFlagWins verifies the --image flag overrides the registry
// config so a caller can always pin an explicit tag.
func TestResolveBuildImageFlagWins(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/acme", ImageName: "etl"}}
	if got := resolveBuildImage("myreg/explicit:v1", cfg, "abc123", ""); got != "myreg/explicit:v1" {
		t.Errorf("resolveBuildImage() = %q, want the explicit flag value", got)
	}
}

// TestResolveBuildImageFromRegistry verifies the image is derived from the
// registry block (url/image_name:version) when no --image flag is given.
func TestResolveBuildImageFromRegistry(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/acme", ImageName: "etl"}}
	if got := resolveBuildImage("", cfg, "v1.2.3", ""); got != "ghcr.io/acme/etl:v1.2.3" {
		t.Errorf("resolveBuildImage() = %q, want ghcr.io/acme/etl:v1.2.3", got)
	}
}

// TestResolveBuildImageEmptyWhenNothing verifies that with neither a flag nor a
// complete registry block the resolver returns "" so the caller can error.
func TestResolveBuildImageEmptyWhenNothing(t *testing.T) {
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/acme"}}
	if got := resolveBuildImage("", cfg, "v1", ""); got != "" {
		t.Errorf("resolveBuildImage() = %q, want empty (image_name missing)", got)
	}
}

// TestGeneratedDockerfileLayers verifies the generated Dockerfile layers in the
// canonical order: FROM base, system packages, pip dependencies (connectors
// expanded), then the DAG source COPY with PYTHONPATH.
func TestGeneratedDockerfileLayers(t *testing.T) {
	cfg := &domain.LeoflowConfig{
		PythonVersion:  "3.11",
		SystemPackages: []string{"git"},
		Connectors:     []string{"postgres"},
		Dependencies:   []string{"pandas==2.2.0"},
	}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	for _, want := range []string{
		"FROM ghcr.io/dexadata/leoflow-runtime:py3.11",
		"apt-get install",
		"git",
		"pip install",
		"apache-airflow-providers-postgres",
		"pandas==2.2.0",
		"COPY dag.py /home/leoflow/dag.py",
		"ENV PYTHONPATH=/home/leoflow",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("generatedDockerfile() missing %q in:\n%s", want, df)
		}
	}
}

// TestGeneratedDockerfileDbtCopiesProject verifies that for a dbt project the
// generated Dockerfile COPYs the dbt project directory (dbt_project.yml + models/
// + baked manifest.json) to the workdir instead of the nonexistent dag.py, and
// does not set a PYTHONPATH pointing at a Python module that dbt does not ship
// (#769). The dbt task runs `dbt --project-dir <project>` from WORKDIR
// /home/leoflow, so the project must land at /home/leoflow/<project>.
func TestGeneratedDockerfileDbtCopiesProject(t *testing.T) {
	cfg := &domain.LeoflowConfig{
		PythonVersion: "3.11",
		Dependencies:  []string{"dbt-duckdb==1.8.0"},
		Dbt:           &domain.DbtConfig{Project: "analytics"},
	}
	// A dbt-only project defaults DagSource to dag.py, which does not exist —
	// the pre-fix COPY dag.py is what fails the build.
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	if !strings.Contains(df, "COPY analytics /home/leoflow/analytics") {
		t.Errorf("generatedDockerfile() should COPY the dbt project dir, got:\n%s", df)
	}
	if strings.Contains(df, "dag.py") {
		t.Errorf("generatedDockerfile() must not reference dag.py for a dbt project:\n%s", df)
	}
	if strings.Contains(df, "PYTHONPATH") {
		t.Errorf("generatedDockerfile() must not set PYTHONPATH for a dbt project:\n%s", df)
	}
	// The dependency layer (dbt adapter) is still emitted before the COPY.
	if !strings.Contains(df, "pip install") || !strings.Contains(df, "dbt-duckdb==1.8.0") {
		t.Errorf("generatedDockerfile() should keep the pip dependency layer:\n%s", df)
	}
}

// TestGeneratedDockerfileDbtProjectPathCleaned verifies a project declared with a
// leading ./ lands at a clean /home/leoflow/<project> path matching the baked
// --project-dir, so `dbt --project-dir ./transform` resolves inside the image.
func TestGeneratedDockerfileDbtProjectPathCleaned(t *testing.T) {
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11", Dbt: &domain.DbtConfig{Project: "./transform"}}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	if !strings.Contains(df, "COPY transform /home/leoflow/transform") {
		t.Errorf("generatedDockerfile() should COPY the cleaned project path, got:\n%s", df)
	}
}

// TestEnsureDockerfileDbtUsesExisting verifies a dbt project that ships its own
// Dockerfile still wins — ensureDockerfile honors it verbatim and never generates.
func TestEnsureDockerfileDbtUsesExisting(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(existing, []byte("FROM ghcr.io/acme/dbt:custom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11", Dbt: &domain.DbtConfig{Project: "analytics"}}
	path, cleanup, err := ensureDockerfile(dir, "Dockerfile", cfg, "dag.py")
	if err != nil {
		t.Fatalf("ensureDockerfile() error = %v", err)
	}
	defer cleanup()
	if path != existing {
		t.Errorf("path = %q, want the existing Dockerfile %q", path, existing)
	}
}

// TestGeneratedDockerfileUnknownConnectorErrors verifies a typo'd connector name
// fails generation loudly (it cannot resolve to a pip package) rather than
// silently producing an image that ModuleNotFoundErrors at run time.
func TestGeneratedDockerfileUnknownConnectorErrors(t *testing.T) {
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11", Connectors: []string{"nope_not_a_connector"}}
	if _, err := generatedDockerfile(cfg, "dag.py"); err == nil {
		t.Fatal("generatedDockerfile() expected an error for an unknown connector, got nil")
	}
}

// TestEnsureDockerfileUsesExisting verifies a project-supplied Dockerfile is used
// as-is (no generation) and is not removed by the cleanup.
func TestEnsureDockerfileUsesExisting(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(existing, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11"}
	path, cleanup, err := ensureDockerfile(dir, "Dockerfile", cfg, "dag.py")
	if err != nil {
		t.Fatalf("ensureDockerfile() error = %v", err)
	}
	defer cleanup()
	if path != existing {
		t.Errorf("path = %q, want the existing Dockerfile %q", path, existing)
	}
	cleanup()
	if _, statErr := os.Stat(existing); statErr != nil {
		t.Errorf("cleanup removed the user's Dockerfile: %v", statErr)
	}
}

// TestEnsureDockerfileGeneratesWhenAbsent verifies that with no Dockerfile present
// one is generated from the config, and the cleanup removes the generated file.
func TestEnsureDockerfileGeneratesWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11", Dependencies: []string{"pandas==2.2.0"}}
	path, cleanup, err := ensureDockerfile(dir, "Dockerfile", cfg, "dag.py")
	if err != nil {
		t.Fatalf("ensureDockerfile() error = %v", err)
	}
	data, readErr := os.ReadFile(path) //nolint:gosec // test reads a path it just created
	if readErr != nil {
		t.Fatalf("reading generated Dockerfile: %v", readErr)
	}
	if !strings.Contains(string(data), "FROM ghcr.io/dexadata/leoflow-runtime:py3.11") {
		t.Errorf("generated Dockerfile missing FROM line:\n%s", data)
	}
	cleanup()
	if _, statErr := os.Stat(path); statErr == nil {
		t.Errorf("cleanup did not remove the generated Dockerfile %q", path)
	}
}

// The synthesized Dockerfile must install deps as root (so console scripts like
// dbt land on PATH, not ~/.local/bin) and drop back to the non-root runtime USER,
// keeping the COPYed project root-owned/read-only (#852).
func TestGeneratedDockerfileInstallsAsRoot(t *testing.T) {
	cfg := &domain.LeoflowConfig{
		PythonVersion: "3.11",
		Dependencies:  []string{"dbt-postgres==1.9.0"},
		Dbt:           &domain.DbtConfig{Project: "analytics"},
	}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	// USER root must precede the pip install, and USER 65532 must be the last USER.
	iRoot := strings.Index(df, "USER root")
	iPip := strings.Index(df, "pip install")
	iNonRoot := strings.LastIndex(df, "USER 65532:65532")
	if iRoot < 0 || iPip < 0 || iNonRoot < 0 {
		t.Fatalf("expected USER root, pip install, and a trailing USER 65532:65532 in:\n%s", df)
	}
	if iRoot >= iPip || iPip >= iNonRoot {
		t.Errorf("order must be USER root -> pip install -> USER 65532:65532, got:\n%s", df)
	}
	// The runtime USER must be non-root (PSA runAsNonRoot): no USER directive after it.
	if strings.Contains(df[iNonRoot+len("USER 65532:65532"):], "USER ") {
		t.Errorf("a USER directive follows the final non-root USER:\n%s", df)
	}
	// The project stays read-only — no chown makes it task-writable.
	if strings.Contains(df, "chown") {
		t.Errorf("project must stay read-only (no chown); got:\n%s", df)
	}
}

// A pure-Python DAG with NO deps needs no root switch (nothing to install) and
// stays on the base's non-root USER.
func TestGeneratedDockerfileNoDepsNoRootSwitch(t *testing.T) {
	cfg := &domain.LeoflowConfig{PythonVersion: "3.11"}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	if strings.Contains(df, "USER root") {
		t.Errorf("no deps → no root switch needed; got:\n%s", df)
	}
}

// TestGeneratedDockerfileMixedCopiesDagSourceAndEveryGroupProject pins the fix
// for #20. A hybrid DAG — a dag.py with dbt projects as task groups (ADR 0043),
// which is the authoring shape leoflow targets — must bake BOTH the DAG source
// and every group's project. generatedDockerfile branched on cfg.Dbt only and
// had no reference to DbtGroups at all, so a group's tasks ran
// `dbt --project-dir <project>` from WORKDIR /home/leoflow against a directory
// that was not in the image: every dbt task exited within seconds of pod start,
// after a green compile and a green Lite run (Lite reads from disk, so the gap
// only appears once there is an image).
func TestGeneratedDockerfileMixedCopiesDagSourceAndEveryGroupProject(t *testing.T) {
	cfg := &domain.LeoflowConfig{
		PythonVersion: "3.11",
		Dependencies:  []string{"dbt-postgres==1.9.0"},
		DbtGroups: map[string]*domain.DbtConfig{
			"transform": {Project: "./transform"},
			"marketing": {Project: "marketing"},
		},
	}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	for _, want := range []string{
		"COPY dag.py /home/leoflow/dag.py",
		"ENV PYTHONPATH=/home/leoflow",
		"COPY transform /home/leoflow/transform",
		"COPY marketing /home/leoflow/marketing",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("generatedDockerfile() missing %q, got:\n%s", want, df)
		}
	}
	// The USER drop must stay last, because the kubelet resolves the image's
	// final USER when a task pod sets runAsNonRoot with no runAsUser, and a root
	// image fails CreateContainerConfigError (#852). Ownership is not the reason
	// — COPY lands uid=0 gid=0 whatever USER is active, measured on a real build
	// under both BuildKit and the classic builder — but
	// the structure is still worth locking: a COPY emitted below the drop would
	// mean the drop is no longer last.
	drop := strings.LastIndex(df, "USER 65532:65532")
	if drop < 0 {
		t.Fatalf("generatedDockerfile() never drops back to the non-root user:\n%s", df)
	}
	for _, copyLine := range []string{"COPY dag.py", "COPY transform", "COPY marketing"} {
		if strings.Index(df, copyLine) > drop {
			t.Errorf("%q is emitted after the USER drop, so it would land task-owned:\n%s", copyLine, df)
		}
	}
}

// TestGeneratedDockerfileMixedGroupCopiesAreDeterministic guards ADR 0003's
// byte-for-byte reproducibility. DbtGroups is a map and Go randomizes map
// iteration, so emitting COPY lines in range order would produce a different
// Dockerfile per compile — a cache-thrashing, unreproducible image.
func TestGeneratedDockerfileMixedGroupCopiesAreDeterministic(t *testing.T) {
	cfg := &domain.LeoflowConfig{
		PythonVersion: "3.11",
		DbtGroups: map[string]*domain.DbtConfig{
			"zulu": {Project: "zulu"}, "alpha": {Project: "alpha"},
			"mike": {Project: "mike"}, "bravo": {Project: "bravo"},
		},
	}
	first, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	for i := range 100 {
		got, gerr := generatedDockerfile(cfg, "dag.py")
		if gerr != nil {
			t.Fatalf("generatedDockerfile() error = %v", gerr)
		}
		if got != first {
			t.Fatalf("generatedDockerfile() is not deterministic (run %d differs):\n%s\n---\n%s", i, first, got)
		}
	}
}

// TestGeneratedDockerfileMixedGroupProjectDotEmitsOneCopy covers project: ".".
// filepath.Clean(".") is ".", and internal/dbt/render.go omits --project-dir for
// that value, so dbt runs from WORKDIR and the project must land at
// /home/leoflow itself. That single COPY already carries dag.py, so emitting a
// separate one would be redundant.
func TestGeneratedDockerfileMixedGroupProjectDotEmitsOneCopy(t *testing.T) {
	cfg := &domain.LeoflowConfig{
		PythonVersion: "3.11",
		DbtGroups:     map[string]*domain.DbtConfig{"transform": {Project: "."}},
	}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	if n := strings.Count(df, "COPY "); n != 1 {
		t.Errorf("generatedDockerfile() emitted %d COPY lines, want exactly 1 for project \".\":\n%s", n, df)
	}
	if !strings.Contains(df, "COPY . /home/leoflow/") {
		t.Errorf("generatedDockerfile() should COPY the whole context for project \".\":\n%s", df)
	}
	if !strings.Contains(df, "ENV PYTHONPATH=/home/leoflow") {
		t.Errorf("generatedDockerfile() must still set PYTHONPATH — dag.py is imported per task:\n%s", df)
	}
}

// TestGeneratedDockerfileMixedDeduplicatesSharedProjectDir: two groups may point
// at the same directory (different granularity or selectors over one project).
// A duplicate COPY is a wasted layer and a diff that looks like a bug.
func TestGeneratedDockerfileMixedDeduplicatesSharedProjectDir(t *testing.T) {
	cfg := &domain.LeoflowConfig{
		PythonVersion: "3.11",
		DbtGroups: map[string]*domain.DbtConfig{
			"staging": {Project: "./warehouse"},
			"marts":   {Project: "warehouse"},
		},
	}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile() error = %v", err)
	}
	if n := strings.Count(df, "COPY warehouse /home/leoflow/warehouse"); n != 1 {
		t.Errorf("generatedDockerfile() emitted the shared project COPY %d times, want 1:\n%s", n, df)
	}
}

// TestBuildAndDeployAgreeOnTheImageTag is the regression test for #1227.
//
// The build tagged with the DAG version unconditionally and never consulted
// tag_strategy; deploy honored it. With `tag_strategy: git_sha` the build
// pushed <image>:<version> and deploy went looking for <image>:<sha>, and the
// error named the missing image rather than the disagreement, so it reads as a
// failed push and sends the reader to check registry credentials.
//
// resolveImageTag was correct and had its own test the whole time. It just had
// one caller. So this asserts the two paths AGREE rather than asserting either
// one in isolation, which is the only assertion that would have caught it.
func TestBuildAndDeployAgreeOnTheImageTag(t *testing.T) {
	const (
		version = "gcpexp1"
		sha     = "e28574c"
	)
	for _, strategy := range []string{"git_sha", "git-sha", "version", "", "something-unknown"} {
		cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{
			URL: "us-central1-docker.pkg.dev/p/repo", ImageName: "gcp-probe", TagStrategy: strategy,
		}}
		built := resolveBuildImage("", cfg, version, sha)
		deployed := deployImageRef(cfg, version, sha)
		if built != deployed {
			t.Errorf("tag_strategy %q: build pushes %q and deploy looks for %q; one artifact, two names",
				strategy, built, deployed)
		}
	}

	// And the strategy must actually be honored, not merely agreed upon: two
	// paths that both ignore tag_strategy would satisfy the loop above.
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{
		URL: "r.io/p", ImageName: "img", TagStrategy: "git_sha",
	}}
	if got := resolveBuildImage("", cfg, version, sha); got != "r.io/p/img:"+sha {
		t.Errorf("with tag_strategy git_sha the build tagged %q, want the sha", got)
	}

	// An explicit --image still wins over everything, which is the escape hatch.
	if got := resolveBuildImage("pinned:1", cfg, version, sha); got != "pinned:1" {
		t.Errorf("--image was overridden by the tag strategy: %q", got)
	}
}
