package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// registryFlagUsage is the help of --registry, shared by compile, build and
// deploy so all three derive the same image.
const registryFlagUsage = "image registry prefix to push to, over every other source (then DEXAFLOW_REGISTRY, dexaflow.yaml, the target, the workspace defaults, the config file)"

// deployOptions holds the resolved flags for a deploy run.
type deployOptions struct {
	serverURL  string
	token      string
	builder    string
	dockerfile string
	dagVersion string
	all        bool
	skipBuild  bool
	trigger    bool
	yes        bool
	force      bool
	paused     bool
	active     bool
	plan       bool
	output     string
	follow     bool
	timeout    time.Duration
	// report collects each DAG's result for --output json; nil for text.
	report *deployReport
}

// pausedOverride is is_paused as --paused or --active set it, nil when
// neither was passed.
func (o deployOptions) pausedOverride() *bool {
	switch {
	case o.paused:
		v := true
		return &v
	case o.active:
		v := false
		return &v
	}
	return nil
}

// newDeployCommand builds `dexaflow deploy [path]`: the pipeline-less promotion of
// a DAG to a Pro control plane (ADR 0041). It reuses the compile/build/push
// primitives, then re-pins the image by digest and registers the dag.json — one
// verb for what is otherwise compile --build --push + push by hand.
func newDeployCommand() *cobra.Command {
	var o deployOptions
	cmd := &cobra.Command{
		Use:   "deploy [path | dag_id]",
		Short: "Build, push, and register a DAG to a control plane (Pro).",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeployCommand(cmd, args, o)
		},
	}
	cmd.Flags().BoolVar(&o.all, "all", false, "deploy every DAG project in the workspace")
	cmd.Flags().BoolVar(&o.skipBuild, "skip-build", false, "register the image already in the registry (no build or push; the digest is read from the registry) but still recompile dag.json from dexaflow.yaml/dag.py")
	cmd.Flags().BoolVar(&o.trigger, "trigger", false, "trigger a run immediately after registering")
	addFollowFlags(cmd, &o.follow, &o.timeout)
	cmd.Flags().BoolVarP(&o.yes, "yes", "y", false, "skip the confirmation prompt (for automation)")
	cmd.Flags().BoolVar(&o.force, "force", false, "build, push and register even when the default content label is already deployed")
	cmd.Flags().StringVar(&o.serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&o.token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	cmd.Flags().String("registry", "", registryFlagUsage)
	cmd.Flags().StringVar(&o.builder, "builder", "docker", "image build tool to shell out to (e.g. docker, podman, nerdctl)")
	cmd.Flags().StringVar(&o.dockerfile, "dockerfile", "Dockerfile", "Dockerfile path relative to the DAG directory")
	cmd.Flags().StringVar(&o.dagVersion, "dag-version", "", "DAG version label (default: git describe of the project directory, or dev outside git, plus a hash of the DAG's content)")
	cmd.Flags().BoolVar(&o.paused, "paused", false, "leave the DAG paused after this deploy, new or not (default: a new DAG follows is_paused_upon_creation, else the server's dags_are_paused_at_creation; a redeploy keeps is_paused)")
	cmd.Flags().BoolVar(&o.active, "active", false, "leave the DAG active (unpaused) after this deploy, new or not")
	cmd.MarkFlagsMutuallyExclusive("paused", "active")
	cmd.Flags().BoolVar(&o.plan, "plan", false, "say per DAG what would be built, pushed and registered, and why, without doing any of it")
	cmd.Flags().StringVar(&o.output, "output", outputText, "output format: text, or json for one JSON document on stdout (human lines go to stderr)")
	addPlatformFlag(cmd)
	return cmd
}

