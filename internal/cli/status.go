package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
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
	cmd.Flags().Bool(noSourceModeFlag, false, "the --no-source-mode a deploy is given: the label of a build even where source_mode_image is set")
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
	choice, token, err := resolveServerChoiceTokenFor(cmd, o.serverURL, o.token, o.allTargets)
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
	dags, err := dagStatuses(cmd, choice.URL, token, projects, o.dockerfile, true)
	if err != nil {
		t.Error = err.Error() + signInHint(err, t.Target, choice.URL, token, o.allTargets)
		return t
	}
	t.Dags = dags
	return t
}

// dagStatuses reads the projects' DAGs from the control plane at serverURL
// and compares each with the content label a deploy would register there,
// in order. An error is a target that could not be read at all
// (targetUnreadable); a DAG that could not be read is in state error.
// lastRun also reads each DAG's last run, a read a deploy token is refused.
// `dexaflow status` and `dexaflow sync` both decide with it.
func dagStatuses(cmd *cobra.Command, serverURL, token string, projects []statusProject, dockerfile string, lastRun bool) ([]dagStatus, error) {
	ctx := cmdContext(cmd)
	remote, err := readTargetState(ctx, serverURL, token, projects, lastRun)
	if err != nil {
		return nil, err
	}
	cluster, cerr := clusterLayer(ctx, serverURL, token)
	if cerr != nil {
		//nolint:errcheck // advisory: a deploy ignores them the same way
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: ignoring the deploy defaults of %s: %v; a deploy ignores them too, so the labels are computed with the local settings only\n", serverURL, cerr)
	}
	out := make([]dagStatus, 0, len(projects))
	for i, p := range projects {
		out = append(out, dagStatusOf(cmd, p, remote[i], cluster, dockerfile))
	}
	return out, nil
}

