package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/dbt"
	"github.com/neochaotic/leoflow/internal/domain"
)

// compileOptions holds the resolved flags for a compile run.
type compileOptions struct {
	output     string
	image      string
	parserCmd  string
	dagVersion string
	builder    string
	dockerfile string
	build      bool
	push       bool
	// local says the compiled DAG will run under a SUBPROCESS executor on this
	// host — Lite — rather than in a pod. It decides whether dbt's --project-dir
	// is baked as an absolute workspace path or as the relative path inside the
	// image, and which dbt binary parses the manifest.
	//
	// It is set explicitly rather than derived from `build`, which was the #993
	// bug: "we did not build an image this invocation" is a different question.
	// `compile` without --build and `deploy --skip-build` both target an image
	// that exists or will be built elsewhere, and both used to bake the
	// operator's own absolute path into every dbt task of a dag.json destined
	// for a cluster. The executor is chosen by server configuration
	// (LEOFLOW_EXECUTOR_TYPE), not by anything in the dag.json, so compile
	// cannot infer this — only Lite's subprocess mode knows.
	local bool
}

func newCompileCommand() *cobra.Command {
	var o compileOptions
	cmd := &cobra.Command{
		Use:   "compile [path]",
		Short: "Compile a DAG project into dag.json via the Python parser.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runCompile(cmd, dir, o)
		},
	}
	cmd.Flags().StringVarP(&o.output, "output", "o", "dag.json", "path to write the compiled dag.json")
	cmd.Flags().StringVar(&o.image, "image", "", "container image reference for the DAG")
	cmd.Flags().StringVar(&o.parserCmd, "parser-cmd", "", "override the parser command (default from config)")
	cmd.Flags().StringVar(&o.dagVersion, "dag-version", "", "DAG version label (default: git describe, else dev)")
	cmd.Flags().BoolVar(&o.build, "build", false, "build the DAG container image (requires --image)")
	cmd.Flags().BoolVar(&o.push, "push", false, "push the built image to its registry (requires --build)")
	cmd.Flags().StringVar(&o.builder, "builder", "docker", "image build tool to shell out to (e.g. docker, podman, nerdctl)")
	cmd.Flags().StringVar(&o.dockerfile, "dockerfile", "Dockerfile", "Dockerfile path relative to the DAG directory")
	return cmd
}

// checkProjectPreconditions runs the checks that apply to every project before
// the dag.py and dbt paths diverge: the leoflow.yaml must validate, it must not
// declare a dbt: block alongside a dag.py (#1015), and a deprecated Python line
// earns a warning the author can still act on.
//
// Grouped rather than inlined so every entry point gets all three by
// construction. warnDeprecatedPython landing on only the dag.py branch would
// leave every dbt project silently on a base image that stops being rebuilt,
// and `leoflow validate` open-coding the first two checks is how it spent this
// PR's first round never warning at all — validate is the sub-second command an
// author runs in a loop with leoflow.yaml open, which is the exact moment the
// warning is worth something.
func checkProjectPreconditions(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig) error {
	if verr := cfg.Validate(); verr != nil {
		return fmt.Errorf("invalid %s: %w", projectConfigPath(dir), verr)
	}
	if derr := errDbtBlockWithDagSource(dir, cfg); derr != nil {
		return derr
	}
	// Stderr, not stdout: the compile summary and any piped output stay clean.
	// Advisory only — a write failure here must not fail an otherwise good run.
	warnDeprecatedPython(cmd.ErrOrStderr(), cfg)
	return nil
}