// runDeployTarget resolves what to deploy from the args and flags: --all (every
// project in the workspace), a directory path, or a dag_id (resolved to its
// subdir in the workspace). A bare invocation is deployHere.
func runDeployTarget(cmd *cobra.Command, args []string, o deployOptions) error {
	if err := checkDeployFollow(o); err != nil {
		return err
	}
	if o.all {
		if len(args) > 0 {
			return fmt.Errorf("--all deploys the whole workspace; do not also pass a path or dag_id")
		}
		return deployAll(cmd, o)
	}
	if len(args) == 0 {
		return deployHere(cmd, o)
	}
	target := args[0]
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return runDeploy(cmd, target, o)
	}
	ws, werr := dagLookupWorkspace(cmd)
	if werr != nil {
		return werr
	}
	printWorkspace(cmd, ws)
	dir, rerr := resolveProjectDir(ws.dir, target)
	if rerr != nil {
		return rerr
	}
	return runDeploy(cmd, dir, o)
}

// deployHere is a bare `dexaflow deploy`: the project in the current folder,
// or, at a workspace root (which is not a project itself), every DAG of the
// workspace. Any other folder that is not a project is an error that says
// how to deploy one DAG or all of them.
func deployHere(cmd *cobra.Command, o deployOptions) error {
	if isProjectDir(".") {
		return runDeploy(cmd, ".", o)
	}
	ws, err := findWorkspaceFromCwd()
	if err != nil {
		return err
	}
	if ws != nil && sameDir(".", ws.Root) {
		return deployAll(cmd, o)
	}
	wd, _ := os.Getwd() //nolint:errcheck // only names the folder in the message.
	return fmt.Errorf("%s is not a DAG project (no %s or dag.py).\n"+
		"  - deploy one DAG of the workspace:  dexaflow deploy <dag_id>\n"+
		"  - deploy all of them:               dexaflow deploy --all\n"+
		"  - deploy a project folder:          dexaflow deploy <path>", wd, projectConfigFile)
}

// sameDir reports whether a and b are the same directory once both are
// absolute and symlink-resolved.
func sameDir(a, b string) bool {
	ra, aerr := resolvedDir(a)
	rb, berr := resolvedDir(b)
	return aerr == nil && berr == nil && ra == rb
}

// resolvedDir returns dir as an absolute, symlink-resolved path.
func resolvedDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", dir, err)
	}
	if r, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		return r, nil
	}
	return abs, nil
}

// isProjectDir reports whether dir holds a DAG project: a dexaflow.yaml (or
// leoflow.yaml) or a dag.py.
func isProjectDir(dir string) bool {
	return fileExists(projectConfigPath(dir)) || fileExists(filepath.Join(dir, "dag.py"))
}

// deployWorkspace is a folder deploy reads DAG projects from, and why.
type deployWorkspace struct {
	dir    string
	source string
}

// printWorkspace says which workspace a deploy reads, before it starts.
func printWorkspace(cmd *cobra.Command, ws deployWorkspace) {
	//nolint:errcheck // informational
	fmt.Fprintf(cmd.OutOrStdout(), "workspace: %s (from %s)\n", ws.dir, ws.source)
}

// liteFallbackWorkspace is the Lite workspace: the config's workspace, or
// ~/dexaflow.
func liteFallbackWorkspace(cmd *cobra.Command) deployWorkspace {
	return deployWorkspace{dir: defaultWorkspace(cmd), source: "the Lite workspace (workspace: in the config file, else ~/dexaflow)"}
}

// dagLookupWorkspace is where `deploy <dag_id>` looks the id up: the
// workspace around the current folder (its dexaflow.workspace.yaml), else the
// git repository around it, else the current folder when it is a project, else
// the Lite workspace. Reading the Lite workspace first made the command fail
// with "lstat ~/dexaflow" inside a repository (#1704). Looking an id up across
// a repository is safe: it deploys one DAG, by name.
func dagLookupWorkspace(cmd *cobra.Command) (deployWorkspace, error) {
	ws, err := findWorkspaceFromCwd()
	if err != nil {
		return deployWorkspace{}, err
	}
	if ws != nil {
		return deployWorkspace{dir: ws.Root, source: ws.File}, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return deployWorkspace{}, fmt.Errorf("resolving the current directory: %w", err)
	}
	if root := gitRootOf(cmdContext(cmd), wd); root != "" {
		return deployWorkspace{dir: root, source: "the git repository around the current folder"}, nil
	}
	if isProjectDir(wd) {
		return deployWorkspace{dir: wd, source: "the current folder, a DAG project"}, nil
	}
	return liteFallbackWorkspace(cmd), nil
}

