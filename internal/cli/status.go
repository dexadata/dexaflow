package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/envcompat"
	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// Exit codes of `dexaflow status`, besides 0 when every DAG is in sync.
const (
	// statusExitNotInSync: a DAG drifted, is behind, or is not deployed.
	statusExitNotInSync = 1
	// statusExitError: a target or a DAG could not be read, or the command
	// was used wrongly.
	statusExitError = 2
)

// statusReadTimeout bounds each read status makes of a control plane.
const statusReadTimeout = 15 * time.Second

// statusOptions holds the flags of `dexaflow status`.
type statusOptions struct {
	serverURL  string
	token      string
	allTargets bool
	output     string
	dockerfile string
}

// newStatusCommand builds `dexaflow status [path | dag_id ...]`: per DAG of
// the workspace, the version current on a target, whether the local content
// is that version, is_paused and the last run (#1704). It only reads: it
// never builds, pushes, registers or prompts.
func newStatusCommand() *cobra.Command {
	var o statusOptions
	cmd := &cobra.Command{
		Use:   "status [path | dag_id ...]",
		Short: "Show per DAG the version current on a target, its last run, and drift from the workspace.",
		Long: "Reports, for each DAG of the workspace (or the DAGs named by dag_id or project folder), the " +
			"version current on the target (its label and when it was registered), whether the local content " +
			"is that version (in sync), an older registered version (behind) or neither (drift), is_paused " +
			"and the last run. A DAG the server does not know is \"not deployed\".\n\n" +
			"The local label is computed exactly as `dexaflow deploy` computes it, with the target's and the " +
			"cluster's deploy defaults, so \"in sync\" means a deploy would build and register nothing. Pass the " +
			"--registry, --platform and --dockerfile a deploy is given, since they are part of the label.\n\n" +
			"Nothing is built, pushed or registered, and nothing is asked.\n\n" +
			"Exit codes: 0 when every DAG is in sync, 1 when a DAG drifted, is behind or is not deployed, " +
			"2 when a target or a DAG could not be read.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, args, o)
		},
	}
	cmd.Flags().StringVar(&o.serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&o.token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: the token saved for the server)")
	cmd.Flags().BoolVar(&o.allTargets, "all-targets", false, "report every target of the workspace, each with its own server and saved token")
	cmd.Flags().StringVar(&o.output, "output", outputText, "output format: text, or json for one JSON document on stdout")
	cmd.Flags().String("registry", "", "the --registry a deploy is given, which is part of the content label (then DEXAFLOW_REGISTRY, dexaflow.yaml, the target, the workspace defaults, the config file, the cluster's deploy defaults)")
	cmd.Flags().StringVar(&o.dockerfile, "dockerfile", "Dockerfile", "the --dockerfile a deploy is given, relative to the DAG directory")
	addPlatformFlag(cmd)
	return cmd
}

// runStatus resolves the DAGs and the targets, reads each target and
// reports. The error carries the exit code.
func runStatus(cmd *cobra.Command, args []string, o statusOptions) error {
	if err := checkStatusOptions(cmd, o); err != nil {
		return &exitCodeError{code: statusExitError, err: err}
	}
	sel, err := statusSelection(cmd, args)
	if err != nil {
		return &exitCodeError{code: statusExitError, err: err}
	}
	names := []string{""}
	if o.allTargets {
		if names, err = workspaceTargetNames(); err != nil {
			return &exitCodeError{code: statusExitError, err: err}
		}
	}
	rep := &statusReport{Workspace: sel.workspace, Targets: []statusTarget{}}
	for _, name := range names {
		rep.Targets = append(rep.Targets, statusOfTarget(cmd, name, sel.projects, o))
	}
	rep.tally()
	if werr := writeStatusReport(cmd.OutOrStdout(), rep, o.output); werr != nil {
		return &exitCodeError{code: statusExitError, err: werr}
	}
	return rep.exitError()
}

// checkStatusOptions refuses the flags that cannot go together.
func checkStatusOptions(cmd *cobra.Command, o statusOptions) error {
	if o.output != outputText && o.output != outputJSON {
		return fmt.Errorf("--output must be %s or %s, not %q", outputText, outputJSON, o.output)
	}
	if !o.allTargets {
		return nil
	}
	serverURL := cmd.Flag("server-url")
	for _, c := range []struct {
		set  bool
		flag string
	}{
		{o.serverURL != "", "--server"},
		{serverURL != nil && serverURL.Changed, "--server-url"},
		{targetFlag(cmd) != "", "--target"},
	} {
		if c.set {
			return fmt.Errorf("--all-targets reads every target of the workspace; it cannot be combined with %s, which names one server", c.flag)
		}
	}
	if _, env := envcompat.Lookup("TOKEN"); env || o.token != "" {
		what := "--token"
		if env {
			what = "DEXAFLOW_TOKEN (or LEOFLOW_TOKEN)"
		}
		return fmt.Errorf("--all-targets sends each target the token saved for its server; %s would send one token to every target. "+
			"Unset it, or report one target with --target <name>", what)
	}
	return nil
}