// runCompile resolves the project config, runs the parser, validates the output,
// and optionally builds the DAG image.
func runCompile(cmd *cobra.Command, dir string, o compileOptions) error {
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		return err
	}
	if perr := checkProjectPreconditions(cmd, dir, cfg); perr != nil {
		return perr
	}
	// Self-heal the extracted parser sources before running the parser, so a binary
	// upgrade (new features like dbt vs a stale ~/.leoflow/pysrc) never surfaces as
	// a confusing "not supported" error (#239).
	ensurePysrc(cmd)
	if cfg.Dbt != nil {
		return runDbtCompile(cmd, dir, o, cfg)
	}
	command, err := resolveParserCommand(cmd, o.parserCmd)
	if err != nil {
		return err
	}
	if o.dagVersion == "" {
		o.dagVersion = gitVersion(cmdContext(cmd))
	}
	// The image is the --image flag when set, else derived from the leoflow.yaml
	// registry block (url/image_name:version), so a yaml-driven build needs no
	// flag. The resolved value flows into dag.json (via the parser) and the build,
	// keeping the registered artifact and the built/pushed image in lockstep.
	image := resolveBuildImage(o.image, cfg, o.dagVersion, gitSHA(cmdContext(cmd)))
	if ierr := checkImageFlags(cmd, o.build, o.push, image); ierr != nil {
		return ierr
	}
	if rerr := runParser(cmd, command, parserArgs{
		source:        dagSourcePath(dir, cfg),
		config:        projectConfigPath(dir),
		output:        o.output,
		image:         image,
		dagVersion:    o.dagVersion,
		projectConfig: cfg,
	}); rerr != nil {
		return rerr
	}
	if eerr := expandDbtGroupsInFile(cmd, dir, o.output, cfg, o.local); eerr != nil {
		return eerr
	}
	if oerr := overlayProject(o.output, cfg); oerr != nil {
		return oerr
	}
	if verr := validateDAGFile(o.output); verr != nil {
		return verr
	}
	if eerr := embedSource(o.output, dagSourcePath(dir, cfg)); eerr != nil {
		return eerr
	}
	if perr := validateOperatorProvidersFile(o.output, cfg); perr != nil {
		return perr
	}
	if berr := buildAndPush(cmd, dir, o, cfg, image); berr != nil {
		return berr
	}
	if o.build {
		remindDeprecatedPythonAfterBuild(cmd.ErrOrStderr(), cfg)
	}
	_, werr := fmt.Fprint(cmd.OutOrStdout(), compileSummary(dagSourcePath(dir, cfg), o.output, image, o.dagVersion))
	return werr
}

// runDbtCompile compiles a dbt project (ADR 0042) instead of a Python DAG: it
// acquires the manifest.json (a baked file or a fresh `dbt parse`), renders it
// into a dag.json via the dbt package, overlays the leoflow.yaml, validates, and
// optionally builds/pushes the image — reusing the same tail as the parser path.
func runDbtCompile(cmd *cobra.Command, dir string, o compileOptions, cfg *domain.LeoflowConfig) error {
	if o.dagVersion == "" {
		o.dagVersion = gitVersion(cmdContext(cmd))
	}
	image := resolveBuildImage(o.image, cfg, o.dagVersion, gitSHA(cmdContext(cmd)))
	if ierr := checkImageFlags(cmd, o.build, o.push, image); ierr != nil {
		return ierr
	}
	manifest, err := loadDbtManifest(cmd, dir, cfg.Dbt, o.local, cfg.DagID)
	if err != nil {
		return err
	}
	conn, profile, perr := dbtConnectionProfile(dir, cfg.Dbt)
	if perr != nil {
		return perr
	}
	// Lite (subprocess executor): --project-dir must be absolute, because the task
	// does not run from the project — it runs from the workspace root — and with no
	// managed connection each task gets the zero-config duckdb profile step, unless
	// the project ships its own profiles.yml (#575). o.local is set by `leoflow
	// dev`'s subprocess mode and by nothing else; deriving it from !o.build is what
	// #993 was.
	local := o.local
	spec, err := dbt.Compile(manifest, dbt.Meta{
		// Advisories reach stderr, not stdout: stdout carries the compiled
		// artifact path that scripts read.
		Warn: func(msg string) {
			//nolint:errcheck // a warning that cannot be delivered must not fail the compile
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg)
		},
		DagID:       cfg.DagID,
		DagVersion:  o.dagVersion,
		Image:       image,
		Owner:       cfg.Owner,
		Description: cfg.Description,
		Tags:        cfg.Tags,
		Schedule:    cfg.Dbt.Schedule,
		Granularity: dbt.Granularity(cfg.Dbt.Granularity),
		// The declared secret set. Without these the dbt-only path produced a
		// dag.json with no connections/variables, so ADR 0055 scoping had nothing
		// to scope: under enforce the pod got none of them; under the default
		// permissive it got the whole vault, minus anything that lives only in an
		// external backend, which is fetched by declared name (#997).
		Connections: cfg.Connections,
		Variables:   cfg.Variables,
		Connection:  conn,
		Profile:     profile,
		Schema:      cfg.Dbt.Schema,
		ProjectDir:  dbtProjectDir(dir, cfg.Dbt.Project, local),
		ProfilesDir: dbtProfilesDir(dir, cfg.Dbt.Project, local),
		Local:       local && !dbtProjectHasProfiles(filepath.Join(dir, cfg.Dbt.Project)),
	})
	if err != nil {
		return fmt.Errorf("dbt compile: %w", err)
	}
	if werr := writeDAGFile(o.output, &spec); werr != nil {
		return werr
	}
	if oerr := overlayProject(o.output, cfg); oerr != nil {
		return oerr
	}
	if verr := validateDAGFile(o.output); verr != nil {
		return verr
	}
	if berr := buildAndPush(cmd, dir, o, cfg, image); berr != nil {
		return berr
	}
	if o.build {
		remindDeprecatedPythonAfterBuild(cmd.ErrOrStderr(), cfg)
	}
	_, werr := fmt.Fprint(cmd.OutOrStdout(), compileSummary(filepath.Join(dir, cfg.Dbt.Project), o.output, image, o.dagVersion))
	return werr
}