// deployAllWorkspace is what `deploy --all` deploys: the workspace around the
// current folder, else the current folder when it is a project (a workspace
// of one). It never widens to a git repository, which may hold DAGs nobody
// meant to deploy together: inside one with no workspace file it refuses.
// Outside any repository it keeps the Lite workspace, as before workspaces.
func deployAllWorkspace(cmd *cobra.Command) (deployWorkspace, error) {
	ws, err := findWorkspaceFromCwd()
	if err != nil {
		return deployWorkspace{}, err
	}
	if ws != nil {
		return deployWorkspace{dir: ws.Root, source: ws.File}, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return deployWorkspace{}, fmt.Errorf("resolving the current directory: %w", err)
	}
	if isProjectDir(wd) {
		return deployWorkspace{dir: wd, source: "the current folder, a DAG project"}, nil
	}
	if root := gitRootOf(cmdContext(cmd), wd); root != "" {
		return deployWorkspace{}, fmt.Errorf("--all needs a workspace: no %s here or above, and deploying every DAG of the repository at %s is not assumed.\n"+
			"  - make the repository a workspace:  dexaflow workspace init %s\n"+
			"  - or deploy one DAG:                dexaflow deploy <dag_id>", workspaceFileName, root, root)
	}
	return liteFallbackWorkspace(cmd), nil
}

// resolveProjectDir maps a dag_id to its project directory within the workspace,
// erroring (with the available ids) when no project matches — so a typo is a
// loud, helpful failure rather than a silent wrong-DAG deploy.
func resolveProjectDir(workspaceDir, dagID string) (string, error) {
	ws, err := ResolveWorkspace(workspaceDir)
	if err != nil {
		return "", err
	}
	available := make([]string, 0, len(ws.Projects))
	for _, p := range ws.Projects {
		if p.DagID == dagID {
			return p.Path, nil
		}
		available = append(available, p.DagID)
	}
	return "", fmt.Errorf("no DAG %q in workspace %s; available: %s",
		dagID, workspaceDir, strings.Join(available, ", "))
}

// deployAll deploys every project in the workspace, best-effort: it builds,
// pushes, and registers each, prints a per-DAG summary, and returns a non-zero
// error if any failed (so CI catches it) — partial pushes are not rolled back
// (content-addressed images make a re-deploy idempotent). ADR 0041.
func deployAll(cmd *cobra.Command, o deployOptions) error {
	where, err := deployAllWorkspace(cmd)
	if err != nil {
		return err
	}
	printWorkspace(cmd, where)
	wsDir := where.dir
	ws, err := ResolveWorkspace(wsDir)
	if err != nil {
		return err
	}
	if len(ws.Projects) == 0 {
		return fmt.Errorf("no DAG projects found in workspace %s", wsDir)
	}
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	choice, _, cerr := deployServerChoice(cmd, o)
	if cerr != nil {
		return cerr
	}
	if !o.plan && shouldConfirm(choice.URL, o.yes, cmdInteractive(cmd)) {
		target := fmt.Sprintf("%d DAG(s) from %s", len(ws.Projects), wsDir)
		if !confirmDeploy(cmd.InOrStdin(), out, target, choice.URL) {
			return fmt.Errorf("deploy aborted")
		}
	}
	o.yes = true // confirmed once for the whole workspace; no per-project prompts
	verb := "deploying"
	if o.plan {
		verb = "planning"
	}
	var failed []string
	for _, p := range ws.Projects {
		devPrintf(out, "==> %s %s (%s)\n", verb, p.DagID, p.Path)
		if derr := runDeploy(cmd, p.Path, o); derr != nil {
			failed = append(failed, p.DagID)
			o.report.addFailure(p.DagID, derr)
			devPrintf(errOut, "    FAILED %s: %v\n", p.DagID, derr)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d deploys failed: %s",
			len(failed), len(ws.Projects), strings.Join(failed, ", "))
	}
	if o.plan {
		_, err = io.WriteString(out, planSummary(len(ws.Projects), wsDir))
		return err
	}
	_, err = fmt.Fprintf(out, "Deployed %d DAG(s) from %s\n", len(ws.Projects), wsDir)
	return err
}

