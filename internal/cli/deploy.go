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
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// registryFlagUsage is the help of --registry for compile and build, which
// derive the image from the same local sources as deploy.
const registryFlagUsage = "image registry prefix to push to, over every other source (then DEXAFLOW_REGISTRY, dexaflow.yaml, the target, the workspace defaults, the config file)"

// deployRegistryFlagUsage is registryFlagUsage for deploy, which also reads
// the deploy defaults the cluster advertises.
const deployRegistryFlagUsage = "image registry prefix to push to, over every other source (then DEXAFLOW_REGISTRY, dexaflow.yaml, the target, the workspace defaults, the config file, the cluster's deploy defaults)"

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
	rebuild    bool
	paused     bool
	active     bool
	plan       bool
	output     string
	// changedSince is the git ref of --changed-since: deploy only the
	// workspace's projects whose image inputs differ from it.
	changedSince string
	follow       bool
	timeout      time.Duration
	// cluster caches the deploy defaults each server advertises for the
	// length of the command; nil reads them on every deploy.
	cluster *clusterDefaultsCache
	// report collects each DAG's result for --output json; nil for text.
	report *deployReport
	// watch is --watch: redeploy on every save until Ctrl-C.
	watch bool
	// watching is the state of a --watch session; nil without --watch.
	watching *watchState
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

// labelOrigin says where the deploy's version label comes from: the
// --dag-version flag, or the default content label.
func (o deployOptions) labelOrigin() labelOrigin {
	if o.dagVersion != "" {
		return labelExplicit
	}
	return labelDefault
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
			if cmd.Flags().Changed("changed-since") && o.changedSince == "" {
				return fmt.Errorf("--changed-since needs a git ref (a commit, branch or tag); it was given an empty value, as an unset CI variable gives")
			}
			return runDeployCommand(cmd, args, o)
		},
	}
	cmd.Flags().BoolVar(&o.all, "all", false, "deploy every DAG project in the workspace")
	cmd.Flags().BoolVar(&o.skipBuild, "skip-build", false, "register the image already in the registry (no build or push; the digest is read from the registry) but still recompile dag.json from dexaflow.yaml/dag.py")
	cmd.Flags().BoolVar(&o.trigger, "trigger", false, "trigger a run immediately after registering")
	addFollowFlags(cmd, &o.follow, &o.timeout)
	cmd.Flags().BoolVarP(&o.yes, "yes", "y", false, "skip the confirmation prompt (for automation)")
	cmd.Flags().BoolVar(&o.force, "force", false, "build, push and register even when the default content label is already deployed")
	cmd.Flags().BoolVar(&o.rebuild, "rebuild", false, "build and push the image even when the registry already holds one for this content label, and build its dependency image again")
	cmd.Flags().StringVar(&o.serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&o.token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	cmd.Flags().String("registry", "", deployRegistryFlagUsage)
	cmd.Flags().Bool(noSourceModeFlag, false, "build and push an image even when a target, the workspace defaults or the cluster set source_mode_image")
	cmd.Flags().StringVar(&o.builder, "builder", "docker", "image build tool to shell out to (e.g. docker, podman, nerdctl)")
	cmd.Flags().StringVar(&o.dockerfile, "dockerfile", "Dockerfile", "Dockerfile path relative to the DAG directory")
	cmd.Flags().StringVar(&o.dagVersion, "dag-version", "", "DAG version label (default: git describe of the project directory, or dev outside git, plus a hash of the DAG's content)")
	cmd.Flags().BoolVar(&o.paused, "paused", false, "leave the DAG paused after this deploy, new or not (default: a new DAG follows is_paused_upon_creation, else the server's dags_are_paused_at_creation; a redeploy keeps is_paused)")
	cmd.Flags().BoolVar(&o.active, "active", false, "leave the DAG active (unpaused) after this deploy, new or not")
	cmd.MarkFlagsMutuallyExclusive("paused", "active")
	cmd.Flags().BoolVar(&o.plan, "plan", false, "say per DAG what would be built, pushed and registered, and why, without doing any of it")
	cmd.Flags().StringVar(&o.changedSince, "changed-since", "", "with --all, deploy only the projects whose image or version label inputs differ between this git ref and the working tree (uncommitted files included); the others are skipped and listed")
	cmd.Flags().BoolVar(&o.watch, "watch", false, "keep running after the deploy and redeploy each project when one of its files is saved, until Ctrl-C (asks once, applies --paused or --active to the first deploy only)")
	cmd.Flags().StringVar(&o.output, "output", outputText, "output format: text, or json for one JSON document on stdout (human lines go to stderr)")
	addPlatformFlag(cmd)
	return cmd
}

