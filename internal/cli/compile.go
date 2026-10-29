package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/dbt"
	"github.com/dexadata/dexaflow/internal/domain"
)

// compileOptions holds the resolved flags for a compile run.
type compileOptions struct {
	// inputs are the build args, secrets and package index of the build,
	// resolved by buildAndPush.
	inputs     buildInputs
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
	// lite marks a compile for `dexaflow lite`, which deploys to no target:
	// the workspace defaults apply, a target's do not.
	lite bool
	// platformsSource names where build.platforms came from, for the build
	// output; runCompile sets it.
	platformsSource string
	// remoteSource names where build.remote_builder came from, for the
	// builder line; runCompile sets it.
	remoteSource string
	// baseImageSource names where base_image came from: the project's own
	// config file, or the workspace, target or cluster defaults that filled
	// it; runCompile sets it.
	baseImageSource string
	// pushRefused is the deploy preflight's refusal of this machine's push
	// credential, kept until the builder is chosen because a remote builder
	// was configured (checkLocalPush); nil when the push was accepted.
	pushRefused error
	// settingsShown says the caller already printed the platforms and their
	// source (deploy does, before it asks to confirm), so the build does not
	// print them twice.
	settingsShown bool
	// built, when set, receives the builder's result. A push always asks
	// the builder for the pushed image's digest, which the compile writes
	// into dag.json (#1703); deploy registers that pin.
	built *build.Result
	// requirePin makes a pushed image whose digest cannot be read a failure
	// (deploy registers only a digest). Without it the compile keeps the
	// tag in dag.json and warns (warnPinRead).
	requirePin bool
	// provenance, when set, receives the builder that produced the image
	// and, for a generated Dockerfile, the base image it builds FROM: the
	// build half of the version's build info (#1702). A reused image (the
	// skip path) sets neither.
	provenance *domain.BuildInfo
	// reuse allows the skip path: the image tag is the default content label,
	// so an image the registry holds under it can be pinned instead of built
	// (ADR 0076). deploy sets it; --rebuild and --force clear it.
	reuse bool
	// rebuild builds the dependency image of a split build again even when
	// the registry holds one for its key (--rebuild).
	rebuild bool
	// beforeBuild, when set, runs once dag.json is compiled and validated and
	// before anything is built: deploy's dry run of the register (#1705).
	beforeBuild func() error
	// cluster is the deploy defaults layer the caller resolved (deploy, and
	// build --push), nil for none. The compile applies it again after the
	// local sources, so what is built is what the caller labeled and showed.
	cluster *defaultsLayer
	// sourceMode marks deploy's compile in source mode (#1772): image is the
	// runtime image, which is never built.
	sourceMode bool
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
			// Cobra does not reset a flag-bound variable between Execute()
			// calls on the same command object, and this one is written back
			// to below. Clearing it when the flag was not set keeps the
			// sentinel honest if a second Execute ever happens.
			if !cmd.Flags().Changed("output") {
				o.output = ""
			}
			return runCompile(cmd, dir, o)
		},
	}
	cmd.Flags().StringVarP(&o.output, "output", "o", "", "path to write the compiled dag.json (default <project>/dag.json)")
	cmd.Flags().StringVar(&o.image, "image", "", "container image reference for the DAG")
	cmd.Flags().String("registry", "", registryFlagUsage)
	cmd.Flags().StringVar(&o.parserCmd, "parser-cmd", "", "override the parser command (default from config)")
	cmd.Flags().StringVar(&o.dagVersion, "dag-version", "", "DAG version label (default: git describe of the project directory, or dev outside git, plus a hash of the DAG's content)")
	cmd.Flags().BoolVar(&o.build, "build", false, "build the DAG container image (requires --image)")
	cmd.Flags().BoolVar(&o.push, "push", false, "push the built image to its registry and write its digest into dag.json (requires --build)")
	cmd.Flags().BoolVar(&o.rebuild, "rebuild", false, "build the dependency image again even when the registry holds one for its key (with --push)")
	cmd.Flags().StringVar(&o.builder, "builder", "docker", "image build tool to shell out to (e.g. docker, podman, nerdctl)")
	cmd.Flags().StringVar(&o.dockerfile, "dockerfile", "Dockerfile", "Dockerfile path relative to the DAG directory")
	addPlatformFlag(cmd)
	return cmd
}