// requireRegistry enforces ADR 0041's hard prerequisite: a Pro deploy pushes the
// DAG image to a registry the cluster can pull from, so a configured registry is
// mandatory. It returns a loud, actionable error (never a silent attempt) when
// the registry URL or image name is missing — Lite runs locally and needs none,
// but deploy does, full stop.
func requireRegistry(cfg *domain.LeoflowConfig) error {
	if cfg != nil && cfg.Registry != nil &&
		strings.TrimSpace(cfg.Registry.URL) != "" && strings.TrimSpace(cfg.Registry.ImageName) != "" {
		return nil
	}
	return fmt.Errorf(`deploy requires a container registry, but none is configured.
  A Pro deploy pushes the DAG image to a registry your cluster can pull from
  (Lite runs locally and needs none). Set one, for one DAG or for all:

      dexaflow.yaml:              registry: {url: ghcr.io/<your-org>}
      dexaflow.workspace.yaml:    defaults: {registry: ghcr.io/<your-org>}
      a target:                   registry: ghcr.io/<your-org> under targets.<name> in dexaflow.workspace.yaml
      this deploy:                --registry ghcr.io/<your-org>  (or DEXAFLOW_REGISTRY)

  The image name defaults to the dag_id. Then authenticate your builder once:  docker login ghcr.io`)
}

// repinImageInSpec rewrites the compiled dag.json's image field to the
// digest-pinned reference, preserving every other field, so Pro pulls exactly
// the bytes that were built (ADR 0003 + 0041). It round-trips through a generic
// map so unknown/future fields survive untouched.
func repinImageInSpec(data []byte, digestRef string) ([]byte, error) {
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing compiled dag.json: %w", err)
	}
	spec["image"] = digestRef
	out, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encoding re-pinned dag.json: %w", err)
	}
	return out, nil
}

// deployImageRef composes the pushed image reference for a project from its
// registry config, the resolved version, and the project's git revision,
// applying the tag strategy (so tag_strategy: git_sha yields a genuine commit
// tag, not the version label).
func deployImageRef(cfg *domain.LeoflowConfig, version string, rev gitRevision) string {
	tag := resolveImageTag(cfg.Registry.TagStrategy, version, rev)
	return composeImageRef(cfg.Registry.URL, cfg.Registry.ImageName, tag)
}

// resolveServerToken resolves the control plane a server command calls and the
// bearer token it sends there. The server comes from resolveServer (flags, a
// named target, the environment, then the config file); the token from
// pickToken (--token, DEXAFLOW_TOKEN or LEOFLOW_TOKEN, the session saved for
// that server, then the single-server session of older configs).
//
// When the token comes from a saved session (not a --token flag or a token
// variable, which are caller- or CI-managed), it is transparently refreshed if
// near expiry and saved back where it came from (aresta #5), so a long dev
// session never re-logs in every hour. The refresh is best-effort and never
// fails the command.
func resolveServerToken(cmd *cobra.Command, serverFlag, tokenFlag string) (serverURL, token string, err error) {
	choice, token, err := resolveServerChoiceToken(cmd, serverFlag, tokenFlag)
	if err != nil {
		return "", "", err
	}
	return choice.URL, token, nil
}