// workspaceTargetNames lists the targets of the workspace around the
// current folder, for --all-targets.
func workspaceTargetNames() ([]string, error) {
	ws, err := findWorkspaceFromCwd()
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, fmt.Errorf("--all-targets reports the targets of a workspace, and there is no %s here or above", workspaceFileName)
	}
	names := ws.Spec.targetNames()
	if len(names) == 0 {
		return nil, fmt.Errorf("%s defines no targets; add one with `dexaflow target add <name> <url>`", ws.File)
	}
	return names, nil
}

// statusProject is one DAG status reports.
type statusProject struct {
	dagID string
	dir   string // absolute
}

// statusSelected is what status reports on: the DAGs, and the workspace
// they come from ("" for DAGs named by folder outside one).
type statusSelected struct {
	workspace string
	projects  []statusProject
}

// statusSelection picks the DAGs the way deploy does: with no argument, the
// project in the current folder, else every project of the workspace deploy
// --all reads; each argument is a project folder or a dag_id looked up as
// `deploy <dag_id>` looks it up.
func statusSelection(cmd *cobra.Command, args []string) (statusSelected, error) {
	if len(args) == 0 {
		if isProjectDir(".") {
			p, err := statusProjectAt(".")
			return statusSelected{projects: []statusProject{p}}, err
		}
		where, err := deployAllWorkspace(cmd)
		if err != nil {
			return statusSelected{}, err
		}
		ws, err := ResolveWorkspace(where.dir)
		if err != nil {
			return statusSelected{}, err
		}
		if len(ws.Projects) == 0 {
			return statusSelected{}, fmt.Errorf("no DAG projects found in workspace %s", ws.Path)
		}
		sel := statusSelected{workspace: ws.Path}
		for _, p := range ws.Projects {
			sel.projects = append(sel.projects, statusProject{dagID: p.DagID, dir: p.Path})
		}
		return sel, nil
	}
	return statusSelectArgs(cmd, args)
}

// statusSelectArgs resolves each argument to a project, once per dag_id.
func statusSelectArgs(cmd *cobra.Command, args []string) (statusSelected, error) {
	var sel statusSelected
	var lookup *deployWorkspace
	for _, arg := range args {
		dir := arg
		if info, err := os.Stat(arg); err != nil || !info.IsDir() {
			if lookup == nil {
				ws, werr := dagLookupWorkspace(cmd)
				if werr != nil {
					return statusSelected{}, werr
				}
				lookup = &ws
				sel.workspace = ws.dir
			}
			found, rerr := resolveProjectDir(lookup.dir, arg)
			if rerr != nil {
				return statusSelected{}, rerr
			}
			dir = found
		} else if !isProjectDir(arg) {
			return statusSelected{}, fmt.Errorf("%s is not a DAG project (no %s or dag.py)", arg, projectConfigFile)
		}
		p, err := statusProjectAt(dir)
		if err != nil {
			return statusSelected{}, err
		}
		if !slices.ContainsFunc(sel.projects, func(q statusProject) bool { return q.dagID == p.dagID }) {
			sel.projects = append(sel.projects, p)
		}
	}
	return sel, nil
}

// statusProjectAt reads the dag_id of the project at dir.
func statusProjectAt(dir string) (statusProject, error) {
	abs, err := resolvedDir(dir)
	if err != nil {
		return statusProject{}, err
	}
	cfg, err := loadDeployProject(abs)
	if err != nil {
		return statusProject{}, err
	}
	return statusProject{dagID: cfg.DagID, dir: abs}, nil
}

