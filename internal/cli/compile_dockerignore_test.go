package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

func writeCtx(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readIgnore(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

// TestExcludePathsReachTheBuildContext is the #995 regression. exclude_paths was
// in the schema, defaulted and documented as "skipped both in image build and
// workspace discovery" with zero consumers in build code, so with the mode-1
// default `project: "."` the single `COPY . /home/leoflow/` baked the whole
// context — a `.env`, a BYO `profiles.yml`, `.git`, and the generated Dockerfile
// the build had just written.
func TestExcludePathsReachTheBuildContext(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x", ".git/config": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ExcludePaths = []string{"secrets", "*.pem"}
	cfg.ApplyDefaults()

	cleanup, _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	got := readIgnore(t, dir)
	for _, want := range []string{"secrets", "*.pem", generatedDockerfileName} {
		if !strings.Contains(got, want) {
			t.Errorf("the .dockerignore does not carry %q; exclude_paths never reached the build\n%s", want, got)
		}
	}
	// The generated Dockerfile is written INTO the context by ensureDockerfile
	// and has no business inside the image it builds.
	cleanup()
	if readIgnore(t, dir) != "" {
		t.Error("the workspace was left dirty: a .dockerignore we created outlived the build")
	}
}

// TestUserDockerignoreIsMergedNotReplaced pins the trap that decided the design.
// BuildKit's `<dockerfile>.dockerignore` would need no cleanup, but where it is
// honored it REPLACES the context .dockerignore — measured: with a user file
// excluding big.bin and a per-Dockerfile file excluding .env, big.bin shipped.
// Closing one leak by reopening the author's own exclusions is not a fix.
func TestUserDockerignoreIsMergedNotReplaced(t *testing.T) {
	dir := writeCtx(t, map[string]string{".dockerignore": "big.bin\n!keep.me\n", "dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()

	cleanup, _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	got := readIgnore(t, dir)
	if !strings.Contains(got, "big.bin") || !strings.Contains(got, "!keep.me") {
		t.Errorf("the author's own rules were dropped:\n%s", got)
	}
	if !strings.Contains(got, ".git") {
		t.Errorf("our excludes were not added:\n%s", got)
	}
	// Ours last: later rules win in .dockerignore, and leoflow.yaml is the
	// authoritative statement of what may leave in the image.
	if strings.Index(got, "big.bin") > strings.Index(got, ".git") {
		t.Errorf("our block precedes the author's, so a stray !rule could defeat exclude_paths:\n%s", got)
	}

	cleanup()
	if restored := readIgnore(t, dir); restored != "big.bin\n!keep.me\n" {
		t.Errorf("the author's .dockerignore was not restored byte-for-byte, got:\n%q", restored)
	}
}

// TestDbtArtifactsAreScopedToTheirProject is the #1013 regression. The compile
// runs a host-side `dbt parse`, which writes into the project it parsed, and
// the wholesale COPY then bakes the result: `.user.yml` (dbt's anonymous-usage
// cookie, a stable UUID identifying the BUILD HOST, read by the in-pod dbt so
// every pod reports as that user) and `logs/dbt.log` (absolute host paths — the
// #993 class arriving through a door the entrypoint assertions do not watch).
//
// Scoped to the project directories rather than added to the defaults, because
// `logs` is an ordinary name a non-dbt project may want shipped.
func TestDbtArtifactsAreScopedToTheirProject(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.DbtGroups = map[string]*domain.DbtConfig{"a": {Project: "transform"}}
	cfg.ApplyDefaults()

	if _, _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	got := readIgnore(t, dir)
	for _, want := range []string{"transform/logs", "transform/.user.yml"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q — a dbt build artifact would be baked\n%s", want, got)
		}
	}
	if strings.Contains(got, "\nlogs\n") {
		t.Errorf("unscoped \"logs\" would exclude an unrelated directory in a non-dbt tree\n%s", got)
	}
}

// TestDbtInputsAreNeverExcluded is the regression for what the dbt e2e caught
// after the first version of this shipped. Three paths were excluded as
// "artifacts" that are each a deliberate INPUT in a configuration that exists:
//
//   - `target/` holds the manifest `dbt.manifest` points at, documented as "a
//     pre-built manifest.json (the Pro/CI baked path)". The e2e's own fixture
//     sets `manifest: target/manifest.json`.
//   - `dbt_packages/` is where `dbt deps` installs; resolving on the build host
//     and baking the result is reasonable and reproducible.
//   - `profiles.yml` is the BYO-profiles pattern — ship your own, point
//     DBT_PROFILES_DIR at it. The runtime generates one from a Leoflow
//     connection when it HAS one; the e2e has none.
//
// The claim that justified excluding profiles.yml ("the runtime always
// generates its own and never reads one from the project") was read off a
// single code path and was false for the configurations that exist. This test
// is here so the next person tempted by the same tidy-up finds out in seconds
// instead of in a k3d run.
func TestDbtInputsAreNeverExcluded(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.Dbt = &domain.DbtConfig{Project: ".", Manifest: "target/manifest.json"}
	cfg.ApplyDefaults()

	if _, _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	got := readIgnore(t, dir)
	for _, input := range []string{"target", "dbt_packages", "profiles.yml"} {
		for _, form := range []string{"\n" + input + "\n", "\n./" + input + "\n"} {
			if strings.Contains(got, form) {
				t.Errorf("%q is excluded, but it is a deliberate dbt input — this breaks the manifest, dbt deps or BYO profiles path\n%s", input, got)
			}
		}
	}
}

// TestByoProfilesIsWarnedNotDropped: profiles.yml is credential-shaped, so it
// gets the same treatment as .env — the author is told, not overruled.
func TestByoProfilesIsWarnedNotDropped(t *testing.T) {
	dir := writeCtx(t, map[string]string{"transform/profiles.yml": "password: s3cret", "dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.DbtGroups = map[string]*domain.DbtConfig{"a": {Project: "transform"}}
	cfg.ApplyDefaults()

	var out bytes.Buffer
	if _, _, err := ensureDockerignore(&out, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	// Found in the dbt project directory, not just the context root: that is
	// where profiles.yml lives, next to dbt_project.yml.
	if !strings.Contains(out.String(), "transform/profiles.yml") {
		t.Errorf("no warning for a BYO profiles.yml inside the dbt project, got %q", out.String())
	}
	if strings.Contains(readIgnore(t, dir), "transform/profiles.yml") {
		t.Error("the BYO profiles.yml was excluded; that breaks a project that points DBT_PROFILES_DIR at it")
	}
}

func TestNoDbtMeansNoDbtExcludes(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	if _, _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	if got := readIgnore(t, dir); strings.Contains(got, "dbt_packages") {
		t.Errorf("a project with no dbt got dbt excludes:\n%s", got)
	}
}

// TestSecretishFilesWarnRatherThanVanish. Silently dropping .env would break a
// DAG that calls load_dotenv() with a failure far from its cause; letting a
// password ship unnoticed is also wrong. The author is told in time to act.
func TestSecretishFilesWarnRatherThanVanish(t *testing.T) {
	dir := writeCtx(t, map[string]string{".env": "PASSWORD=hunter2", "dag.py": "x"})
	// A dbt project at "." is what makes the generated Dockerfile a wholesale
	// `COPY . /home/leoflow/`, which is when .env actually ships.
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.Dbt = &domain.DbtConfig{Project: "."}
	cfg.ApplyDefaults()

	var out bytes.Buffer
	if _, _, err := ensureDockerignore(&out, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), ".env") {
		t.Errorf("no warning for a .env that will be baked, got %q", out.String())
	}
	if strings.Contains(readIgnore(t, dir), "\n.env\n") {
		t.Error(".env was excluded silently; that breaks load_dotenv() far from its cause")
	}

	// And it stops once the author acts on it.
	cfg2 := &domain.LeoflowConfig{DagID: "d", Dbt: &domain.DbtConfig{Project: "."}}
	cfg2.ExcludePaths = []string{".env"}
	cfg2.ApplyDefaults()
	var out2 bytes.Buffer
	if _, _, err := ensureDockerignore(&out2, dir, cfg2, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2.String(), "warning") {
		t.Errorf("still warning after .env was added to exclude_paths: %q", out2.String())
	}
}

// TestNoWarningWhenNothingCopiesIt is the "cries wolf" regression. A plain
// dag.py project's generated Dockerfile is `COPY dag.py /home/leoflow/dag.py`
// — the .env never enters the image. Warning that it "will be baked" there is
// false, and a security warning that fires on the most common project shape
// trains people to ignore the one that is real.
func TestNoWarningWhenNothingCopiesIt(t *testing.T) {
	dir := writeCtx(t, map[string]string{".env": "PASSWORD=hunter2", "dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()

	var out bytes.Buffer
	if _, _, err := ensureDockerignore(&out, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("warned about a file no COPY reaches: %q", out.String())
	}
}

// TestAuthorsOwnExclusionSilencesTheWarning. Someone who excluded .env in their
// .dockerignore — the docker-native, obvious place — must not be told to go and
// duplicate it in leoflow.yaml.
func TestAuthorsOwnExclusionSilencesTheWarning(t *testing.T) {
	dir := writeCtx(t, map[string]string{
		".env": "PASSWORD=hunter2", "dag.py": "x", ".dockerignore": ".env\n",
	})
	cfg := &domain.LeoflowConfig{DagID: "d", Dbt: &domain.DbtConfig{Project: "."}}
	cfg.ApplyDefaults()

	var out bytes.Buffer
	if _, _, err := ensureDockerignore(&out, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), ".env") {
		t.Errorf("warned about a file the author had already excluded: %q", out.String())
	}
}

// TestBareNamesPruneNestedCopies is Blocker 2. `.dockerignore` is not
// `.gitignore`: a pattern with no slash matches ONLY at the context root.
// Measured on both builders — with `__pycache__` and `*.pyc` in the file,
// `pkg/__pycache__/a.pyc` shipped; with the `**/` forms it did not. Three of
// the five shipped defaults are names that overwhelmingly appear nested, so
// without the expansion the feature lands looking delivered and prunes almost
// nothing.
func TestBareNamesPruneNestedCopies(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()

	if _, _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	got := readIgnore(t, dir)
	for _, want := range []string{"**/__pycache__", "**/*.pyc", "**/.venv"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q — the default would prune only the context root\n%s", want, got)
		}
	}
}

// TestOurBlockBeatsAnEarlierNegation is the other half of Blocker 2, and it is
// what makes the documented promise true. Re-emitting a pattern that is already
// present does NOT override an earlier `!` exception — measured: with
// `secrets`, `!secrets/keep.pem`, `secrets`, the keep.pem shipped on both
// builders. Only the `p/**` form wins.
func TestOurBlockBeatsAnEarlierNegation(t *testing.T) {
	dir := writeCtx(t, map[string]string{
		"dag.py": "x", ".dockerignore": "secrets\n!secrets/keep.pem\n",
	})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ExcludePaths = []string{"secrets"}
	cfg.ApplyDefaults()

	if _, _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	got := readIgnore(t, dir)
	if !strings.Contains(got, "secrets/**") {
		t.Errorf("no `secrets/**` after the author's negation, so exclude_paths loses to it\n%s", got)
	}
	if strings.Index(got, "secrets/**") < strings.Index(got, "!secrets/keep.pem") {
		t.Errorf("our overriding form precedes the negation, so it does not win\n%s", got)
	}
}

// TestInterruptedBlockIsStrippedNotAdopted is Finding 4. There is no signal
// handling in the CLI, so Ctrl-C during a multi-minute `docker build` kills the
// process before any defer — that is THE interruption, not a rare one. Read
// back naively, our leftover block looks like the author's own file, and the
// next successful build "restores" it permanently: a stale, self-perpetuating,
// git-committable artifact headed "removed after the build" that no longer
// tracks leoflow.yaml.
func TestInterruptedBlockIsStrippedNotAdopted(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x", ".dockerignore": "big.bin\n"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()

	// Build one: interrupted — no cleanup runs.
	if _, _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	// Build two: completes.
	var out bytes.Buffer
	cleanup, _, err := ensureDockerignore(&out, dir, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "interrupted build") {
		t.Errorf("the leftover block was adopted silently, not reported: %q", out.String())
	}
	cleanup()

	if got := readIgnore(t, dir); got != "big.bin\n" {
		t.Errorf("the workspace was not returned to the author's own file, got:\n%q", got)
	}
}

// TestMergeIsIdempotentAcrossAnInterruptedBuild. A build killed before cleanup
// leaves the merged file behind — a superset of the author's, so it excludes
// more and never less, which is the right direction to fail in. The next build
// must not stack duplicate blocks onto it.
func TestMergeIsIdempotentAcrossAnInterruptedBuild(t *testing.T) {
	dir := writeCtx(t, map[string]string{".dockerignore": "big.bin\n", "dag.py": "x"})
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()

	for range 3 {
		if _, _, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg, false); err != nil {
			t.Fatal(err)
		}
	}
	got := readIgnore(t, dir)
	if n := strings.Count(got, dockerignoreHeader); n != 1 {
		t.Errorf("header appears %d times after three interrupted builds, want 1\n%s", n, got)
	}
	if n := strings.Count(got, "\n.git\n"); n != 1 {
		t.Errorf(".git appears %d times, want 1\n%s", n, got)
	}
}

// TestBuildActuallyGetsTheDockerignore binds the feature to a build.
//
// Every other test here calls ensureDockerignore directly, so deleting its only
// caller in buildAndPush left the whole suite green — restoring precisely the
// #995 condition this PR exists to end: declared, documented, and wired to
// nothing. For a change whose thesis is "a field nobody consumed", shipping the
// wiring untested is the same defect one level up.
//
// The builder is operator-configurable (ADR 0015: shelled out, no Docker SDK),
// so a script standing in for `docker` can record what the context looked like
// at the moment the build ran — which is the only moment that matters.
func TestBuildActuallyGetsTheDockerignore(t *testing.T) {
	dir := writeCtx(t, map[string]string{
		"dag.py": "print('x')", ".env": "PASSWORD=hunter2", ".git/config": "x",
	})
	probe := filepath.Join(t.TempDir(), "fake-builder")
	record := filepath.Join(t.TempDir(), "seen.txt")
	script := "#!/bin/sh\ncp \"" + dir + "/.dockerignore\" \"" + record + "\" 2>/dev/null\nexit 0\n"
	if err := os.WriteFile(probe, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cfg := &domain.LeoflowConfig{DagID: "d", Dbt: &domain.DbtConfig{Project: "."}}
	cfg.ApplyDefaults()
	opts := compileOptions{build: true, builder: probe}
	if err := buildAndPush(cmd, dir, opts, cfg, "img:t"); err != nil {
		t.Fatalf("buildAndPush: %v", err)
	}

	seen, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("no .dockerignore existed when the builder ran: %v", err)
	}
	for _, want := range []string{".git", "**/__pycache__", generatedDockerfileName} {
		if !strings.Contains(string(seen), want) {
			t.Errorf("the builder saw a .dockerignore without %q:\n%s", want, seen)
		}
	}
	// And the workspace is clean again afterwards.
	if _, statErr := os.Stat(filepath.Join(dir, ".dockerignore")); !os.IsNotExist(statErr) {
		t.Error("the .dockerignore outlived the build")
	}
}

// TestSecretDirectoriesAreReachable. The scan skipped anything IsDir(), which
// made the highest-value hits structurally unreachable: a whole .ssh or .aws
// copied into a project is a bigger leak than any single file.
func TestSecretDirectoriesAreReachable(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x", ".aws/credentials": "key", ".env.production": "P=1"})
	cfg := &domain.LeoflowConfig{DagID: "d", Dbt: &domain.DbtConfig{Project: "."}}
	cfg.ApplyDefaults()

	var out bytes.Buffer
	if _, _, err := ensureDockerignore(&out, dir, cfg, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".aws", ".env.production"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no warning for %q, got %q", want, out.String())
		}
	}
}

// TestFindingsComeBackForThePostBuildReminder. A warning printed before minutes
// of layer output is a warning nobody reads — the repo already learned this and
// shipped remindDeprecatedPythonAfterBuild for exactly this reason.
func TestFindingsComeBackForThePostBuildReminder(t *testing.T) {
	dir := writeCtx(t, map[string]string{"dag.py": "x", ".env": "P=1"})
	cfg := &domain.LeoflowConfig{DagID: "d", Dbt: &domain.DbtConfig{Project: "."}}
	cfg.ApplyDefaults()

	_, baked, err := ensureDockerignore(&bytes.Buffer{}, dir, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(baked) == 0 {
		t.Fatal("nothing came back, so the post-build reminder can never fire")
	}
	var out bytes.Buffer
	remindBakedSecretsAfterBuild(&out, baked)
	if !strings.Contains(out.String(), ".env") {
		t.Errorf("the reminder does not name the file: %q", out.String())
	}
	// And it stays silent when there is nothing to say.
	var quiet bytes.Buffer
	remindBakedSecretsAfterBuild(&quiet, nil)
	if quiet.Len() != 0 {
		t.Errorf("reminded with no findings: %q", quiet.String())
	}
}