// signInHint is the line a 401 of the target gets: how to sign in to that
// target, and with no token sent under --all-targets (allTargets), why none
// was.
func signInHint(err error, target, serverURL, token string, allTargets bool) string {
	var whole *targetUnreadable
	if !errors.As(err, &whole) || !whole.unauthorized {
		return ""
	}
	login := loginCommandPath + " --server " + serverURL
	if target != "" {
		login = loginCommandPath + " --target " + target
	}
	switch {
	case token == "" && allTargets:
		return "\nhint: no token is saved for " + serverURL + " (a token saved without a server is never sent with --all-targets); sign in:  " + login
	case token == "":
		return "\nhint: no token is saved for " + serverURL + "; sign in:  " + login
	}
	return "\nhint: the server refused the token; sign in again:  " + login
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
func dagStatusOf(cmd *cobra.Command, p statusProject, remote remoteDag, cluster *defaultsLayer, dockerfile string) dagStatus {
	ds := dagStatus{DagID: p.dagID, Path: p.dir}
	label, err := localLabel(cmd, p.dir, cluster, dockerfile)
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
	current := remote.currentLabel
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
	prov, serr := settleDeployConfig(cmd, dir, cfg, cluster)
	if serr != nil {
		return "", serr
	}
	return deployContentLabel(cmd, dir, cfg, dockerfile, prov.sourceMode.image)
}

// remoteDag is what one control plane holds for a DAG.
type remoteDag struct {
	found  bool
	paused *bool
	// currentLabel is the label the details name current, "" for none.
	currentLabel string
	current      *statusVersion
	versions     []statusVersion
	lastRun      *statusRun
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
type targetUnreadable struct {
	err error
	// unauthorized marks a 401: the target refused the token.
	unauthorized bool
}

func (e *targetUnreadable) Error() string { return e.err.Error() }
func (e *targetUnreadable) Unwrap() error { return e.err }

// readTargetState reads every project's DAG from the control plane at
// serverURL, in order, with its last run when lastRun is set. An unreachable server or a refused token (401) stops
// the reads with an error for the whole target; any other failure, a 403
// included, is recorded on its DAG.
func readTargetState(ctx context.Context, serverURL, token string, projects []statusProject, lastRun bool) ([]remoteDag, error) {
	c, err := cliAPIClient(serverURL, token, apiclient.WithHTTPClient(cliHTTPClient(statusReadTimeout)))
	if err != nil {
		return nil, fmt.Errorf("building the API client: %w", err)
	}
	rd := remoteReader{c: c, serverURL: serverURL, token: token, lastRun: lastRun}
	out := make([]remoteDag, len(projects))
	for i, p := range projects {
		r, rerr := rd.read(ctx, p.dagID)
		var whole *targetUnreadable
		if errors.As(rerr, &whole) {
			return nil, whole
		}
		r.err = rerr
		out[i] = r
	}
	return out, nil
}

// remoteReader reads DAGs from one control plane.
type remoteReader struct {
	c         *apiclient.ClientWithResponses
	serverURL string
	token     string
	// lastRun also reads each DAG's run with the latest logical date.
	lastRun bool
}

// read reads one DAG with the reads deploy's pre-check makes
// (checkDeployed): GET /api/v2/dags/{dag_id}/details for the current label
// and is_paused (a 404 for a DAG never deployed), every page of its
// versions (listRegisteredVersions), then, with rd.lastRun, its run with the
// latest logical date.
func (rd remoteReader) read(ctx context.Context, dagID string) (remoteDag, error) {
	base := strings.TrimRight(rd.serverURL, "/") + "/api/v2/dags/" + url.PathEscape(dagID)
	var details struct {
		BundleVersion *string `json:"bundle_version"`
		IsPaused      *bool   `json:"is_paused"`
	}
	found, err := rd.getJSON(ctx, base+"/details", &details)
	if err != nil || !found {
		return remoteDag{}, err
	}
	versions, err := listRegisteredVersions(base, func(u string, page any) (bool, error) {
		return rd.getJSON(ctx, u, page)
	})
	if err != nil {
		return remoteDag{}, err
	}
	r := remoteDag{found: true, paused: details.IsPaused, currentLabel: valueOf(details.BundleVersion)}
	for _, v := range versions {
		r.versions = append(r.versions, statusVersion{Label: v.Label, VersionNumber: v.Number, CreatedAt: v.CreatedAt})
	}
	r.current = currentVersion(r.currentLabel, versions)
	if !rd.lastRun {
		return r, nil
	}
	if r.lastRun, err = readLastRun(ctx, rd.c, dagID); err != nil {
		return remoteDag{}, err
	}
	return r, nil
}

// getJSON GETs u into out. found is false, with no error, for a 404. An
// unreachable server and a 401 are targetUnreadable; any other answer but
// 200 is an error of this DAG.
func (rd remoteReader) getJSON(ctx context.Context, u string, out any) (found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, statusReadTimeout)
	defer cancel()
	status, body, err := getOnce(ctx, u, rd.token)
	switch {
	case err != nil:
		return false, &targetUnreadable{err: fmt.Errorf("the control plane did not answer: %w", err)}
	case status == http.StatusNotFound:
		return false, nil
	case status == http.StatusUnauthorized:
		return false, &targetUnreadable{err: apiStatusError(status, body), unauthorized: true}
	case status != http.StatusOK:
		return false, fmt.Errorf("reading %s: %w", u, apiStatusError(status, body))
	}
	if derr := json.Unmarshal(body, out); derr != nil {
		return false, fmt.Errorf("decoding %s: %w", u, derr)
	}
	return true, nil
}

// currentVersion is the registered version labeled label, the label the
// details name current, nil when there is none. Among several versions with
// that label it is the one marked is_current, else the highest number. The
// label alone decides the state, as for deploy: a version is never taken as
// current because of its number.
func currentVersion(label string, versions []registeredVersion) *statusVersion {
	if label == "" {
		return nil
	}
	pick := -1
	for i, v := range versions {
		if v.Label != label {
			continue
		}
		if valueOf(v.IsCurrent) {
			pick = i
			break
		}
		if pick < 0 || v.Number > versions[pick].Number {
			pick = i
		}
	}
	if pick < 0 {
		return &statusVersion{Label: label}
	}
	v := versions[pick]
	return &statusVersion{Label: v.Label, VersionNumber: v.Number, CreatedAt: v.CreatedAt}
}

// readLastRun reads the DAG's run with the latest logical date, nil when it
// has none.
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

// newestRunFirst asks for the run with the latest logical date first, as
// `dexaflow runs status` does: Dexaflow lists a DAG's runs that way already
// and does not read order_by, which keeps this right on Airflow and if that
// default changes.
func newestRunFirst(_ context.Context, req *http.Request) error {
	q := req.URL.Query()
	q.Set("order_by", "-logical_date")
	req.URL.RawQuery = q.Encode()
	return nil
}