// statusOfTarget reports the projects against one target: name, or with ""
// the server any server command would call.
func statusOfTarget(cmd *cobra.Command, name string, projects []statusProject, o statusOptions) statusTarget {
	t := statusTarget{Target: name, Dags: []dagStatus{}}
	if name != "" {
		restore, err := useTargetFlag(cmd, name)
		if err != nil {
			t.Error = err.Error()
			return t
		}
		defer restore()
	}
	choice, token, err := resolveServerChoiceToken(cmd, o.serverURL, o.token)
	if err != nil {
		t.Error = err.Error()
		return t
	}
	t.Server, t.Source = choice.URL, choice.Source
	if choice.Target != nil {
		t.Target = choice.Target.Name
	}
	if name != "" {
		t.Source = "target " + name + ", from --all-targets"
	}
	if choice.Implicit && !choice.Lite {
		t.Error = "no control plane to report on: no --server, --target, DEXAFLOW_SERVER_URL or server_url is set; " +
			"name one with --target <name> or --server <url>, or add a target with `dexaflow target add <name> <url>`"
		return t
	}
	ctx := cmdContext(cmd)
	remote, err := readTargetState(ctx, choice.URL, token, projects)
	if err != nil {
		t.Error = err.Error()
		return t
	}
	cluster, cerr := clusterLayer(ctx, choice.URL, token)
	if cerr != nil {
		//nolint:errcheck // advisory: a deploy ignores them the same way
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: ignoring the deploy defaults of %s: %v; a deploy ignores them too, so the labels are computed with the local settings only\n", choice.URL, cerr)
	}
	for i, p := range projects {
		t.Dags = append(t.Dags, dagStatusOf(cmd, p, remote[i], cluster, o))
	}
	return t
}

// useTargetFlag sets the global --target flag to name, so the server, the
// token and the deploy defaults of the target resolve exactly as for
// `--target <name>`, and returns what puts the flag back.
func useTargetFlag(cmd *cobra.Command, name string) (func(), error) {
	f := cmd.Flag("target")
	if f == nil {
		return nil, errors.New("this command has no --target flag")
	}
	old := f.Value.String()
	if err := f.Value.Set(name); err != nil {
		return nil, fmt.Errorf("selecting target %s: %w", name, err)
	}
	return func() { _ = f.Value.Set(old) }, nil //nolint:errcheck // restoring the value it held cannot fail
}

// dagStatusOf settles the project for the target as a deploy does, computes
// its content label and compares it with what the server holds.
func dagStatusOf(cmd *cobra.Command, p statusProject, remote remoteDag, cluster *defaultsLayer, o statusOptions) dagStatus {
	ds := dagStatus{DagID: p.dagID, Path: p.dir}
	label, err := localLabel(cmd, p.dir, cluster, o.dockerfile)
	if err == nil {
		err = remote.err
	}
	if err != nil {
		ds.State, ds.Error = stateError, err.Error()
		return ds
	}
	ds.LocalVersion = label
	if !remote.found {
		ds.State = stateNotDeployed
		return ds
	}
	ds.IsPaused, ds.LastRun, ds.CurrentVersion = remote.paused, remote.lastRun, remote.current
	current := ""
	if remote.current != nil {
		current = remote.current.Label
	}
	switch classifyLabel(label, current, remote.labels()) {
	case deployCurrent:
		ds.State = stateInSync
	case deployStale:
		ds.State, ds.MatchedVersion = stateBehind, remote.version(label)
	case deployUnknown:
		ds.State = stateDrift
		if current == "" {
			ds.State = stateNotDeployed
		}
	}
	return ds
}

// localLabel is the content label a deploy of the project at dir to the
// target in use would register: the same load, settle and label functions
// as runDeploy, with the cluster's deploy defaults as their last layer.
func localLabel(cmd *cobra.Command, dir string, cluster *defaultsLayer, dockerfile string) (string, error) {
	cfg, err := loadDeployProject(dir)
	if err != nil {
		return "", err
	}
	if _, serr := settleDeployConfig(cmd, dir, cfg, cluster); serr != nil {
		return "", serr
	}
	return deployContentLabel(cmd, dir, cfg, dockerfile)
}

// remoteDag is what one control plane holds for a DAG.
type remoteDag struct {
	found    bool
	paused   *bool
	current  *statusVersion
	versions []statusVersion
	lastRun  *statusRun
	// err is a read of this DAG that failed while the server answered.
	err error
}

// labels lists the labels of every registered version.
func (r remoteDag) labels() []string {
	out := make([]string, 0, len(r.versions))
	for _, v := range r.versions {
		out = append(out, v.Label)
	}
	return out
}

// version returns the registered version labeled label, nil when none is.
func (r remoteDag) version(label string) *statusVersion {
	for i := range r.versions {
		if r.versions[i].Label == label {
			v := r.versions[i]
			return &v
		}
	}
	return nil
}

// targetUnreadable is a read that says nothing more can be read from the
// target: it is not reachable, or it refuses the token.
type targetUnreadable struct{ err error }

func (e *targetUnreadable) Error() string { return e.err.Error() }
func (e *targetUnreadable) Unwrap() error { return e.err }