// resolveServerChoiceToken is resolveServerToken that also returns what chose
// the server, for commands that say so.
func resolveServerChoiceToken(cmd *cobra.Command, serverFlag, tokenFlag string) (choice *serverChoice, token string, err error) {
	choice, err = resolveServer(cmd, serverFlag)
	if err != nil {
		return nil, "", err
	}
	serverURL := choice.URL
	token, source := pickToken(choice, tokenFlag)
	ctx := cmdContext(cmd)
	switch source {
	case tokenSession:
		token = autoRefreshWith(ctx, serverURL, token, func(fresh string) error {
			return saveSession(cmd, serverURL, fresh)
		})
	case tokenLegacy:
		token = autoRefreshToken(ctx, configFilePath(cmd), serverURL, token)
	case tokenNone, tokenExternal:
	}
	// Record what was decided so a 401 can say that the saved token belongs to
	// another server (#1102): it was withheld, so the call went unauthenticated.
	// A session saved for this very server cannot belong to another one.
	store := choice.tc.store
	withheld := source == tokenNone && store.Token != ""
	resolvedTarget.server = serverURL
	resolvedTarget.configured = ""
	if source == tokenLegacy || withheld {
		resolvedTarget.configured = store.ServerURL
	}
	resolvedTarget.fromConfig = source == tokenLegacy || withheld
	return choice, token, nil
}

// loadDeployConfig loads and validates the project config, fills what it
// leaves unset from the workspace and the target (applyWorkspaceDefaults), and
// enforces the registry a Pro deploy pushes to. It returns where the registry
// came from.
func loadDeployConfig(cmd *cobra.Command, dir string) (*domain.LeoflowConfig, defaultsProvenance, error) {
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		return nil, defaultsProvenance{}, err
	}
	if verr := cfg.Validate(); verr != nil {
		return nil, defaultsProvenance{}, fmt.Errorf("invalid %s: %w", projectConfigPath(dir), verr)
	}
	prov, derr := applyWorkspaceDefaults(cmd, dir, cfg, true)
	if derr != nil {
		return nil, defaultsProvenance{}, derr
	}
	if rerr := requireRegistry(cfg); rerr != nil {
		return nil, defaultsProvenance{}, rerr
	}
	return cfg, prov, nil
}

// printDeploySettings says, before anything is built, where the deploy goes
// and where each setting it did not get from the project came from.
func printDeploySettings(out io.Writer, choice *serverChoice, cfg *domain.LeoflowConfig, prov defaultsProvenance) error {
	var b strings.Builder
	fmt.Fprintf(&b, "server:     %s (%s)\n", choice.URL, choice.Source)
	fmt.Fprintf(&b, "registry:   %s/%s (from %s)\n", cfg.Registry.URL, cfg.Registry.ImageName, prov.registrySource)
	fmt.Fprintf(&b, "platforms:  %s (from %s)\n", strings.Join(cfg.Build.Platforms, ","), orDefault(prov.platformsSource))
	if cfg.BaseImage != "" {
		fmt.Fprintf(&b, "base image: %s (from %s)\n", cfg.BaseImage, prov.baseImageSource)
	}
	_, err := io.WriteString(out, b.String())
	return err
}

// orDefault names a setting's source, or the built-in default.
func orDefault(source string) string {
	if source == "" {
		return "the default"
	}
	return source
}