// defaultOutputPath resolves where the compiled dag.json goes when --output was
// not given.
//
// The flag used to default to the bare name `dag.json`, which resolves against
// the CURRENT directory rather than the project the command was pointed at. So
// `dexaflow compile /tmp/probe` run from a checkout overwrote that checkout's own
// tracked dag.json: the compile succeeded, the artifact it printed was correct,
// and the damage was to a file the command was never asked to touch (#1084).
//
// The project directory is what `compile <dir>` reads like and what the success
// line already implies. Compiling the directory you are standing in, which is
// the common interactive case and what every documented pipeline does before
// reading the artifact back, still writes ./dag.json: filepath.Join(".", x) is
// x. So the change is confined to the case that was broken.
func defaultOutputPath(dir, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(dir, "dag.json")
}

// loadCompileConfig is resolveCompileTarget plus the workspace and target
// defaults (applyWorkspaceDefaults) that deploy applies too, so a compile and a
// deploy of the same project build the same image. The deploy defaults a
// cluster advertises apply only when the caller read them (o.cluster: deploy,
// build --push); `compile` itself works offline and never asks a server.
func loadCompileConfig(cmd *cobra.Command, dir string, o compileOptions) (*domain.LeoflowConfig, defaultsProvenance, error) {
	cfg, err := resolveCompileTarget(dir, o.output)
	if err != nil {
		return nil, defaultsProvenance{}, err
	}
	prov, derr := applyDefaults(cmd, dir, cfg, !o.lite, o.cluster)
	if derr != nil {
		return nil, defaultsProvenance{}, derr
	}
	return cfg, prov, nil
}

// resolveCompileTarget loads the project config and confirms the artifact can
// be written where it is going, which are the two things every compile needs
// before it starts a parser.
func resolveCompileTarget(dir, output string) (*domain.LeoflowConfig, error) {
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		return nil, err
	}
	if werr := checkOutputWritable(output); werr != nil {
		return nil, werr
	}
	return cfg, nil
}

// checkOutputWritable refuses early, and by name, when the artifact cannot be
// written where it is going.
//
// Moving the default into the project directory made a previously working case
// fail: a source tree mounted read-only into a build container, a checkout owned
// by another user, a read-only CI volume. Without this the failure surfaced as a
// PermissionError traceback out of the Python parser, which names the parser's
// internals and not the thing the user can change.
func checkOutputWritable(output string) error {
	dir := filepath.Dir(output)
	probe, err := os.CreateTemp(dir, ".leoflow-write-probe-*")
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf(
				"cannot write the compiled artifact to %s: %s is not writable. Pass -o <path> to put it somewhere else",
				output, dir)
		}
		// Anything else (a missing directory, a path that is a file) is
		// reported by the checks that already own it, with their own wording.
		return nil
	}
	name := probe.Name()
	_ = probe.Close()      //nolint:errcheck // the probe's content is irrelevant
	return os.Remove(name) //nolint:wrapcheck // a failure to clean up the probe is worth surfacing as-is
}

// checkProjectPreconditions runs the checks that apply to every project before
// the dag.py and dbt paths diverge: the dexaflow.yaml must validate, it must not
// declare a dbt: block alongside a dag.py (#1015), and a deprecated Python line
// or a build field nothing applies earns a warning the author can still act on.
//
// Grouped rather than inlined so every entry point gets all of them by
// construction. warnDeprecatedPython landing on only the dag.py branch would
// leave every dbt project silently on a base image that stops being rebuilt,
// and `dexaflow validate` open-coding the first two checks is how it spent this
// PR's first round never warning at all — validate is the sub-second command an
// author runs in a loop with dexaflow.yaml open, which is the exact moment the
// warning is worth something.
func checkProjectPreconditions(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig) error {
	if verr := validateProject(dir, cfg); verr != nil {
		return verr
	}
	// Stderr, not stdout: the compile summary and any piped output stay clean.
	// Advisory only — a write failure here must not fail an otherwise good run.
	warnDeprecatedPython(cmd.ErrOrStderr(), cfg)
	warnIgnoredBuildFields(cmd.ErrOrStderr(), filepath.Base(projectConfigPath(dir)), cfg)
	return nil
}

// validateProject is the part of checkProjectPreconditions that can refuse a
// project, without its warnings, so `dexaflow build` can check a whole
// workspace before building any of it (#1121).
func validateProject(dir string, cfg *domain.LeoflowConfig) error {
	if verr := cfg.Validate(); verr != nil {
		return fmt.Errorf("invalid %s: %w", projectConfigPath(dir), verr)
	}
	return errDbtBlockWithDagSource(dir, cfg)
}