// readTargetState reads every project's DAG from the control plane at
// serverURL, in order. An unreachable server or a refused token stops the
// reads with an error for the whole target; any other failure is recorded
// on its DAG.
func readTargetState(ctx context.Context, serverURL, token string, projects []statusProject) ([]remoteDag, error) {
	c, err := apiclient.New(serverURL, token, apiclient.WithHTTPClient(&http.Client{Timeout: statusReadTimeout}))
	if err != nil {
		return nil, fmt.Errorf("building the API client: %w", err)
	}
	out := make([]remoteDag, len(projects))
	for i, p := range projects {
		r, rerr := readRemoteDag(ctx, c, p.dagID)
		var whole *targetUnreadable
		if errors.As(rerr, &whole) {
			return nil, whole
		}
		r.err = rerr
		out[i] = r
	}
	return out, nil
}

// readRemoteDag reads one DAG with Airflow-compatible reads: the DAG
// (is_paused, or a 404 for a DAG never deployed), its versions, and its
// newest run.
func readRemoteDag(ctx context.Context, c *apiclient.ClientWithResponses, dagID string) (remoteDag, error) {
	dag, err := c.GetDagWithResponse(ctx, dagID)
	if err != nil {
		return remoteDag{}, &targetUnreadable{err: fmt.Errorf("the control plane did not answer: %w", err)}
	}
	switch status := dag.StatusCode(); {
	case status == http.StatusNotFound:
		return remoteDag{}, nil
	case status == http.StatusUnauthorized:
		return remoteDag{}, &targetUnreadable{err: fmt.Errorf("reading %s: %w", dagID, apiStatusError(status, dag.Body))}
	case status != http.StatusOK || dag.JSON200 == nil:
		return remoteDag{}, fmt.Errorf("reading %s: %w", dagID, apiStatusError(status, dag.Body))
	}
	r := remoteDag{found: true, paused: dag.JSON200.IsPaused}
	if r.versions, r.current, err = readVersions(ctx, c, dagID); err != nil {
		return remoteDag{}, err
	}
	if r.lastRun, err = readLastRun(ctx, c, dagID); err != nil {
		return remoteDag{}, err
	}
	return r, nil
}

// readVersions reads the DAG's registered versions and picks the current
// one: the version marked is_current, else (a server that does not mark
// it, such as Airflow) the highest version number.
func readVersions(ctx context.Context, c *apiclient.ClientWithResponses, dagID string) (versions []statusVersion, current *statusVersion, err error) {
	resp, err := c.ListDagVersionsWithResponse(ctx, dagID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing versions of %s: %w", dagID, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, nil, fmt.Errorf("listing versions of %s: %w", dagID, apiStatusError(resp.StatusCode(), resp.Body))
	}
	if resp.JSON200 == nil || resp.JSON200.DagVersions == nil {
		return nil, nil, nil
	}
	pick, marked := -1, false
	for _, v := range *resp.JSON200.DagVersions {
		versions = append(versions, statusVersion{Label: valueOf(v.BundleVersion), VersionNumber: valueOf(v.VersionNumber), CreatedAt: v.CreatedAt})
		i := len(versions) - 1
		switch {
		case valueOf(v.IsCurrent):
			pick, marked = i, true
		case !marked && (pick < 0 || versions[i].VersionNumber > versions[pick].VersionNumber):
			pick = i
		}
	}
	if pick >= 0 {
		v := versions[pick]
		current = &v
	}
	return versions, current, nil
}

// readLastRun reads the DAG's newest run, nil when it has none.
func readLastRun(ctx context.Context, c *apiclient.ClientWithResponses, dagID string) (*statusRun, error) {
	limit := 1
	resp, err := c.ListDagRunsWithResponse(ctx, dagID, &apiclient.ListDagRunsParams{Limit: &limit}, newestRunFirst)
	if err != nil {
		return nil, fmt.Errorf("listing runs of %s: %w", dagID, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("listing runs of %s: %w", dagID, apiStatusError(resp.StatusCode(), resp.Body))
	}
	if resp.JSON200 == nil || resp.JSON200.DagRuns == nil || len(*resp.JSON200.DagRuns) == 0 {
		return nil, nil //nolint:nilnil // a DAG with no runs is a normal outcome, not an error.
	}
	run := (*resp.JSON200.DagRuns)[0]
	return &statusRun{RunID: valueOf(run.DagRunId), State: string(valueOf(run.State)), StartDate: run.StartDate}, nil
}

// newestRunFirst asks for the newest run first, as `dexaflow runs status`
// does: Dexaflow lists a DAG's runs newest first already and does not read
// order_by, which keeps this right on Airflow and if that default changes.
func newestRunFirst(_ context.Context, req *http.Request) error {
	q := req.URL.Query()
	q.Set("order_by", "-logical_date")
	req.URL.RawQuery = q.Encode()
	return nil
}