// dbtProjectDir returns the dbt --project-dir to bake into task commands. For an
// image build (Pro) the project is baked at the relative path inside the image, so
// the relative value is kept. For a local/subprocess build (Lite) the task runs on
// the host from a per-task temp workdir, so it must be the ABSOLUTE workspace project
// path — otherwise `dbt --project-dir ./transform` fails ("does not exist").
func dbtProjectDir(dagDir, project string, local bool) string {
	if !local || project == "" {
		return project
	}
	if abs, err := filepath.Abs(filepath.Join(dagDir, project)); err == nil {
		return abs
	}
	return project
}

// expandDbtGroupsInFile expands any dbt_group placeholders in the compiled dag.json
// (ADR 0043): each is replaced by its dbt project's rendered tasks (namespaced) and
// the group's downstream is rewired onto the group's leaves. Runs after the parser
// and before validation, so the final dag.json carries no dbt_group type.
func expandDbtGroupsInFile(cmd *cobra.Command, dir, output string, cfg *domain.LeoflowConfig, local bool) error {
	data, err := os.ReadFile(output) //nolint:gosec // G304: operator-supplied output path.
	if err != nil {
		return fmt.Errorf("reading %s: %w", output, err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		return fmt.Errorf("parsing %s: %w", output, uerr)
	}
	hasGroup := false
	for _, t := range spec.Tasks {
		if t.Type == domain.TaskTypeDbtGroup {
			hasGroup = true
			break
		}
	}
	if !hasGroup {
		return nil // no placeholders to expand — leave the dag.json untouched
	}
	render := func(group string) ([]domain.TaskSpec, error) {
		gc, ok := cfg.DbtGroups[group]
		if !ok {
			return nil, fmt.Errorf("dag uses dbt_group(%q) but leoflow.yaml has no dbt_groups.%s", group, group)
		}
		manifest, merr := loadDbtManifest(cmd, dir, gc, local, cfg.DagID)
		if merr != nil {
			return nil, merr
		}
		conn, profile, perr := dbtConnectionProfile(dir, gc)
		if perr != nil {
			return nil, perr
		}
		return dbt.Render(manifest, dbt.Options{
			// Wired here too, not only on the dbt-only path: an embedded group
			// merges folders exactly the same way, and a warning that exists
			// but is never delivered on half the call sites is the defect it
			// was written to prevent. Named by group — a dag.py DAG can embed
			// several, and "group `staging`" alone would not say which.
			Warn: func(msg string) {
				//nolint:errcheck // a warning that cannot be delivered must not fail the compile
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: dbt_group(%s): %s\n", group, msg)
			},
			// The DAG's declared connections, so the managed one does not
			// shadow them here either. The parser emits the top-level
			// `connections:` into dag.json, and spec is that file — an embedded
			// group is the same rendering as a dbt-only project, so it loses
			// them the same way.
			DagConnections: spec.Connections,
			Granularity:    dbt.Granularity(gc.Granularity),
			Connection:     conn,
			Profile:        profile,
			Schema:         gc.Schema,
			ProjectDir:     dbtProjectDir(dir, gc.Project, local),
			ProfilesDir:    dbtProfilesDir(dir, gc.Project, local),
			// Auto-default duckdb (L4) only when the project has no profiles.yml of its
			// own — never override a warehouse the user configured.
			Local: local && !dbtProjectHasProfiles(filepath.Join(dir, gc.Project)),
		})
	}
	tasks, err := dbt.ExpandGroups(spec.Tasks, render)
	if err != nil {
		return err
	}
	spec.Tasks = tasks
	return writeDAGFile(output, &spec)
}

// dbtConnectionProfile resolves the managed connection id and the dbt profile name
// for a dbt config (ADR 0043 #2). With a connection, the profile name (from the
// project's dbt_project.yml) is required. With no connection it still returns the
// profile name so a Lite build can write a default duckdb profile under it (L4);
// a project without a readable `profile:` falls back to empty (the baked profiles.yml).
func dbtConnectionProfile(dir string, c *domain.DbtConfig) (conn, profile string, err error) {
	if c.Connection == "" {
		p, perr := dbtProfileName(filepath.Join(dir, c.Project))
		if perr != nil {
			return "", "", nil //nolint:nilerr // no readable profile: fall back to the baked profiles.yml, not an error
		}
		return "", p, nil
	}
	profile, err = dbtProfileName(filepath.Join(dir, c.Project))
	if err != nil {
		return "", "", err
	}
	return c.Connection, profile, nil
}

// dbtProfileName reads the `profile:` field from a project's dbt_project.yml — the
// key the generated profiles.yml must use.
func dbtProfileName(projectDir string) (string, error) {
	path := filepath.Join(projectDir, "dbt_project.yml")
	data, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied project path.
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var p struct {
		Profile string `yaml:"profile"`
	}
	if uerr := yaml.Unmarshal(data, &p); uerr != nil {
		return "", fmt.Errorf("parsing %s: %w", path, uerr)
	}
	if p.Profile == "" {
		return "", fmt.Errorf("%s has no `profile:` field", path)
	}
	return p.Profile, nil
}

// liteDbtBinAt returns the per-DAG venv's dbt under home when it exists, else "".
func liteDbtBinAt(home, dagID string) string {
	if home == "" || dagID == "" {
		return ""
	}
	cand := filepath.Join(home, ".leoflow", "dev", "venvs", dagID, "bin", "dbt")
	if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
		return cand
	}
	return ""
}