// runDeploy orchestrates the pipeline-less promotion: validate the project,
// enforce the registry, build+push the image for the target platform, re-pin it
// by digest into dag.json, and register that with the control plane. With
// --plan it stops after deciding what it would do.
func runDeploy(cmd *cobra.Command, dir string, o deployOptions) (err error) {
	cfg, prov, err := loadDeployConfig(cmd, dir)
	if err != nil {
		return err
	}
	// A failure past this point is reported for its DAG in --output json.
	defer func() {
		if err != nil {
			o.report.addFailure(cfg.DagID, err)
		}
	}()
	choice, token, serr := deployServerChoice(cmd, o)
	if serr != nil {
		return serr
	}
	o.report.setServer(choice)
	serverURL := choice.URL
	if perr := printDeploySettings(cmd.OutOrStdout(), choice, cfg, prov); perr != nil {
		return perr
	}
	if !o.plan && shouldConfirm(serverURL, o.yes, cmdInteractive(cmd)) &&
		!confirmDeploy(cmd.InOrStdin(), cmd.OutOrStdout(), dir, serverURL) {
		return fmt.Errorf("deploy aborted")
	}
	d := &deployRun{cmd: cmd, dir: dir, o: o, cfg: cfg, serverURL: serverURL, token: token}
	return d.run()
}

// deployRun is one DAG's deploy once its config and server are resolved.
type deployRun struct {
	cmd              *cobra.Command
	dir              string
	o                deployOptions
	cfg              *domain.LeoflowConfig
	serverURL, token string
	// wasPaused is the DAG's is_paused before an --active deploy, so the
	// summary can describe the backlog it releases.
	wasPaused bool
}

// readStateBefore records, for --active, whether the DAG was paused before
// this deploy. Other deploys do not change is_paused, or leave it paused,
// and need no read.
func (d *deployRun) readStateBefore(ctx context.Context) {
	if p := d.o.pausedOverride(); p == nil || *p {
		return
	}
	if before, ok := fetchDagState(ctx, d.serverURL, d.token, d.cfg.DagID); ok {
		d.wasPaused = before.IsPaused
	}
}

// run resolves the label and image, then plans, or checks, builds, pushes
// and registers.
func (d *deployRun) run() error {
	origin := labelDefault
	if d.o.dagVersion != "" {
		origin = labelExplicit
	}
	output := filepath.Join(d.dir, "dag.json")
	var built build.Result
	co := compileOptions{
		output:     output,
		builder:    d.o.builder,
		dockerfile: d.o.dockerfile,
		dagVersion: d.o.dagVersion,
		build:      !d.o.skipBuild,
		push:       !d.o.skipBuild,
		// printDeploySettings showed the platforms; the build pins its push.
		settingsShown: true,
		built:         &built,
	}
	// Resolved here rather than inside runCompile because the digest lookup
	// below needs the same reference; runCompile then takes both as given.
	image, ierr := resolveVersionAndImage(d.cmd, d.dir, &co, d.cfg)
	if ierr != nil {
		return ierr
	}
	co.image = image
	version := co.dagVersion
	ctx, out := cmdContext(d.cmd), d.cmd.OutOrStdout()
	if d.o.plan {
		return d.plan(ctx, version, image, origin)
	}
	// Only a content label says anything about content; --force skips it.
	if done, perr := preDeployCheck(ctx, d.cmd.ErrOrStderr(), out, d.serverURL, d.token, d.cfg.DagID, version,
		origin == labelDefault && !d.o.force); done || perr != nil {
		if perr != nil {
			return perr
		}
		return d.alreadyCurrent(ctx, version)
	}
	if cerr := runCompile(d.cmd, d.dir, co); cerr != nil {
		return cerr
	}
	digestRef, derr := deployedDigest(d.cmd, image, built)
	if derr != nil {
		return derr
	}
	d.readStateBefore(ctx)
	res, rerr := registerDeployedDAG(ctx, out, registerRequest{serverURL: d.serverURL, token: d.token, output: output,
		digestRef: digestRef, version: version, origin: origin, paused: d.o.pausedOverride(), warn: d.cmd.ErrOrStderr()})
	if rerr != nil {
		return rerr
	}
	surfaceConnections(out, output)
	return d.finish(ctx, res)
}