// prepareCompile loads the project config with its defaults, records where
// the platforms came from, and runs the project preconditions, every check
// that must pass before the parser starts.
func prepareCompile(cmd *cobra.Command, dir string, o *compileOptions) (*domain.LeoflowConfig, error) {
	cfg, prov, err := loadCompileConfig(cmd, dir, *o)
	if err != nil {
		return nil, err
	}
	o.platformsSource = prov.platformsSource
	o.remoteSource = prov.remoteSource
	o.baseImageSource = prov.baseImageSource
	if perr := checkProjectPreconditions(cmd, dir, cfg); perr != nil {
		return nil, perr
	}
	return cfg, nil
}

// runCompile resolves the project config, runs the parser, validates the output,
// and optionally builds the DAG image.
func runCompile(cmd *cobra.Command, dir string, o compileOptions) error {
	// Here rather than in the cobra RunE so every caller is covered by
	// construction: deploy.go and workspace_build.go each hand-rolled the same
	// filepath.Join, and a compileOptions literal that forgot it reached a write
	// with an empty path.
	o.output = defaultOutputPath(dir, o.output)
	if o.built == nil {
		o.built = &build.Result{}
	}
	cfg, err := prepareCompile(cmd, dir, &o)
	if err != nil {
		return err
	}
	// Self-heal the extracted parser sources before running the parser, so a binary
	// upgrade (new features like dbt vs a stale ~/.dexaflow/pysrc) never surfaces as
	// a confusing "not supported" error (#239).
	ensurePysrc(cmd)
	// The image is the --image flag when set, else derived from the dexaflow.yaml
	// registry block (url/image_name:tag), so a yaml-driven build needs no flag.
	// The resolved value flows into dag.json and the build, keeping the
	// registered artifact and the built/pushed image in lockstep.
	image, ierr := resolveVersionAndImage(cmd, dir, &o, cfg)
	if ierr != nil {
		return ierr
	}
	if cfg.Dbt != nil {
		return runDbtCompile(cmd, dir, o, cfg, image)
	}
	command, err := resolveParserCommand(cmd, o.parserCmd)
	if err != nil {
		return err
	}
	if ierr := checkImageFlags(cmd, o, image); ierr != nil {
		return ierr
	}
	if rerr := parseForDeclaredPython(cmd, command, parserArgs{
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
	if berr := checkThenBuild(cmd, dir, o, cfg, image); berr != nil {
		return berr
	}
	if o.build {
		remindDeprecatedPythonAfterBuild(cmd.ErrOrStderr(), cfg)
	}
	_, werr := fmt.Fprint(cmd.OutOrStdout(), compileSummary(dagSourcePath(dir, cfg), o.output, compiledImageRef(image, *o.built), o.dagVersion))
	return werr
}

// checkThenBuild runs o.beforeBuild on the compiled dag.json, then builds
// and pushes as o asks.
func checkThenBuild(cmd *cobra.Command, dir string, o compileOptions, cfg *domain.LeoflowConfig, image string) error {
	if o.beforeBuild != nil {
		if err := o.beforeBuild(); err != nil {
			return err
		}
	}
	return buildAndPush(cmd, dir, o, cfg, image)
}

// runDbtCompile compiles a dbt project (ADR 0042) instead of a Python DAG: it
// acquires the manifest.json (a baked file or a fresh `dbt parse`), renders it
// into a dag.json via the dbt package, overlays the dexaflow.yaml, validates, and
// optionally builds/pushes the image — reusing the same tail as the parser path.
func runDbtCompile(cmd *cobra.Command, dir string, o compileOptions, cfg *domain.LeoflowConfig, image string) error {
	if ierr := checkImageFlags(cmd, o, image); ierr != nil {
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
	if berr := checkThenBuild(cmd, dir, o, cfg, image); berr != nil {
		return berr
	}
	if o.build {
		remindDeprecatedPythonAfterBuild(cmd.ErrOrStderr(), cfg)
	}
	_, werr := fmt.Fprint(cmd.OutOrStdout(), compileSummary(filepath.Join(dir, cfg.Dbt.Project), o.output, compiledImageRef(image, *o.built), o.dagVersion))
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
			return nil, fmt.Errorf("dag uses dbt_group(%q) but dexaflow.yaml has no dbt_groups.%s", group, group)
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
	cand := filepath.Join(stateDirIn(home), "dev", "venvs", dagID, "bin", "dbt")
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
// the adapter its dexaflow.yaml declares, while PATH's is whatever the operator
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
// --build/--push flags, with the builder selectBuilder resolves. When building
// and the project ships no Dockerfile, one is generated from the dexaflow.yaml
// (base image + system packages + dependencies/connectors), built, and removed
// afterward so the workspace stays clean. A push always follows a build
// (checkImageFlags refuses --push without --build).
func buildAndPush(cmd *cobra.Command, dir string, o compileOptions, cfg *domain.LeoflowConfig, image string) error {
	if !o.build {
		return nil
	}
	if o.push && !o.settingsShown {
		// deploy warned with its settings.
		warnAuthMethod(cmd.ErrOrStderr(), cfg)
	}
	name, _ := resolveDockerfileName(cmd, o, cfg)
	o.inputs = resolveBuildInputs(cfg, os.LookupEnv)
	b, reason := selectBuilder(cmdContext(cmd), dir, name, o, cfg, image)
	return buildImage(cmd, dir, o, cfg, image, b, reason)
}

// buildImage runs builder b on the project, from its own Dockerfile or one
// generated from dexaflow.yaml, and pushes when asked. The checks before the
// build (a build.dockerfile outside the project, exclude_paths, secrets the
// image would carry) run on the skip path too, where b builds nothing: the
// image it reuses was built from the same inputs, so what they refuse or warn
// about holds for it as well.
func buildImage(cmd *cobra.Command, dir string, o compileOptions, cfg *domain.LeoflowConfig, image string, b build.Builder, reason string) error {
	name, fromConfig := resolveDockerfileName(cmd, o, cfg)
	if fromConfig {
		if cerr := checkConfiguredDockerfile(dir, name); cerr != nil {
			return cerr
		}
	}
	dockerfile, cleanup, derr := ensureDockerfile(dir, name, cfg, dagSourcePath(dir, cfg))
	if derr != nil {
		return derr
	}
	defer cleanup()
	// A project that ships its own Dockerfile gets the exclusions too, but
	// we cannot read its COPY lines, so the secret warning softens rather
	// than claiming what ships.
	ownDockerfile := dockerfile != filepath.Join(dir, generatedDockerfileName)
	if !ownDockerfile {
		fmt.Fprintln(cmd.ErrOrStderr(), resolvedBaseNote(cfg, o.baseImageSource))
	}
	warnShadowedByDockerfile(cmd.ErrOrStderr(), dockerfileShadow{
		own: ownDockerfile, dockerfile: name, configFile: projectConfigPath(dir), baseImageSource: o.baseImageSource,
	}, cfg)
	// Applies whether or not the project ships its own Dockerfile: exclude_paths
	// is a statement about what may enter the image, and it does not become
	// less true because the author wrote the Dockerfile themselves.
	ignoreCleanup, baked, ierr := ensureDockerignore(cmd.ErrOrStderr(), dir, cfg, ownDockerfile)
	if ierr != nil {
		return ierr
	}
	defer ignoreCleanup()
	if perr := printBuildSettings(cmd.OutOrStdout(), o, cfg, b, reason); perr != nil {
		return perr
	}
	if lerr := checkLocalPush(cmd.ErrOrStderr(), o.pushRefused, b); lerr != nil {
		return lerr
	}
	code, contextFiles, lerr := buildFiles(dir, cfg, o.output, dockerfile, b)
	if lerr != nil {
		return lerr
	}
	warnUploadedSecrets(cmd.ErrOrStderr(), contextFiles, cfg)
	if ierr := checkBuildInputs(cmd.ErrOrStderr(), o, cfg, b, ownDockerfile); ierr != nil {
		return ierr
	}
	res, berr := b.Build(cmdContext(cmd), build.Request{
		Image:        image,
		Dockerfile:   dockerfile,
		ContextDir:   dir,
		Platforms:    buildPlatforms(cfg),
		Push:         o.push,
		Pin:          o.push,
		Code:         code,
		ContextFiles: contextFiles,
		BuildArgs:    o.inputs.args,
		Secrets:      o.inputs.secrets,
		Env:          o.inputs.env,
		Stdout:       cmd.OutOrStdout(),
		Stderr:       cmd.ErrOrStderr(),
	})
	if werr := warnPinRead(cmd.ErrOrStderr(), o, res, berr); werr != nil {
		return werr
	}
	if perr := recordBuild(o, res); perr != nil {
		return perr
	}
	// A reused image (the skip path) was built earlier by a build this CLI
	// knows nothing of: naming "skip" or the base it would use now would be
	// a guess, so it records neither.
	if o.provenance != nil && res.Path != build.PathSkip {
		o.provenance.Builder = b.Describe()
		if !ownDockerfile {
			o.provenance.BaseImage = resolveBaseImage(cfg)
		}
		*o.provenance = o.provenance.Sanitized()
	}
	// After the build, where the eye lands: the warning above is minutes of
	// layer output away by now. A reused image printed no build output.
	if res.Path != build.PathSkip {
		remindBakedSecretsAfterBuild(cmd.ErrOrStderr(), baked)
	}
	return nil
}

// warnPinRead turns err, a build's failure, into a warning on w when only
// the read of the pushed image's digest failed (build.ErrPinRead) and the
// caller does not require a pin: the image is in the registry, so the
// compile keeps its tag in dag.json rather than fail after the push
// (podman reading another auth file, a plain-HTTP registry off the loopback).
// Any other failure, or any failure under o.requirePin, is returned.
func warnPinRead(w io.Writer, o compileOptions, res build.Result, err error) error {
	if err == nil || o.requirePin || !res.Pushed || !errors.Is(err, build.ErrPinRead) {
		return err
	}
	//nolint:errcheck // advisory
	fmt.Fprintf(w, "warning: %s was pushed, but dag.json keeps its tag: %v\n"+
		"  `dexaflow push` reads the digest again before registering; or register it pinned with "+
		"`dexaflow push --image <repository>@sha256:<digest>`\n", res.Image, err)
	return nil
}

// recordBuild hands res to the caller that asked for it (o.built) and, for a
// pushed image, writes its digest-pinned reference into dag.json
// (pinCompiledImage).
func recordBuild(o compileOptions, res build.Result) error {
	if o.built != nil {
		*o.built = res
	}
	if res.Ref == "" {
		return nil
	}
	return pinCompiledImage(o.output, res.Ref)
}

// pinCompiledImage writes ref, the pushed image's digest-pinned reference,
// as the image of the compiled dag.json at path, so the artifact `dexaflow
// push` registers names the bytes that were pushed (#1703), as deploy's does.
// The file keeps the layout the compile gives it.
func pinCompiledImage(path, ref string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: output path is operator-supplied on the CLI.
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		return fmt.Errorf("parsing %s: %w", path, uerr)
	}
	spec.Image = ref
	return writeDAGFile(path, &spec)
}

// compiledImageRef is the image the compiled dag.json names: the pinned
// reference of a pushed build, else image as resolved.
func compiledImageRef(image string, built build.Result) string {
	if built.Ref != "" {
		return built.Ref
	}
	return image
}

// resolveDockerfileName picks the Dockerfile name to look for in the DAG
// directory: an explicit --dockerfile flag wins, then dexaflow.yaml's
// build.dockerfile, else the "Dockerfile" default. fromConfig reports that the
// name came from build.dockerfile, which is the one source confined to the
// project (#1272): the flag is the operator's own choice on the command line.
func resolveDockerfileName(cmd *cobra.Command, o compileOptions, cfg *domain.LeoflowConfig) (name string, fromConfig bool) {
	if cmd.Flags().Changed("dockerfile") {
		return o.dockerfile, false
	}
	if cfg.Build != nil && cfg.Build.Dockerfile != "" {
		return cfg.Build.Dockerfile, true
	}
	return o.dockerfile, false
}

// checkConfiguredDockerfile refuses a build.dockerfile that resolves outside the
// project directory once symlinks are followed (#1272).
//
// Validate already refuses an absolute path and a `..` escape, but a symlink is
// lexically clean: `docker/Dockerfile` where docker links to a sibling
// directory builds from a file nobody reviewed with this dexaflow.yaml, and a
// Dockerfile that exists is used as-is, skipping every check the generated one
// gets. So both sides are resolved here, the project directory included, which
// keeps a project reached through a symlink (a CI workspace often is) working.
//
// A name that does not exist is not refused: ensureDockerfile then generates
// the Dockerfile, which is the guarded path, exactly as before.
func checkConfiguredDockerfile(dir, name string) error {
	outside := func(why string) error {
		return fmt.Errorf("%w: build.dockerfile %q %s, outside the project directory %s; "+
			"keep the Dockerfile inside the project so it is reviewed with it",
			domain.ErrInvalidBuildDockerfile, name, why, dir)
	}
	if filepath.IsAbs(name) {
		return outside("is absolute")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolving build.dockerfile %q: %w", name, err)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolving the project directory %s: %w", dir, err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return outside("resolves to " + strconv.Quote(resolved))
	}
	return nil
}

// compileSummary formats the success line for `dexaflow compile`. When image is
// empty it renders `no image` instead of `image ` to avoid the dangling-comma
// artifact reported as #D10 in the dogfood audit (#212).
func compileSummary(src, out, image, version string) string {
	imageField := "image " + image
	if image == "" {
		imageField = "no image"
	}
	return fmt.Sprintf("Compiled %s -> %s (%s, version %s)\n", src, out, imageField, version)
}

// checkImageFlags enforces the --build/--image relationship of o and notes
// when an image is recorded without being built, except in source mode,
// where no image is ever built.
func checkImageFlags(cmd *cobra.Command, o compileOptions, image string) error {
	building, push := o.build, o.push
	if push && !building {
		return errors.New("--push requires --build")
	}
	if building && image == "" {
		return errors.New("--build needs an image reference: pass --image, or set registry.url + registry.image_name in dexaflow.yaml")
	}
	if !building && image != "" && !o.sourceMode {
		_, werr := fmt.Fprintf(cmd.ErrOrStderr(), "note: recording image %q without building it; pass --build to build the DAG image\n", image)
		return werr
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
	// PyYAML read of dexaflow.yaml. The Go side stays the single source of
	// truth for the config schema; the parser carries zero third-party
	// Python deps (ADR 0024 + alpha cleanup).
	projectConfig *domain.LeoflowConfig
	// stdout receives the parser's own stdout. Nil means the command's stdout,
	// which is what compile wants; validate --parse discards it, because the
	// parser announces a scratch file the user never asked for.
	stdout io.Writer
	// stderr receives the parser's stderr. Nil means the command's stderr.
	// parseForDeclaredPython holds a retargeted run's output back until it
	// knows whether that run is the one to report.
	stderr io.Writer
}

// parserConfigEnv is the env var that carries the resolved project config
// (JSON) from the CLI to the Python parser. The parser refuses to read a
// YAML file directly — keeping this seam tight prevents a re-vendoring of
// PyYAML.
const parserConfigEnv = "LEOFLOW_PROJECT_CONFIG_JSON"

// resolveVersionAndImage settles the two names of a compile: it fills
// o.dagVersion with the content-derived default label (defaultDAGVersion) when
// --dag-version was not given, and returns the image reference, the --image
// flag when set, else the registry block's url/image_name with the tag strategy
// applied to the project's own git revision. Callers that resolve before
// runCompile (deploy) pass both back in, so runCompile resolves nothing twice.
func resolveVersionAndImage(cmd *cobra.Command, dir string, o *compileOptions, cfg *domain.LeoflowConfig) (string, error) {
	ctx := cmdContext(cmd)
	if o.dagVersion == "" {
		label, err := contentLabel(cmd, dir, *o, cfg)
		if err != nil {
			return "", err
		}
		o.dagVersion = label
	}
	if o.image != "" || cfg.Registry == nil || cfg.Registry.URL == "" || cfg.Registry.ImageName == "" {
		// No reference to derive: the flag, or nothing (checkImageFlags names
		// what is missing), so git has nothing to say.
		return o.image, nil
	}
	rev := readGitRevision(ctx, dir)
	if msg := tagStrategyFallback(cfg.Registry.TagStrategy, rev); msg != "" {
		//nolint:errcheck // a warning that cannot be delivered must not fail the compile
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg)
	}
	return resolveBuildImage("", cfg, o.dagVersion, rev), nil
}

// contentLabel is the default version label of the project at dir, whose
// settled config is cfg, for a compile with the options o: the Dockerfile
// name o and dexaflow.yaml pick, and the compiled output o.output, which the
// hash leaves out. In source mode (o.sourceMode) the label hashes the runtime
// image o.image too.
func contentLabel(cmd *cobra.Command, dir string, o compileOptions, cfg *domain.LeoflowConfig) (string, error) {
	dockerfile, _ := resolveDockerfileName(cmd, o, cfg)
	runtimeImage := ""
	if o.sourceMode {
		runtimeImage = o.image
	}
	return defaultDAGVersionOn(cmdContext(cmd), dir, cfg, dockerfile, o.output, runtimeImage)
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
	// `python3 -m leoflow_parser` and nothing else wires ~/.dexaflow/pysrc/parser
	// onto PYTHONPATH, so the parser fails with ModuleNotFoundError (#587).
	env := withParserPythonPath(os.Environ())
	// Hand the resolved project config to the parser as JSON via an env var.
	// The parser uses this instead of re-parsing dexaflow.yaml in-process, so
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
	if a.stdout != nil {
		pc.Stdout = a.stdout
	}
	// Stream the parser's stderr to the terminal and capture it, so a parse
	// failure carries the real traceback (e.g. the SyntaxError + file:line) in
	// the returned error — surfaced both in the dev terminal and the UI's import
	// error banner, not just an opaque "exit status 1".
	var stderr bytes.Buffer
	streamTo := cmd.ErrOrStderr()
	if a.stderr != nil {
		streamTo = a.stderr
	}
	pc.Stderr = io.MultiWriter(streamTo, &stderr)
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

// overlayProject writes the dexaflow.yaml Dexaflow-specific config (staging,
// on-failure alerts, min_idle_workers and per-task overrides) onto the
// produced dag.json. These
// are deployment concerns, not Airflow DAG attributes, so the parser does not
// emit them (ADR 0022, 0023; #424).
// Per-task overrides are bound by task_id; an entry naming a task absent from the
// DAG is a hard error (no silent drop). No-op when nothing is configured.
func overlayProject(dagJSONPath string, cfg *domain.LeoflowConfig) error {
	if !hasProjectOverlay(cfg) {
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
	overlayDAGFields(&spec, cfg)
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
		// Size 1 is the default weight; leaving it out keeps dag.json and its
		// hash identical to a DAG that never set size (ADR 0066). This runs after
		// both layers so a task's explicit 1 still beats a larger defaults.size.
		if spec.Tasks[i].PoolSlots == 1 {
			spec.Tasks[i].PoolSlots = 0
		}
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

// hasProjectOverlay reports whether cfg declares anything overlayProject
// writes onto dag.json.
func hasProjectOverlay(cfg *domain.LeoflowConfig) bool {
	return cfg.Staging != nil || cfg.Alerts != nil || cfg.MinIdleWorkers > 0 ||
		len(cfg.Tasks) > 0 || hasDAGDefaults(cfg.Defaults)
}

// overlayDAGFields copies the DAG-level fields of cfg the parser does not
// emit onto spec: staging, alerts and min_idle_workers.
func overlayDAGFields(spec *domain.DAGSpec, cfg *domain.LeoflowConfig) {
	if cfg.Staging != nil {
		spec.Staging = cfg.Staging
	}
	if cfg.Alerts != nil {
		spec.Alerts = cfg.Alerts
	}
	if cfg.MinIdleWorkers > 0 {
		spec.MinIdleWorkers = cfg.MinIdleWorkers
	}
}

// validateTaskBindings guards the YAML↔task binding: every key in the dexaflow.yaml
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
			return fmt.Errorf("dexaflow.yaml tasks: unknown task_id %q; the DAG defines %v", id, ids)
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
	if o.Size != nil {
		task.PoolSlots = *o.Size
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

// hasDAGDefaults reports whether the dexaflow.yaml defaults block carries a
// DAG-wide value the overlay must bake onto tasks (resources, node_selector or
// size). Retries/retry_delay/timeout are handled upstream by the parser's
// default_args, so they don't count here.
func hasDAGDefaults(d *domain.ConfigDefaults) bool {
	return d != nil && (d.Resources.AsResources() != nil || len(d.NodeSelector) > 0 || d.Size != nil)
}

// applyDAGDefaults fills a task's resources, node_selector and pool_slots from
// the DAG-wide dexaflow.yaml defaults when the task declares none of its own.
// Per-task values (set by applyTaskOverride or compiled from the DAG) always
// win; the default
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
	// A task's own pool_slots (from dag.py or tasks.<id>.size) is more specific
	// than the DAG-wide size (ADR 0066).
	if task.PoolSlots == 0 && d.Size != nil {
		task.PoolSlots = *d.Size
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