// dbtParseBinAt picks the dbt that parses the manifest: the DAG's own per-DAG
// venv dbt when this host has one, else whatever is on PATH.
//
// Deliberately NOT a function of where the DAG will run. Tying it to that was
// the #993 conflation one layer down — a venv dbt is that DAG's own, pinned to
// the adapter its leoflow.yaml declares, while PATH's is whatever the operator
// happens to have. When both exist the venv one is strictly the better parser,
// whichever executor the artifact ends up on.
func dbtParseBinAt(home, dagID string) string {
	if v := liteDbtBinAt(home, dagID); v != "" {
		return v
	}
	return "dbt"
}

// dbtParseBin is dbtParseBinAt against the real home.
func dbtParseBin(dagID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "dbt"
	}
	return dbtParseBinAt(home, dagID)
}

// writeParseDuckdbProfile writes a temporary default-duckdb profiles.yml for
// `dbt parse` when a Lite project has no connection and no profiles.yml — the
// compile-time half of the zero-config local warehouse (L4). Returns the temp
// profiles dir (the caller removes it), or "" when the project already carries a
// profiles.yml (respected) or its profile name can't be read.
// dbtProjectHasProfiles reports whether a dbt project ships its own profiles.yml —
// which the zero-config default duckdb (L4) must never override.
// dbtProfilesDir returns the --profiles-dir to bake, or "" to leave dbt to
// DBT_PROFILES_DIR. A project that ships its own profiles.yml has to be pointed
// at explicitly: the base image aims DBT_PROFILES_DIR at an ephemeral /tmp dir
// so dbt's writes stay off the read-only project (#852), which also means dbt
// never looks inside the project (#994). Same absolute-vs-relative rule as
// dbtProjectDir — the profiles live in the project directory, so it is the same
// path.
func dbtProfilesDir(dagDir, project string, local bool) string {
	if !dbtProjectHasProfiles(filepath.Join(dagDir, project)) {
		return ""
	}
	if d := dbtProjectDir(dagDir, project, local); d != "" {
		return d
	}
	return "."
}

func dbtProjectHasProfiles(projectDir string) bool {
	_, err := os.Stat(filepath.Join(projectDir, "profiles.yml"))
	return err == nil
}

func writeParseDuckdbProfile(dir string, c *domain.DbtConfig) string {
	projectDir := filepath.Join(dir, c.Project)
	if dbtProjectHasProfiles(projectDir) {
		return ""
	}
	profile, err := dbtProfileName(projectDir)
	if err != nil {
		return ""
	}
	tmp, err := os.MkdirTemp("", "leoflow-dbt-profiles-")
	if err != nil {
		return ""
	}
	db := filepath.Join(projectDir, "leoflow_local.duckdb")
	content := fmt.Sprintf(`{%q:{"target":"dev","outputs":{"dev":{"type":"duckdb","path":%q,"threads":4}}}}`, profile, db)
	if werr := os.WriteFile(filepath.Join(tmp, "profiles.yml"), []byte(content), 0o600); werr != nil {
		_ = os.RemoveAll(tmp) //nolint:errcheck // best-effort cleanup of a temp dir
		return ""
	}
	return tmp
}

// writeDAGFile marshals a DAGSpec to path as indented dag.json.
func writeDAGFile(path string, spec *domain.DAGSpec) error {
	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding dag.json: %w", err)
	}
	if werr := os.WriteFile(path, append(out, '\n'), 0o600); werr != nil {
		return fmt.Errorf("writing %s: %w", path, werr)
	}
	return nil
}