// plan says what a deploy of this DAG would do, and records it.
func (d *deployRun) plan(ctx context.Context, version, image string, origin labelOrigin) error {
	in := planInput{serverURL: d.serverURL, token: d.token, dagID: d.cfg.DagID, version: version, image: image,
		origin: origin, skipBuild: d.o.skipBuild, force: d.o.force, paused: d.o.pausedOverride()}
	res, err := planDeploy(ctx, d.cmd.ErrOrStderr(), in)
	if err != nil {
		return err
	}
	d.o.report.add(res)
	return printPlan(d.cmd.OutOrStdout(), d.dir, res, in)
}

// alreadyCurrent finishes a deploy whose content is already the current
// version: nothing was registered, so --paused or --active is applied with
// the Airflow PATCH, then the summary and --trigger run as for a register.
func (d *deployRun) alreadyCurrent(ctx context.Context, version string) error {
	d.readStateBefore(ctx)
	if p := d.o.pausedOverride(); p != nil {
		if err := patchDagPausedRaw(ctx, d.serverURL, d.token, d.cfg.DagID, *p); err != nil {
			return err
		}
	}
	return d.finish(ctx, deployResult{DagID: d.cfg.DagID, Status: deployStatusCurrent, Version: version})
}

// finish prints what happens next, records the result and runs --trigger.
func (d *deployRun) finish(ctx context.Context, res deployResult) error {
	out := d.cmd.OutOrStdout()
	if err := printNextStep(ctx, out, d.serverURL, d.token, &res, d.wasPaused); err != nil {
		return err
	}
	d.o.report.add(res)
	if trigger := deployTrigger(ctx, out, d.serverURL, d.token, d.o); trigger != nil {
		return trigger(res.DagID)
	}
	return nil
}

// preDeployCheck runs checkDeployed for a content label (when enabled) and
// acts on it: done when the label is already the current version (nothing is
// built, pushed or registered; the caller finishes the deploy), an error when
// the label is an older version or the check was refused for authentication.
func preDeployCheck(ctx context.Context, warn, out io.Writer, serverURL, token, dagID, version string, enabled bool) (done bool, err error) {
	if !enabled {
		return false, nil
	}
	state, current, err := checkDeployed(ctx, warn, serverURL, token, dagID, version)
	if err != nil {
		return false, err
	}
	switch state {
	case deployCurrent:
		_, werr := fmt.Fprintf(out, "%s already deployed as %s (the current version): nothing to build, push or register.\n"+
			"Changes outside the hashed inputs (environment variables, Variables read at parse time) need a new --dag-version; --force skips this check.\n",
			dagID, version)
		return true, werr
	case deployStale:
		return false, fmt.Errorf("%s content matches version %s, which is not the current version (%s). "+
			"To make it current again, run `dexaflow rollback %s --to %s`; to register this content again as a new version, pass --dag-version <new-label>",
			dagID, version, current, dagID, version)
	case deployUnknown:
	}
	return false, nil
}

// registerRequest is what registerDeployedDAG registers: the compiled
// dag.json at output, pinned to digestRef, under version, with the optional
// is_paused of --paused or --active.
type registerRequest struct {
	serverURL, token string
	output           string
	digestRef        string
	version          string
	origin           labelOrigin
	paused           *bool
	// warn receives the warning that the server did not apply paused.
	warn io.Writer
}

// registerAnswer is the part of the register response the deploy reports.
// IsPaused and DagCreated are nil from a server that does not report them.
type registerAnswer struct {
	SpecHash   string `json:"spec_hash"`
	IsPaused   *bool  `json:"is_paused"`
	DagCreated *bool  `json:"dag_created"`
}

// warnPausedIgnored warns when --paused or --active was asked and the answer
// does not show it applied: a server from before the is_paused parameter
// registers without it.
func warnPausedIgnored(w io.Writer, asked, got *bool) {
	if asked == nil || w == nil || (got != nil && *got == *asked) {
		return
	}
	flag := "--active"
	if *asked {
		flag = "--paused"
	}
	//nolint:errcheck // advisory: the register succeeded either way
	fmt.Fprintf(w, "warning: the server did not confirm %s (is_paused); it may predate the parameter. Check the DAG's state, and set it with `dexaflow admin dags pause|unpause`.\n", flag)
}