// runDeployTarget resolves what to deploy from the args and flags
// (resolveDeployTarget) and deploys it.
func runDeployTarget(cmd *cobra.Command, args []string, o deployOptions) error {
	if err := checkDeployFollow(o); err != nil {
		return err
	}
	dir, all, err := resolveDeployTarget(cmd, args, o)
	if err != nil {
		return err
	}
	return deploySelection(cmd, dir, all, o)
}

// deploySelection deploys what resolveDeployTarget chose: the workspace when
// all is set, else the project at dir.
func deploySelection(cmd *cobra.Command, dir string, all bool, o deployOptions) error {
	if all {
		return deployAll(cmd, o)
	}
	return runDeploy(cmd, dir, o)
}

// resolveDeployTarget decides what a deploy deploys from the args and flags:
// --all (every project in the workspace, all set), a directory path, or a
// dag_id (resolved to its subdir in the workspace). A bare invocation is
// deployHere's choice.
func resolveDeployTarget(cmd *cobra.Command, args []string, o deployOptions) (dir string, all bool, err error) {
	if o.changedSince != "" && len(args) > 0 {
		return "", false, fmt.Errorf("--changed-since selects among the workspace's projects, so it goes with --all; " +
			"to deploy a named DAG or folder, drop --changed-since")
	}
	if o.all {
		if len(args) > 0 {
			return "", false, fmt.Errorf("--all deploys the whole workspace; do not also pass a path or dag_id")
		}
		return "", true, nil
	}
	if len(args) == 0 {
		return deployHere(o)
	}
	target := args[0]
	if info, serr := os.Stat(target); serr == nil && info.IsDir() {
		return target, false, nil
	}
	ws, werr := dagLookupWorkspace(cmd)
	if werr != nil {
		return "", false, werr
	}
	printWorkspace(cmd, ws)
	dir, rerr := resolveProjectDir(ws.dir, target)
	if rerr != nil {
		return "", false, rerr
	}
	return dir, false, nil
}