// buildAndPush optionally builds the DAG image and pushes it, honoring the
// --build/--push flags. When building and the project ships no Dockerfile, one is
// generated from the leoflow.yaml (base image + system packages + dependencies/
// connectors), built, and removed afterward so the workspace stays clean.
func buildAndPush(cmd *cobra.Command, dir string, o compileOptions, cfg *domain.LeoflowConfig, image string) error {
	if o.build {
		name := resolveDockerfileName(cmd, o, cfg)
		dockerfile, cleanup, derr := ensureDockerfile(dir, name, cfg, dagSourcePath(dir, cfg))
		if derr != nil {
			return derr
		}
		defer cleanup()
		// A project that ships its own Dockerfile gets the exclusions too, but
		// we cannot read its COPY lines — so the secret warning softens rather
		// than claiming what ships.
		ownDockerfile := dockerfile != filepath.Join(dir, generatedDockerfileName)
		// Applies whether or not the project ships its own Dockerfile: exclude_paths
		// is a statement about what may enter the image, and it does not become
		// less true because the author wrote the Dockerfile themselves.
		ignoreCleanup, baked, ierr := ensureDockerignore(cmd.ErrOrStderr(), dir, cfg, ownDockerfile)
		if ierr != nil {
			return ierr
		}
		defer ignoreCleanup()
		var platforms []string
		if cfg.Build != nil {
			platforms = cfg.Build.Platforms
		}
		if berr := buildImage(cmd, o.builder, image, dockerfile, dir, platforms); berr != nil {
			return berr
		}
		// After the build, where the eye lands: the warning above is minutes of
		// layer output away by now.
		remindBakedSecretsAfterBuild(cmd.ErrOrStderr(), baked)
	}
	if o.push {
		if perr := pushImage(cmd, o.builder, image); perr != nil {
			return perr
		}
	}
	return nil
}

// resolveDockerfileName picks the Dockerfile name to look for in the DAG
// directory: an explicit --dockerfile flag wins, then leoflow.yaml's
// build.dockerfile, else the "Dockerfile" default.
func resolveDockerfileName(cmd *cobra.Command, o compileOptions, cfg *domain.LeoflowConfig) string {
	if cmd.Flags().Changed("dockerfile") {
		return o.dockerfile
	}
	if cfg.Build != nil && cfg.Build.Dockerfile != "" {
		return cfg.Build.Dockerfile
	}
	return o.dockerfile
}

// compileSummary formats the success line for `leoflow compile`. When image is
// empty it renders `no image` instead of `image ` to avoid the dangling-comma
// artifact reported as #D10 in the dogfood audit (#212).
func compileSummary(src, out, image, version string) string {
	imageField := "image " + image
	if image == "" {
		imageField = "no image"
	}
	return fmt.Sprintf("Compiled %s -> %s (%s, version %s)\n", src, out, imageField, version)
}

// checkImageFlags enforces the --build/--image relationship and notes when an
// image is recorded without being built.
func checkImageFlags(cmd *cobra.Command, build, push bool, image string) error {
	if push && !build {
		return errors.New("--push requires --build")
	}
	if build && image == "" {
		return errors.New("--build needs an image reference: pass --image, or set registry.url + registry.image_name in leoflow.yaml")
	}
	if !build && image != "" {
		_, werr := fmt.Fprintf(cmd.ErrOrStderr(), "note: recording image %q without building it; pass --build to build the DAG image\n", image)
		return werr
	}
	return nil
}

// buildArgs assembles the builder argv for a DAG image build. When platforms is
// non-empty it injects `--platform a,b`, so a deploy can target the cluster's
// architecture instead of the host's — the Mac-safe default linux/amd64 (ADR
// 0041). A single platform works with a plain `docker build`; a multi-platform
// list is the caller's signal to route through buildx (the deploy command does).
func buildArgs(image, dockerfile, contextDir string, platforms []string) []string {
	args := []string{"build"}
	if len(platforms) > 0 {
		args = append(args, "--platform", strings.Join(platforms, ","))
	}
	return append(args, "-t", image, "-f", dockerfile, contextDir)
}

// buildImage shells out to the configured builder to build the DAG image
// out-of-process (ADR 0015: no Docker SDK in our binaries). The build context
// is the DAG directory; platforms selects the target architecture(s).
func buildImage(cmd *cobra.Command, builder, image, dockerfile, contextDir string, platforms []string) error {
	//nolint:gosec // G204: builder is operator-configured by design (ADR 0015).
	bc := exec.CommandContext(cmdContext(cmd), builder, buildArgs(image, dockerfile, contextDir, platforms)...)
	bc.Stdout = cmd.OutOrStdout()
	bc.Stderr = cmd.ErrOrStderr()
	if err := bc.Run(); err != nil {
		return fmt.Errorf("building image %q with %q: %w", image, builder, err)
	}
	return nil
}