// registerDeployedDAG re-pins the compiled dag.json to the pushed image's digest
// and registers it with the control plane, then prints a per-phase summary. It
// is split from runDeploy so the post-build flow (which needs no builder) is
// unit-testable against a fake control plane.
func registerDeployedDAG(ctx context.Context, out io.Writer, r registerRequest) (deployResult, error) {
	data, rerr := os.ReadFile(r.output) //nolint:gosec // output path is operator-controlled (the DAG dir).
	if rerr != nil {
		return deployResult{}, fmt.Errorf("reading %s: %w", r.output, rerr)
	}
	repinned, perr := repinImageInSpec(data, r.digestRef)
	if perr != nil {
		return deployResult{}, perr
	}
	if werr := os.WriteFile(r.output, repinned, 0o600); werr != nil {
		return deployResult{}, fmt.Errorf("writing %s: %w", r.output, werr)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(repinned, &spec); jerr != nil {
		return deployResult{}, fmt.Errorf("parsing compiled dag.json: %w", jerr)
	}
	status, body, uerr := pushVersionPaused(ctx, r.serverURL, r.token, spec.DagID, repinned, r.paused)
	if uerr != nil {
		return deployResult{}, uerr
	}
	if status >= http.StatusMultipleChoices {
		return deployResult{}, versionConflictHint(apiStatusError(status, []byte(body)), r.version, r.origin)
	}
	var answer registerAnswer
	_ = json.Unmarshal([]byte(body), &answer) //nolint:errcheck // an older server's body may say less; the register itself succeeded
	warnPausedIgnored(r.warn, r.paused, answer.IsPaused)
	res := deployResult{DagID: spec.DagID, Version: r.version, Image: r.digestRef, Digest: digestOf(r.digestRef),
		SpecHash: answer.SpecHash, IsPaused: answer.IsPaused, Created: status == http.StatusCreated,
		DagCreated: answer.DagCreated != nil && *answer.DagCreated, Status: deployStatusRegistered}
	if status == http.StatusOK {
		// 200 rather than 201: the server already had this exact spec and made
		// no version. Reached when the pre-check was skipped (--force) or could
		// not be made, so the label may not be the current version.
		res.Status = deployStatusUnchanged
		_, werr := fmt.Fprintf(out,
			"warning: %s was already registered as version %s with this exact content; nothing new was registered, and it may not be the current version\n",
			spec.DagID, r.version)
		return res, werr
	}
	_, werr := fmt.Fprintf(out,
		"Deployed %s -> %s\n  image %s\n  registered version %s\n", spec.DagID, r.serverURL, r.digestRef, r.version)
	return res, werr
}

// triggerDeployRun kicks a manual run of the just-registered DAG, the --trigger
// opt-in (deploy registers only by default). It posts to the Airflow-compatible
// trigger endpoint with a unique manual run id, and returns the run id the
// server answers with (the one it was sent when the answer names none).
func triggerDeployRun(ctx context.Context, out io.Writer, serverURL, token, dagID string) (string, error) {
	runID := fmt.Sprintf("manual__%d", time.Now().UTC().Unix())
	payload := []byte(fmt.Sprintf(`{"dag_run_id":%q}`, runID))
	url := strings.TrimRight(serverURL, "/") + "/api/v2/dags/" + dagID + "/dagRuns"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("building trigger request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("triggering run for %q: %w", dagID, err)
	}
	raw, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return "", fmt.Errorf("reading trigger response: %w", readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("closing trigger response: %w", closeErr)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("trigger returned %d: %s", resp.StatusCode, string(raw))
	}
	var answer struct {
		DagRunID string `json:"dag_run_id"`
	}
	if json.Unmarshal(raw, &answer) == nil && answer.DagRunID != "" {
		runID = answer.DagRunID
	}
	_, err = fmt.Fprintf(out, "  triggered run %s\n", runID)
	return runID, err
}