// deployHere is what a bare `dexaflow deploy` deploys: the project in the
// current folder, or, at a workspace root (which is not a project itself),
// every DAG of the workspace (all set). Any other folder that is not a
// project is an error that says how to deploy one DAG or all of them.
func deployHere(o deployOptions) (dir string, all bool, err error) {
	if isProjectDir(".") {
		if o.changedSince != "" {
			return "", false, fmt.Errorf("--changed-since selects among the workspace's projects: pass --all to deploy the changed ones, or drop --changed-since to deploy this project")
		}
		return ".", false, nil
	}
	ws, err := findWorkspaceFromCwd()
	if err != nil {
		return "", false, err
	}
	if ws != nil && sameDir(".", ws.Root) {
		return "", true, nil
	}
	wd, _ := os.Getwd() //nolint:errcheck // only names the folder in the message.
	return "", false, fmt.Errorf("%s is not a DAG project (no %s or dag.py).\n"+
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
// (content-addressed images make a re-deploy idempotent). ADR 0041. With
// --changed-since it deploys only the projects that changed since the ref.
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
	out := cmd.OutOrStdout()
	choice, _, cerr := deployServerChoice(cmd, o)
	if cerr != nil {
		return cerr
	}
	projects, skipped, serr := selectChanged(cmd, ws, o)
	if serr != nil {
		return serr
	}
	if len(projects) == 0 {
		o.report.setServer(choice)
		_, err = fmt.Fprintf(out, "No DAG project changed since %s; nothing to deploy (%d skipped)\n", o.changedSince, skipped)
		return err
	}
	if !o.plan && shouldConfirm(choice.URL, o.yes, cmdInteractive(cmd)) {
		target := fmt.Sprintf("%d DAG(s) from %s", len(projects), wsDir)
		if !confirmDeploy(cmd.InOrStdin(), out, target, choice.URL) {
			return fmt.Errorf("deploy aborted")
		}
	}
	o.yes = true // confirmed once for the whole workspace; no per-project prompts
	if failed := deployProjects(cmd, projects, o); len(failed) > 0 {
		return fmt.Errorf("%d of %d deploys failed: %s",
			len(failed), len(projects), strings.Join(failed, ", "))
	}
	return printDeployAllSummary(out, o, len(projects), skipped, wsDir)
}

// deployProjects deploys each project with runDeploy, in order and best
// effort, as deploy --all does, and returns the dag_ids whose deploy failed;
// each failure is also recorded in o.report. It prompts for nothing beyond
// what runDeploy does with o.yes.
func deployProjects(cmd *cobra.Command, projects []Project, o deployOptions) []string {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	verb := "deploying"
	if o.plan {
		verb = "planning"
	}
	var failed []string
	for _, p := range projects {
		devPrintf(out, "==> %s %s (%s)\n", verb, p.DagID, p.Path)
		if derr := runDeploy(cmd, p.Path, o); derr != nil {
			failed = append(failed, p.DagID)
			o.report.addFailure(p.DagID, derr)
			devPrintf(errOut, "    FAILED %s: %v\n", p.DagID, derr)
		}
	}
	return failed
}

// printDeployAllSummary writes the closing line of deploy --all.
func printDeployAllSummary(out io.Writer, o deployOptions, n, skipped int, wsDir string) error {
	tail := ""
	if o.changedSince != "" {
		tail = fmt.Sprintf(" (%d unchanged since %s, skipped)", skipped, o.changedSince)
	}
	if o.plan {
		_, err := io.WriteString(out, strings.TrimSuffix(planSummary(n, wsDir), "\n")+tail+"\n")
		return err
	}
	_, err := fmt.Fprintf(out, "Deployed %d DAG(s) from %s%s\n", n, wsDir, tail)
	return err
}

// selectChanged returns the workspace's projects deploy --all deploys: every
// one, or with --changed-since those that changed since the ref. It prints
// and records each skipped project with its reason, and each project removed
// since the ref, and returns how many were skipped.
func selectChanged(cmd *cobra.Command, ws *WorkspaceSpec, o deployOptions) ([]Project, int, error) {
	if o.changedSince == "" {
		return ws.Projects, 0, nil
	}
	ctx, out := cmdContext(cmd), cmd.OutOrStdout()
	cs, err := readChangeSet(ctx, ws.Path, o.changedSince)
	if err != nil {
		return nil, 0, err
	}
	shared := cs.sharedChange(ws.Path)
	var keep []Project
	for _, p := range ws.Projects {
		verdict := projectChange{changed: true, reason: shared + " changed since " + cs.ref}
		if shared == "" {
			verdict = cs.projectChanged(ctx, p, o.dockerfile)
		}
		if verdict.changed {
			devPrintf(out, "==> %s: %s\n", p.DagID, verdict.reason)
			keep = append(keep, p)
			continue
		}
		devPrintf(out, "==> skipping %s (%s)\n", p.DagID, verdict.reason)
		o.report.add(deployResult{DagID: p.DagID, Status: deployStatusSkipped, Reason: verdict.reason})
	}
	current := map[string]string{}
	for _, p := range ws.Projects {
		if dir, derr := resolvedDir(p.Path); derr == nil {
			current[p.DagID] = dir
		}
	}
	removed, moved := cs.removedProjects(ctx, ws.Path, current)
	for _, m := range moved {
		devPrintf(out, "==> %s moved to %s since %s: %s is deployed from there when it changed\n", m.From, m.To, cs.ref, m.DagID)
		o.report.addMoved(m)
	}
	for _, r := range removed {
		devPrintf(out, "==> %s (%s) %s. If its DAG should go, remove it with `dexaflow dags delete %s --deregister`\n",
			r.Path, r.DagID, r.Reason, r.DagID)
		o.report.addRemoved(r)
	}
	return keep, len(ws.Projects) - len(keep), nil
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
      the whole cluster:          deploy.defaults.registry in the server's Helm values

  The image name defaults to the dag_id. Then give the CLI a credential for the registry:
%s`, registryLoginHints())
}

// registryLoginHints lists the credential each kind of registry takes
// (ADR 0080): the cloud CLI's session for Artifact Registry, GCR, ECR and
// ACR, the docker config for any other. With no registry configured there is
// no host to name the exact command for, as the preflight does.
func registryLoginHints() string {
	session := func(p string) string { return build.CloudRegistry{Provider: p}.SessionCommand() }
	return "      Artifact Registry, GCR: " + session(build.ProviderGoogle) + "\n" +
		"      ECR: " + session(build.ProviderAWS) + "\n" +
		"      ACR: " + session(build.ProviderAzure) + "\n" +
		"      any other registry: dexaflow registry login <registry> (or docker login <registry>)\n" +
		"  A build by a local container tool pushes with that tool's own login; the deploy preflight names it."
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
// pickTokenFor (--token, DEXAFLOW_TOKEN or LEOFLOW_TOKEN, the session saved for
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
	return resolveServerChoiceTokenFor(cmd, serverFlag, tokenFlag, false)
}

// resolveServerChoiceTokenFor is resolveServerChoiceToken; ownOnly sends the
// server only a token saved for it (pickTokenFor), for `status --all-targets`.
func resolveServerChoiceTokenFor(cmd *cobra.Command, serverFlag, tokenFlag string, ownOnly bool) (choice *serverChoice, token string, err error) {
	choice, err = resolveServer(cmd, serverFlag)
	if err != nil {
		return nil, "", err
	}
	serverURL := choice.URL
	token, source := pickTokenFor(choice, tokenFlag, ownOnly)
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

// loadDeployProject loads and validates the project config of a deploy.
func loadDeployProject(dir string) (*domain.LeoflowConfig, error) {
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		return nil, err
	}
	if verr := cfg.Validate(); verr != nil {
		return nil, fmt.Errorf("invalid %s: %w", projectConfigPath(dir), verr)
	}
	return cfg, nil
}

// settleDeployConfig fills what the project leaves unset from the workspace,
// the target, the user's config and the deploy defaults the cluster
// advertises (cluster, nil when none; applyDeployDefaults), and enforces the
// registry a Pro deploy pushes to, unless it runs in source mode, which
// pushes nothing. It returns where each value came from.
func settleDeployConfig(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, cluster *defaultsLayer) (defaultsProvenance, error) {
	prov, err := applyDeployDefaults(cmd, dir, cfg, cluster)
	if err != nil {
		return defaultsProvenance{}, err
	}
	if prov.sourceMode.on() {
		return prov, nil
	}
	if rerr := requireRegistry(cfg); rerr != nil {
		return defaultsProvenance{}, rerr
	}
	return prov, nil
}

// deploySettings says, before anything is built, where the deploy goes,
// where each setting it did not get from the project in dir came from, and
// what the cluster advertises about where its tasks run.
func deploySettings(choice *serverChoice, dir string, cfg *domain.LeoflowConfig, prov defaultsProvenance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "server:     %s (%s)\n", choice.URL, choice.Source)
	if sm := prov.sourceMode; sm.on() {
		// Nothing is built or pushed: no registry, platform or base image.
		fmt.Fprintf(&b, "image:      %s (source mode, from %s): dag.py runs on this runtime image, nothing is built or pushed\n", sm.image, sm.source)
	} else {
		fmt.Fprintf(&b, "registry:   %s/%s (from %s)\n", cfg.Registry.URL, cfg.Registry.ImageName, prov.registrySource)
		fmt.Fprintf(&b, "platforms:  %s (from %s)\n", strings.Join(cfg.Build.Platforms, ","), orDefault(prov.platformsSource))
		if cfg.BaseImage != "" {
			fmt.Fprintf(&b, "base image: %s (from %s)\n", cfg.BaseImage, prov.baseImageSource)
		}
	}
	b.WriteString(clusterExecutionLines(dir, cfg, prov.cluster))
	return b.String()
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
// by digest into dag.json, and register that with the control plane. In
// source mode it registers dag.py on the runtime image instead, with no
// build or push. With --plan it stops after deciding what it would do.
func runDeploy(cmd *cobra.Command, dir string, o deployOptions) (err error) {
	cfg, err := loadDeployProject(dir)
	if err != nil {
		return err
	}
	// A failure past this point is reported for its DAG in --output json.
	defer func() {
		if err != nil {
			o.report.addFailure(cfg.DagID, err)
		}
	}()
	// Read from the project's own config, before any default fills it in,
	// and checked against a local source mode setting before the server is
	// asked anything.
	scan := scanSourceMode(dir, cfg)
	if lerr := localSourceMode(cmd, dir).check(dir, scan.needs); lerr != nil {
		return lerr
	}
	choice, token, serr := deployServerChoice(cmd, o)
	if serr != nil {
		// With no server there are no cluster defaults: a project that has
		// no registry either still gets the registry error first.
		if _, rerr := settleDeployConfig(cmd, dir, cfg, nil); rerr != nil {
			return rerr
		}
		return serr
	}
	cluster := o.cluster.layer(cmdContext(cmd), cmd.ErrOrStderr(), choice.URL, token)
	prov, perr := settleDeployConfig(cmd, dir, cfg, cluster)
	if perr != nil {
		return perr
	}
	if merr := prov.sourceMode.check(dir, scan.needs); merr != nil {
		// The cluster's setting: read with the deploy defaults.
		return merr
	}
	if ferr := prov.sourceMode.checkFlags(cmd, scan, !o.watching.redeploy()); ferr != nil {
		return ferr
	}
	o.report.setServer(choice)
	serverURL := choice.URL
	settings := deploySettings(choice, dir, cfg, prov)
	if !o.watching.redeploy() {
		// A redeploy of a watch prints them when it deploys (watchSkip).
		o.watching.noteSettings(dir, settings)
		if _, werr := io.WriteString(cmd.OutOrStdout(), settings); werr != nil {
			return werr
		}
		warnAuthMethod(cmd.ErrOrStderr(), cfg)
	}
	if !o.plan && shouldConfirm(serverURL, o.yes, cmdInteractive(cmd)) &&
		!confirmDeploy(cmd.InOrStdin(), cmd.OutOrStdout(), dir, serverURL) {
		return fmt.Errorf("deploy aborted")
	}
	d := &deployRun{cmd: cmd, dir: dir, o: o, cfg: cfg, serverURL: serverURL, token: token, cluster: cluster, settings: settings,
		sourceMode: prov.sourceMode}
	return d.run()
}

// deployRun is one DAG's deploy once its config and server are resolved.
type deployRun struct {
	cmd              *cobra.Command
	dir              string
	o                deployOptions
	cfg              *domain.LeoflowConfig
	serverURL, token string
	// cluster is the deploy defaults layer cfg was settled with, nil for
	// none; the compile applies it again so it builds what was labeled.
	cluster *defaultsLayer
	// wasPaused is the DAG's is_paused before an --active deploy, so the
	// summary can describe the backlog it releases.
	wasPaused bool
	// settings is what deploySettings says about this deploy.
	settings string
	// sourceMode is the runtime image of a source mode deploy, which
	// registers dag.py on it with no build or push; zero for a build.
	sourceMode sourceModeChoice
	// preflightWarnings is what the dry run of the register warned about,
	// already printed.
	preflightWarnings []string
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
	origin := d.o.labelOrigin()
	output := deployOutput(d.dir)
	var built build.Result
	ctx, out := cmdContext(d.cmd), d.cmd.OutOrStdout()
	// Where this deploy builds the version (#1702); the build adds the
	// builder and the base image.
	provenance := collectBuildInfo(ctx, d.dir, os.Getenv)
	co := d.compileOptions(output, &built, &provenance)
	// Resolved here rather than inside runCompile because the digest lookup
	// below needs the same reference; runCompile then takes both as given.
	image, ierr := d.labelAndImage(&co)
	if ierr != nil {
		return ierr
	}
	co.image = image
	version := co.dagVersion
	co.reuse = d.mayReuse(image, version, origin)
	co.rebuild = d.o.rebuild
	if skip, werr := d.watchSkip(ctx, version); skip || werr != nil {
		return werr
	}
	if d.o.plan {
		return d.plan(ctx, version, image, origin)
	}
	access, aerr := d.checkServer(ctx)
	if aerr != nil {
		return aerr
	}
	// Only a content label says anything about content; --force skips it.
	if done, perr := preDeployCheck(ctx, d.cmd.ErrOrStderr(), out, d.serverURL, d.token, d.cfg.DagID, version,
		origin == labelDefault && !d.o.force, d.o.watching != nil); done || perr != nil {
		if perr != nil {
			return perr
		}
		return d.alreadyCurrent(ctx, version)
	}
	refused, rerr := d.checkRegistry(ctx, image, co.reuse)
	if rerr != nil {
		return rerr
	}
	co.pushRefused = refused
	co.beforeBuild = d.beforeBuild(ctx, access, output, image, version, origin, co.reuse)
	if cerr := runCompile(d.cmd, d.dir, co); cerr != nil {
		return cerr
	}
	digestRef, derr := d.registeredImage(image, built)
	if derr != nil {
		return derr
	}
	d.readStateBefore(ctx)
	res, rerr := registerDeployedDAG(ctx, out, registerRequest{serverURL: d.serverURL, token: d.token, output: output,
		digestRef: digestRef, version: version, origin: origin, paused: d.o.pausedOverride(), warn: d.cmd.ErrOrStderr(), shown: d.preflightWarnings,
		build: provenance})
	if rerr != nil {
		return rerr
	}
	surfaceConnections(out, output)
	return d.finish(ctx, res)
}

// compileOptions are the options of this deploy's compile into output: a
// build and push of the image (built receives the builder's result, and
// provenance says where it was built), none with --skip-build, and in source
// mode a compile on the runtime image only.
func (d *deployRun) compileOptions(output string, built *build.Result, provenance *domain.BuildInfo) compileOptions {
	building := !d.o.skipBuild && !d.sourceMode.on()
	return compileOptions{
		cluster:    d.cluster,
		output:     output,
		image:      d.sourceMode.image,
		builder:    d.o.builder,
		dockerfile: d.o.dockerfile,
		dagVersion: d.o.dagVersion,
		build:      building,
		push:       building,
		sourceMode: d.sourceMode.on(),
		// deploySettings showed the platforms; the build pins its push.
		settingsShown: true,
		built:         built,
		requirePin:    true,
		provenance:    provenance,
	}
}

// beforeBuild is what runs once dag.json is compiled and before anything is
// built: in source mode the check of the source the server will make
// (checkSourceModeSpec), then the server's dry run of the register when it
// offers one, then the task pod step (checkTaskPods) with what the dry run
// reported; nil when there is nothing to run.
func (d *deployRun) beforeBuild(ctx context.Context, access serverAccess, output, image, version string, origin labelOrigin, reuse bool) func() error {
	if dockerfile, _ := d.ownDockerfile(); !d.sourceMode.on() && !access.dryRun && dockerfile == "" {
		return nil
	}
	return func() error {
		if d.sourceMode.on() {
			if err := checkSourceModeSpec(output, image); err != nil {
				return err
			}
		}
		var pods *taskPodsAnswer
		if access.dryRun {
			answer, err := d.dryRun(ctx, output, image, version, origin, reuse)
			if err != nil {
				return err
			}
			if answer != nil {
				pods = answer.TaskPods
			}
		}
		return d.checkTaskPods(pods)
	}
}

// registeredImage is the image the register names: the pushed image pinned
// by digest (deployedDigest), or in source mode the runtime image as it is.
func (d *deployRun) registeredImage(image string, built build.Result) (string, error) {
	if d.sourceMode.on() {
		return image, nil
	}
	return deployedDigest(d.cmd, registryFor(cmdContext(d.cmd), d.cfg), image, built)
}

// labelAndImage fills co.dagVersion with the default content label
// (deployContentLabel) when --dag-version was not given, and returns the
// image reference (resolveVersionAndImage).
func (d *deployRun) labelAndImage(co *compileOptions) (string, error) {
	if co.dagVersion == "" {
		label, err := deployContentLabel(d.cmd, d.dir, d.cfg, d.o.dockerfile, d.sourceMode.image)
		if err != nil {
			return "", err
		}
		co.dagVersion = label
	}
	return resolveVersionAndImage(d.cmd, d.dir, co, d.cfg)
}

// deployOutput is where a deploy of the project at dir compiles its dag.json.
func deployOutput(dir string) string {
	return filepath.Join(dir, "dag.json")
}

// deployContentLabel is the default content label a deploy of the project at
// dir registers: cfg is its config settled for the server deployed to
// (settleDeployConfig, the cluster's deploy defaults included), and
// dockerfile is the --dockerfile flag, and sourceModeImage the runtime image
// of a source mode deploy ("" for a build), which the label hashes.
// `dexaflow status` computes the local label with it too, so status and
// deploy cannot disagree on a DAG's label.
func deployContentLabel(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, dockerfile, sourceModeImage string) (string, error) {
	return contentLabel(cmd, dir, compileOptions{output: deployOutput(dir), dockerfile: dockerfile,
		sourceMode: sourceModeImage != "", image: sourceModeImage}, cfg)
}

// checkServer is the first step of the preflight (#1705): the control plane
// answers, and the token may deploy, before anything is compiled.
func (d *deployRun) checkServer(ctx context.Context) (serverAccess, error) {
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	access, err := checkServerAccess(ctx, d.cmd.ErrOrStderr(), d.serverURL, d.token, d.cfg.DagID, d.o.pausedOverride())
	if err != nil {
		return serverAccess{}, err
	}
	_, werr := fmt.Fprintf(d.cmd.OutOrStdout(), "preflight:  control plane %s answers, the token may deploy\n", d.serverURL)
	return access, werr
}

// checkRegistry is the push step of the preflight: the registry accepts a
// push of image, checked unless nothing will be pushed (--skip-build, source
// mode, or a reusable image the registry already holds). With a remote
// builder configured, a refusal is not decided here: it is returned as
// refused, and the build fails on it unless the builder it chooses pushes
// nothing from this machine (checkLocalPush).
func (d *deployRun) checkRegistry(ctx context.Context, image string, reuse bool) (refused, err error) {
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	if d.o.skipBuild || d.sourceMode.on() {
		return nil, nil
	}
	reg := registryFor(ctx, d.cfg)
	if reuse && registryHolds(ctx, reg, image) {
		return nil, nil
	}
	err = checkRegistryPush(ctx, d.cmd.OutOrStdout(), d.cmd.ErrOrStderr(), reg, image, d.o.builder)
	if err == nil || remoteFor(ctx, d.cfg) == nil {
		return nil, err
	}
	return err, nil
}

// dryRun is the server's dry run of the compiled spec at output, before the
// build, and its answer (nil when there is none to read). reuse says the
// compile takes the skip path when the registry holds the image.
func (d *deployRun) dryRun(ctx context.Context, output, image, version string, origin labelOrigin, reuse bool) (*dryRunAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	answer, err := serverDryRun(ctx, d.cmd.OutOrStdout(), d.cmd.ErrOrStderr(), versionPost{serverURL: d.serverURL, token: d.token,
		dagID: d.cfg.DagID, paused: d.o.pausedOverride()}, output, d.dryRunImage(ctx, image, reuse), version, origin)
	if answer != nil {
		d.preflightWarnings = answer.Warnings
	}
	return answer, err
}

// dryRunImage is the image the dry run is sent. The register gets the image
// pinned by digest. When nothing is built (--skip-build, or the skip path of
// a reused content label the registry already holds), that is the digest the
// registry holds for the tag, read here, and it decides the image checks and
// a conflict as for the register. In source mode it is the runtime image
// itself, which the register gets too. Before a build the digest does not exist
// yet: the repository at a placeholder digest, so an allowlist of
// repositories or of repo@* decides as for the register, and a refusal of
// the image itself or a conflict is left to the register.
func (d *deployRun) dryRunImage(ctx context.Context, image string, reuse bool) dryImage {
	if d.sourceMode.on() {
		// The runtime image is registered as it is.
		return dryImage{ref: image, decides: true}
	}
	if d.o.skipBuild || reuse {
		if p, err := registryFor(ctx, d.cfg).Resolve(ctx, image); err == nil {
			return dryImage{ref: p.Ref, decides: true}
		}
	}
	return dryImage{ref: build.PinnedRef(image, placeholderDigest)}
}

// mayReuse reports whether this deploy may take the skip path: the tag is the
// content label (reusableImage), and neither --rebuild nor --force asks for a
// build.
func (d *deployRun) mayReuse(image, version string, origin labelOrigin) bool {
	return reusableImage(image, version, origin) && !d.o.rebuild && !d.o.force
}

// reusableImage reports whether the image tag is the default content label,
// so an image the registry holds under it was built from this deploy's
// inputs and can be pinned without a build (ADR 0076, the skip path of
// ADR 0073). An explicit --dag-version, or a tag from git (tag_strategy
// git_sha or git_tag), names no content: a dirty tree or a moved tag would
// reuse an image of other sources. A git strategy that fell back to the label (outside
// git, no tag on HEAD) tags with the label, and so may reuse.
func reusableImage(image, version string, origin labelOrigin) bool {
	return origin == labelDefault && version != "" && strings.HasSuffix(image, ":"+version)
}

// plan says what a deploy of this DAG would do, and records it.
func (d *deployRun) plan(ctx context.Context, version, image string, origin labelOrigin) error {
	in := planInput{serverURL: d.serverURL, token: d.token, dagID: d.cfg.DagID, version: version, image: image,
		origin: origin, skipBuild: d.o.skipBuild, sourceMode: d.sourceMode.on(), force: d.o.force, paused: d.o.pausedOverride(),
		reuse: d.mayReuse(image, version, origin), platforms: buildPlatforms(d.cfg), registry: registryFor(ctx, d.cfg)}
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
	d.o.watching.deployed(d.dir, res.Version)
	if trigger := deployTrigger(ctx, out, d.serverURL, d.token, d.o); trigger != nil {
		return trigger(res.DagID)
	}
	return nil
}

// preDeployCheck runs checkDeployed for a content label (when enabled) and
// acts on it: done when the label is already the current version (nothing is
// built, pushed or registered; the caller finishes the deploy), an error when
// the label is an older version or the check was refused for authentication.
// watching (deploy --watch, which refuses --dag-version) leaves the
// --dag-version way out of that error.
func preDeployCheck(ctx context.Context, warn, out io.Writer, serverURL, token, dagID, version string, enabled, watching bool) (done bool, err error) {
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
		if watching {
			return false, fmt.Errorf("%s content matches version %s, which is not the current version (%s). "+
				"To make it current again, run `dexaflow rollback %s --to %s`; the watch then sees this content as deployed",
				dagID, version, current, dagID, version)
		}
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
	// warn receives the warning that the server did not apply paused, and
	// the register's own warnings.
	warn io.Writer
	// shown is the warnings the preflight's dry run already printed, which
	// the register's answer repeats and the deploy does not print again.
	shown []string
	// build is where the version was built, sent with the register
	// (#1702); its zero value sends nothing.
	build domain.BuildInfo
}

// registerAnswer is the part of the register response the deploy reports.
// IsPaused and DagCreated are nil from a server that does not report them.
type registerAnswer struct {
	SpecHash   string `json:"spec_hash"`
	IsPaused   *bool  `json:"is_paused"`
	DagCreated *bool  `json:"dag_created"`
	// Execution is where each task runs (#1707); nil from an older server.
	Execution *executionSummary `json:"execution"`
	// Warnings is what the register let through with a warning (#1703).
	Warnings []string `json:"warnings"`
}

// printNewWarnings prints on w each of the server's warnings that is not in
// shown, the ones already printed.
func printNewWarnings(w io.Writer, warnings, shown []string) {
	if w == nil {
		return
	}
	for _, msg := range warnings {
		if !slices.Contains(shown, msg) {
			//nolint:errcheck // advisory: the register succeeded
			fmt.Fprintf(w, "warning: the server: %s\n", msg)
		}
	}
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
	post := versionPost{serverURL: r.serverURL, token: r.token, dagID: spec.DagID, spec: repinned, paused: r.paused}
	if !r.build.IsZero() {
		post.build = &r.build
	}
	status, header, raw, uerr := postVersion(ctx, post)
	if uerr != nil {
		return deployResult{}, uerr
	}
	if err := deployViaIssuerError(r.serverURL, status, header, raw); err != nil {
		return deployResult{}, err
	}
	if status >= http.StatusMultipleChoices {
		return deployResult{}, versionConflictHint(apiStatusError(status, raw), r.version, r.origin)
	}
	var answer registerAnswer
	_ = json.Unmarshal(raw, &answer) //nolint:errcheck // an older server's body may say less; the register itself succeeded
	warnPausedIgnored(r.warn, r.paused, answer.IsPaused)
	printNewWarnings(r.warn, answer.Warnings, r.shown)
	extras := readServerExtras(raw)
	res := deployResult{DagID: spec.DagID, Version: r.version, Image: r.digestRef, Digest: digestOf(r.digestRef),
		SpecHash: answer.SpecHash, IsPaused: answer.IsPaused, Created: status == http.StatusCreated,
		DagCreated: answer.DagCreated != nil && *answer.DagCreated, Status: deployStatusRegistered, Execution: answer.Execution,
		Links: extras.Links, Checks: extras.Checks}
	if status == http.StatusOK {
		// 200 rather than 201: the server already had this exact spec and made
		// no version. Reached when the pre-check was skipped (--force) or could
		// not be made, so the label may not be the current version.
		res.Status = deployStatusUnchanged
		_, werr := fmt.Fprintf(out,
			"warning: %s was already registered as version %s with this exact content; nothing new was registered, and it may not be the current version\n%s%s",
			spec.DagID, r.version, executionLines(answer.Execution), extras.lines())
		return res, werr
	}
	_, werr := fmt.Fprintf(out,
		"Deployed %s -> %s\n  image %s\n  registered version %s\n%s%s", spec.DagID, r.serverURL, r.digestRef, r.version, executionLines(answer.Execution), extras.lines())
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
	resp, err := cliHTTPClient(30 * time.Second).Do(req)
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