// pushImage shells out to the configured builder to push the image to its
// registry (out-of-process; ADR 0015).
func pushImage(cmd *cobra.Command, builder, image string) error {
	//nolint:gosec // G204: builder is operator-configured by design (ADR 0015).
	pc := exec.CommandContext(cmdContext(cmd), builder, "push", image)
	pc.Stdout = cmd.OutOrStdout()
	pc.Stderr = cmd.ErrOrStderr()
	if err := pc.Run(); err != nil {
		return fmt.Errorf("pushing image %q with %q: %w", image, builder, err)
	}
	return nil
}

// parserArgs collects the inputs passed to the Python parser subprocess.
type parserArgs struct {
	source     string
	config     string
	output     string
	image      string
	dagVersion string
	// projectConfig is the parsed + defaulted LeoflowConfig the CLI loaded
	// before invoking the parser. It is marshaled to JSON and handed to the
	// parser via LEOFLOW_PROJECT_CONFIG_JSON, replacing the in-parser
	// PyYAML read of leoflow.yaml. The Go side stays the single source of
	// truth for the config schema; the parser carries zero third-party
	// Python deps (ADR 0024 + alpha cleanup).
	projectConfig *domain.LeoflowConfig
}

// parserConfigEnv is the env var that carries the resolved project config
// (JSON) from the CLI to the Python parser. The parser refuses to read a
// YAML file directly — keeping this seam tight prevents a re-vendoring of
// PyYAML.
const parserConfigEnv = "LEOFLOW_PROJECT_CONFIG_JSON"

// gitVersion derives a version label from git, falling back to "dev".
func gitVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(out))
}

// gitSHA returns the short commit hash, or "" when the project is not in git —
// so a deploy with tag_strategy git_sha gets a genuine per-commit tag and falls
// back to the version label otherwise.
func gitSHA(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolveParserCommand returns the explicit --parser-cmd value when set,
// otherwise the command resolved from configuration.
func resolveParserCommand(cmd *cobra.Command, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	cfg, err := config.Load(configFilePath(cmd), cmd.Flags())
	if err != nil {
		return "", err
	}
	return cfg.ParserCmd, nil
}

// runParser invokes the operator-configured parser command with the compile
// subcommand and its arguments, streaming output to the command's streams.
func runParser(cmd *cobra.Command, command string, a parserArgs) error {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return errors.New("parser command is empty")
	}
	argv := make([]string, 0, len(fields)+8)
	argv = append(argv, fields[1:]...)
	argv = append(argv, "compile",
		"--source", a.source,
		"--config", a.config,
		"--output", a.output,
		"--image", a.image,
		"--dag-version", a.dagVersion)
	//nolint:gosec // G204: the parser command is operator-configured by design (ADR 0005).
	pc := exec.CommandContext(cmdContext(cmd), fields[0], argv...)
	// Build the child environment. Guarantee the extracted parser sources are
	// importable: on a binary-only install the default command is a bare
	// `python3 -m leoflow_parser` and nothing else wires ~/.leoflow/pysrc/parser
	// onto PYTHONPATH, so the parser fails with ModuleNotFoundError (#587).
	env := withParserPythonPath(os.Environ())
	// Hand the resolved project config to the parser as JSON via an env var.
	// The parser uses this instead of re-parsing leoflow.yaml in-process, so
	// Go owns the schema and the parser ships zero third-party deps.
	if a.projectConfig != nil {
		raw, merr := json.Marshal(a.projectConfig)
		if merr != nil {
			return fmt.Errorf("marshaling project config for parser: %w", merr)
		}
		env = append(env, parserConfigEnv+"="+string(raw))
	}
	pc.Env = env
	pc.Stdout = cmd.OutOrStdout()
	// Stream the parser's stderr to the terminal and capture it, so a parse
	// failure carries the real traceback (e.g. the SyntaxError + file:line) in
	// the returned error — surfaced both in the dev terminal and the UI's import
	// error banner, not just an opaque "exit status 1".
	var stderr bytes.Buffer
	pc.Stderr = io.MultiWriter(cmd.ErrOrStderr(), &stderr)
	if err := pc.Run(); err != nil {
		raw := strings.TrimSpace(stderr.String())
		detail := lastLines(raw, 20)
		// Lead with the user-actionable summary line (e.g. `SyntaxError: ...`)
		// when present, so the cause is visible above the bounded traceback
		// instead of buried under our internal parser file paths (#D9).
		if summary := parserErrorSummary(raw); summary != "" {
			if detail != "" {
				return fmt.Errorf("%s\n\n%s", summary, detail)
			}
			return errors.New(summary)
		}
		if detail != "" {
			return fmt.Errorf("running parser %q: %w\n%s", command, err, detail)
		}
		return fmt.Errorf("running parser %q: %w", command, err)
	}
	return nil
}

// lastLines returns the final n lines of s (the most relevant tail of a Python
// traceback), trimming the rest so the error stays bounded.
func lastLines(s string, n int) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// pythonErrorRe matches a single Python exception-summary line of the form
// `<CamelCaseName>Error: <message>` — the canonical last line of a traceback.
// The leading-uppercase + ending-in-Error shape filters out the noisy
// `File "..."` and code-snippet lines that surround it.
var pythonErrorRe = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*Error: .+$`)

// parserErrorSummary extracts the deepest Python exception summary
// (`*Error: message`) from a parser traceback. The deepest line is the
// actual cause; intermediate "During handling of the above exception ..."
// wrappers are skipped. Returns "" when no error line is present so callers
// can fall back to the raw traceback. Closes #D9 from the dogfood audit
// (#212): users see a one-line cause instead of an internal-paths dump.
func parserErrorSummary(stderr string) string {
	var last string
	for _, raw := range strings.Split(stderr, "\n") {
		ln := strings.TrimSpace(raw)
		if pythonErrorRe.MatchString(ln) {
			last = ln
		}
	}
	return last
}

// overlayProject writes the leoflow.yaml Leoflow-specific config (staging,
// on-failure alerts, and per-task overrides) onto the produced dag.json. These
// are deployment concerns, not Airflow DAG attributes, so the parser does not
// emit them (ADR 0022, 0023; #424).
// Per-task overrides are bound by task_id; an entry naming a task absent from the
// DAG is a hard error (no silent drop). No-op when nothing is configured.
func overlayProject(dagJSONPath string, cfg *domain.LeoflowConfig) error {
	if cfg.Staging == nil && cfg.Alerts == nil && len(cfg.Tasks) == 0 && !hasDAGDefaults(cfg.Defaults) {
		return nil
	}
	data, err := os.ReadFile(dagJSONPath) //nolint:gosec // G304: output path is operator-supplied on the CLI.
	if err != nil {
		return fmt.Errorf("reading %s: %w", dagJSONPath, err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		return fmt.Errorf("parsing %s: %w", dagJSONPath, uerr)
	}
	if cfg.Staging != nil {
		spec.Staging = cfg.Staging
	}
	if cfg.Alerts != nil {
		spec.Alerts = cfg.Alerts
	}
	if verr := validateTaskBindings(cfg.Tasks, spec.Tasks); verr != nil {
		return verr
	}
	for i := range spec.Tasks {
		if override := cfg.Tasks[spec.Tasks[i].TaskID]; override != nil {
			applyTaskOverride(&spec.Tasks[i], override)
		}
		// DAG-wide defaults are a fallback, applied after the per-task override so
		// the most-specific value always wins (task > defaults). Baking them here
		// lands them in dag.json and flows them through dispatch→BuildPod exactly
		// like the per-task values already do.
		applyDAGDefaults(&spec.Tasks[i], cfg.Defaults)
	}
	out, err := json.MarshalIndent(&spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", dagJSONPath, err)
	}
	if werr := os.WriteFile(dagJSONPath, append(out, '\n'), 0o600); werr != nil {
		return fmt.Errorf("writing %s: %w", dagJSONPath, werr)
	}
	return nil
}

// validateTaskBindings guards the YAML↔task binding: every key in the leoflow.yaml
// tasks block must name a task_id present in the compiled DAG, so a typo fails the
// compile instead of silently overriding nothing (ADR 0023).
func validateTaskBindings(overrides map[string]*domain.TaskConfig, tasks []domain.TaskSpec) error {
	if len(overrides) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(tasks))
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		known[t.TaskID] = struct{}{}
		ids = append(ids, t.TaskID)
	}
	for id := range overrides {
		if _, ok := known[id]; !ok {
			sort.Strings(ids)
			return fmt.Errorf("leoflow.yaml tasks: unknown task_id %q; the DAG defines %v", id, ids)
		}
	}
	return nil
}

// dbtProfileConn extracts the managed connection id from a dbt task's compiled
// entrypoint (`python -m leoflow_runtime --dbt-profile <conn> <profile> && …`),
// or "" if the task has no dbt profile step. It is how applyTaskOverride knows a
// task depends on a specific connection the author's override must not drop.
func dbtProfileConn(entrypoint string) string {
	fields := strings.Fields(entrypoint)
	for i, f := range fields {
		if f == "--dbt-profile" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// containsString reports whether xs contains s.
func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// applyTaskOverride sets each override field that is present onto the task,
// leaving unset fields as compiled. Env entries are merged over any existing env.
func applyTaskOverride(task *domain.TaskSpec, o *domain.TaskConfig) {
	if o.Retries != nil {
		task.Retries = o.Retries
	}
	if o.RetryDelaySeconds != nil {
		task.RetryDelaySeconds = o.RetryDelaySeconds
	}
	if o.ExecutionTimeoutSeconds != nil {
		task.ExecutionTimeoutSeconds = o.ExecutionTimeoutSeconds
	}
	if o.Resources != nil {
		task.Resources = o.Resources
	}
	if o.Execution != nil {
		task.Execution = o.Execution
	}
	// Per-task declared secret sets narrow the DAG-level declaration (ADR 0045,
	// ADR 0055). A set list replaces (does not merge with) the compiled value,
	// matching the override semantics of the other scalar/struct fields above.
	if len(o.Connections) > 0 {
		task.Connections = o.Connections
		// A dbt task's managed connection is a compiler-injected DEPENDENCY — its
		// profile step reads AIRFLOW_CONN_<conn> — not an author preference. An
		// override that narrows connections must not silently drop it, or the task
		// fails "not delivered" under enforce scoping / an external backend (#10).
		// Re-add it if the override omitted it.
		if conn := dbtProfileConn(task.Entrypoint); conn != "" && !containsString(task.Connections, conn) {
			task.Connections = append(task.Connections, conn)
		}
	}
	if len(o.Variables) > 0 {
		task.Variables = o.Variables
	}
	if len(o.Env) > 0 {
		if task.Env == nil {
			task.Env = make(map[string]string, len(o.Env))
		}
		for k, v := range o.Env {
			task.Env[k] = v
		}
	}
}

// hasDAGDefaults reports whether the leoflow.yaml defaults block carries a
// DAG-wide value the overlay must bake onto tasks (resources or node_selector).
// Retries/retry_delay/timeout are handled upstream by the parser's default_args,
// so they don't count here.
func hasDAGDefaults(d *domain.ConfigDefaults) bool {
	return d != nil && (d.Resources.AsResources() != nil || len(d.NodeSelector) > 0)
}

// applyDAGDefaults fills a task's resources and node_selector from the DAG-wide
// leoflow.yaml defaults when the task declares none of its own. Per-task values
// (set by applyTaskOverride or compiled from the DAG) always win; the default
// never partially merges into an explicit block. Closes the accepted-but-ignored
// footgun where defaults.resources / defaults.node_selector reached neither
// dag.json nor the task pod (EKS validation aresta #6).
func applyDAGDefaults(task *domain.TaskSpec, d *domain.ConfigDefaults) {
	if d == nil {
		return
	}
	if task.Resources == nil {
		if r := d.Resources.AsResources(); r != nil {
			task.Resources = r
		}
	}
	if len(d.NodeSelector) > 0 && (task.Execution == nil || len(task.Execution.NodeSelector) == 0) {
		if task.Execution == nil {
			task.Execution = &domain.Execution{}
		}
		sel := make(map[string]string, len(d.NodeSelector))
		for k, v := range d.NodeSelector {
			sel[k] = v
		}
		task.Execution.NodeSelector = sel
	}
}

// validateDAGFile reads a produced dag.json and validates it against the schema.
func validateDAGFile(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: output path is supplied by the operator on the CLI.
	if err != nil {
		return fmt.Errorf("reading produced %s: %w", path, err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parsing produced %s: %w", path, err)
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("produced %s is invalid: %w", path, err)
	}
	if err := spec.ValidateSchedule(); err != nil {
		return fmt.Errorf("produced %s is invalid: %w", path, err)
	}
	return nil
}

// embedSource reads the produced dag.json and the original dag.py and stores the
// Python text in the spec's source field, so the control plane can serve it to
// the UI's Code tab. Re-marshaled with indentation to keep dag.json readable.
func embedSource(dagJSONPath, sourcePath string) error {
	specData, err := os.ReadFile(dagJSONPath) //nolint:gosec // G304: output path is operator-supplied on the CLI.
	if err != nil {
		return fmt.Errorf("reading %s: %w", dagJSONPath, err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(specData, &spec); uerr != nil {
		return fmt.Errorf("parsing %s: %w", dagJSONPath, uerr)
	}
	src, err := os.ReadFile(sourcePath) //nolint:gosec // G304: source path is operator-supplied on the CLI.
	if err != nil {
		return fmt.Errorf("reading dag source %s: %w", sourcePath, err)
	}
	spec.Source = string(src)
	out, err := json.MarshalIndent(&spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", dagJSONPath, err)
	}
	if werr := os.WriteFile(dagJSONPath, append(out, '\n'), 0o600); werr != nil {
		return fmt.Errorf("writing %s: %w", dagJSONPath, werr)
	}
	return nil
}

// cmdContext returns the command's context, falling back to context.Background.
func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
