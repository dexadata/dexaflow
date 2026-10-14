package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// Exit codes of `dexaflow sync`, besides 0 when the target matches the
// workspace (with --dry-run) or every change was applied.
const (
	// syncExitDrift: with --dry-run, sync would change something.
	syncExitDrift = 1
	// syncExitError: the target or a DAG could not be read, a change
	// failed, or the command was used wrongly.
	syncExitError = 2
)

// syncOptions holds the flags of `dexaflow sync`.
type syncOptions struct {
	serverURL  string
	token      string
	builder    string
	dockerfile string
	output     string
	prune      bool
	dryRun     bool
	yes        bool
}

// newSyncCommand builds `dexaflow sync <dir>`: make the target in use run
// what the workspace at dir holds (#1708). It compares with the logic of
// `dexaflow status`, deploys through the code of `deploy --all`, makes an
// older registered version current again as `dexaflow rollback` does, and
// with --prune pauses the DAGs the workspace does not hold.
func newSyncCommand() *cobra.Command {
	var o syncOptions
	cmd := &cobra.Command{
		Use:   "sync <dir>",
		Short: "Make the target run the DAGs of a workspace: deploy what changed, report or pause what is gone (Pro).",
		Long: "Compares the DAG projects under <dir> with the DAGs registered on the target in use and makes " +
			"the target match:\n\n" +
			"  - in sync: the local content label is the current version; nothing is done.\n" +
			"  - drift or not deployed: the content is not registered; it is deployed exactly as " +
			"`dexaflow deploy --all` deploys it (build or reuse the image, push, register). A new DAG is paused " +
			"or not as the server decides for new DAGs; a redeploy never changes is_paused.\n" +
			"  - behind: the content is an older registered version; that version is made current again, " +
			"as `dexaflow rollback --to <label>` does, with nothing built.\n" +
			"  - not in workspace: the target holds a DAG no project under <dir> defines. It is reported, and " +
			"with --prune it is paused (reversible with `dexaflow admin dags unpause`). Nothing is ever deleted.\n\n" +
			"Targets, tokens and deploy defaults resolve as for a command run in <dir>: --target, then the " +
			"target selected for the workspace around <dir>, its default_target, then the config file. The " +
			"labels are computed as deploy computes them, so pass the --registry, --platform and --dockerfile " +
			"a deploy is given.\n\n" +
			"--dry-run builds, pushes, registers and changes nothing, so it can run as a CI check or an " +
			"Argo CD or Flux hook.\n\n" +
			"Exit codes: 0 when the target matches the workspace (--dry-run) or every change was applied; " +
			"1 when --dry-run found something to change; 2 when the target or a DAG could not be read, a " +
			"change failed, or the command was used wrongly. A DAG not in the workspace counts as a change " +
			"only with --prune, and only while it is not paused.",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &exitCodeError{code: syncExitError, err: fmt.Errorf("sync takes one argument, the workspace folder whose DAGs the target should run; got %d", len(args))}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cmd, args[0], o)
		},
	}
	cmd.Flags().BoolVar(&o.prune, "prune", false, "pause the DAGs the target holds and the workspace does not (never deletes)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "say per DAG what sync would do and change nothing; exit 1 when something would change")
	cmd.Flags().BoolVarP(&o.yes, "yes", "y", false, "skip the confirmation prompt (for automation)")
	cmd.Flags().StringVar(&o.output, "output", outputText, "output format: text, or json for one JSON document on stdout (human lines go to stderr)")
	cmd.Flags().StringVar(&o.serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&o.token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: the token saved for the server)")
	cmd.Flags().String("registry", "", deployRegistryFlagUsage)
	cmd.Flags().StringVar(&o.builder, "builder", "docker", "image build tool to shell out to (e.g. docker, podman, nerdctl)")
	cmd.Flags().StringVar(&o.dockerfile, "dockerfile", "Dockerfile", "Dockerfile path relative to the DAG directory")
	addPlatformFlag(cmd)
	return cmd
}

// runSync compares, reports, and unless --dry-run applies. The error
// carries the exit code.
func runSync(cmd *cobra.Command, dir string, o syncOptions) error {
	if o.output != outputText && o.output != outputJSON {
		return &exitCodeError{code: syncExitError, err: fmt.Errorf("--output must be %s or %s, not %q", outputText, outputJSON, o.output)}
	}
	stdout := cmd.OutOrStdout()
	if o.output == outputJSON {
		cmd.SetOut(cmd.ErrOrStderr())
		defer cmd.SetOut(stdout)
	}
	rep, err := syncWorkspace(cmd, dir, o)
	if err != nil {
		return &exitCodeError{code: syncExitError, err: err}
	}
	rep.tally()
	if o.output == outputText && !rep.DryRun && rep.changes() > 0 {
		// After the lines of the deploys.
		devPrintf(stdout, "\n")
	}
	if werr := writeSyncReport(stdout, rep, o.output); werr != nil {
		return &exitCodeError{code: syncExitError, err: werr}
	}
	return rep.exitError()
}

// syncWorkspace reads the workspace at dir and the target, plans, and
// unless --dry-run applies the plan. An error is a misuse or a workspace
// that cannot be read; what the target answers is in the report.
func syncWorkspace(cmd *cobra.Command, dir string, o syncOptions) (*syncReport, error) {
	abs, err := resolvedDir(dir)
	if err != nil {
		return nil, err
	}
	if info, serr := os.Stat(abs); serr != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder: sync takes the workspace folder whose DAGs the target should run", dir)
	}
	restore, err := enterDir(cmd, abs)
	if err != nil {
		return nil, err
	}
	defer restore()
	ws, err := ResolveWorkspace(abs)
	if err != nil {
		return nil, err
	}
	if len(ws.Projects) == 0 {
		// Never "nothing is desired": with --prune that would pause every DAG.
		return nil, fmt.Errorf("no DAG projects found in %s; sync refuses to take an empty folder as the DAGs the target should run", abs)
	}
	choice, token, err := deployServerChoice(cmd, deployOptions{serverURL: o.serverURL, token: o.token})
	if err != nil {
		return nil, err
	}
	rep := &syncReport{Workspace: abs, Server: choice.URL, Source: choice.Source, DryRun: o.dryRun, Prune: o.prune, Dags: []syncDag{}}
	if choice.Target != nil {
		rep.Target = choice.Target.Name
	}
	planSync(cmd, rep, token, ws.Projects, o)
	if o.dryRun || rep.Error != "" || rep.changes() == 0 {
		return rep, nil
	}
	if shouldConfirm(choice.URL, o.yes, cmdInteractive(cmd)) {
		rep.tally()
		if werr := writeSyncReport(cmd.OutOrStdout(), rep, outputText); werr != nil {
			return nil, werr
		}
		if !confirmDeploy(cmd.InOrStdin(), cmd.OutOrStdout(), "the changes above", choice.URL) {
			return nil, errors.New("sync aborted")
		}
	}
	applySync(cmd, rep, token, ws.Projects, o)
	return rep, nil
}

// enterDir makes dir the current folder until the returned func runs, so the
// workspace around dir (its targets, its defaults and the target selected
// for it) resolves as for a command run there. A relative --config keeps
// naming the file it named.
func enterDir(cmd *cobra.Command, dir string) (func(), error) {
	if f := cmd.Flag("config"); f != nil && f.Value.String() != "" && !filepath.IsAbs(f.Value.String()) {
		abs, err := filepath.Abs(f.Value.String())
		if err != nil {
			return nil, fmt.Errorf("resolving --config: %w", err)
		}
		if serr := f.Value.Set(abs); serr != nil {
			return nil, fmt.Errorf("resolving --config: %w", serr)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolving the current directory: %w", err)
	}
	if cerr := os.Chdir(dir); cerr != nil {
		return nil, fmt.Errorf("entering %s: %w", dir, cerr)
	}
	return func() { _ = os.Chdir(wd) }, nil //nolint:errcheck // going back to the folder the command started in
}

// planSync fills the report: each project's state and action, from the
// reads `dexaflow status` makes, then each DAG of the target that no project
// defines. A target that cannot be read sets rep.Error.
func planSync(cmd *cobra.Command, rep *syncReport, token string, projects []Project, o syncOptions) {
	sp := make([]statusProject, 0, len(projects))
	for _, p := range projects {
		sp = append(sp, statusProject{dagID: p.DagID, dir: p.Path})
	}
	// No last run: sync does not show it, and a deploy token may not read runs.
	dags, err := dagStatuses(cmd, rep.Server, token, sp, o.dockerfile, false)
	if err != nil {
		rep.Error = err.Error() + signInHint(err, rep.Target, rep.Server, token, false)
		return
	}
	for _, d := range dags {
		rep.Dags = append(rep.Dags, syncDagOf(d))
	}
	absent, err := absentDags(cmd, rep.Server, token, projects, o.prune)
	if err != nil {
		rep.Error = err.Error()
		return
	}
	rep.Dags = append(rep.Dags, absent...)
}

// syncDagOf is the action a project's status asks for.
func syncDagOf(d dagStatus) syncDag {
	s := syncDag{DagID: d.DagID, Path: d.Path, State: d.State, LocalVersion: d.LocalVersion,
		CurrentVersion: d.CurrentVersion, MatchedVersion: d.MatchedVersion, IsPaused: d.IsPaused,
		Action: syncActionNone, Error: d.Error}
	switch d.State {
	case stateDrift, stateNotDeployed:
		s.Action = syncActionDeploy
	case stateBehind:
		s.Action = syncActionRollback
	}
	return s
}

// absentDags lists the DAGs of the target that no project defines, each
// with the pause --prune asks for when it is not paused yet.
func absentDags(cmd *cobra.Command, serverURL, token string, projects []Project, prune bool) ([]syncDag, error) {
	c, err := apiclient.New(serverURL, token, apiclient.WithHTTPClient(&http.Client{Timeout: statusReadTimeout}))
	if err != nil {
		return nil, fmt.Errorf("building the API client: %w", err)
	}
	registered, err := listAllDags(cmdContext(cmd), c)
	if err != nil {
		return nil, fmt.Errorf("reading the DAGs of %s: %w", serverURL, err)
	}
	desired := map[string]bool{}
	for _, p := range projects {
		desired[p.DagID] = true
	}
	var out []syncDag
	for _, d := range registered {
		id := valueOf(d.DagId)
		if id == "" || desired[id] {
			continue
		}
		s := syncDag{DagID: id, State: stateNotInWorkspace, IsPaused: d.IsPaused, Action: syncActionNone}
		if prune && !valueOf(d.IsPaused) {
			s.Action = syncActionPause
		}
		out = append(out, s)
	}
	return out, nil
}

// applySync makes each planned change: the deploys first, through the code
// of deploy --all, then the rollbacks and the pauses. A change that fails is
// recorded on its DAG and the others still go through.
func applySync(cmd *cobra.Command, rep *syncReport, token string, projects []Project, o syncOptions) {
	results := syncDeploys(cmd, rep, projects, o)
	c, cerr := apiclient.New(rep.Server, token)
	ctx, out := cmdContext(cmd), cmd.OutOrStdout()
	for i := range rep.Dags {
		d := &rep.Dags[i]
		var err error
		switch d.Action {
		case syncActionNone:
			continue
		case syncActionDeploy:
			err = d.takeDeploy(results)
		case syncActionRollback:
			if err = cerr; err == nil {
				err = setCurrentVersion(ctx, out, c, d.DagID, d.rollbackTo())
			}
		case syncActionPause:
			if err = cerr; err == nil {
				err = patchDagPaused(ctx, c, d.DagID, true)
			}
		}
		d.Result = syncResultApplied
		if err != nil {
			d.Result, d.Error = syncResultFailed, err.Error()
		}
	}
}

// syncDeploys deploys the projects whose DAG the plan deploys, with the
// flags of this sync, and returns each one's result by dag_id.
func syncDeploys(cmd *cobra.Command, rep *syncReport, projects []Project, o syncOptions) map[string]deployResult {
	want := map[string]bool{}
	for _, d := range rep.Dags {
		if d.Action == syncActionDeploy {
			want[d.DagID] = true
		}
	}
	var deploy []Project
	for _, p := range projects {
		if want[p.DagID] {
			deploy = append(deploy, p)
		}
	}
	if len(deploy) == 0 {
		return nil
	}
	dopts := deployOptions{serverURL: o.serverURL, token: o.token, builder: o.builder, dockerfile: o.dockerfile,
		yes: true, output: outputText, cluster: &clusterDefaultsCache{}, report: &deployReport{DAGs: []deployResult{}}}
	deployProjects(cmd, deploy, dopts)
	results := map[string]deployResult{}
	for _, r := range dopts.report.DAGs {
		results[r.DagID] = r
	}
	return results
}

// takeDeploy records the deploy's result on d, and returns its failure.
func (d *syncDag) takeDeploy(results map[string]deployResult) error {
	r, ok := results[d.DagID]
	if !ok {
		return errors.New("the deploy reported no result")
	}
	d.Deploy = &r
	if r.Status == deployStatusFailed {
		return errors.New(r.Error)
	}
	return nil
}

// rollbackTo is the version a rollback of d makes current: the older one
// its local content equals.
func (d *syncDag) rollbackTo() listedVersion {
	v := listedVersion{label: d.LocalVersion}
	if d.MatchedVersion != nil {
		v.number = d.MatchedVersion.VersionNumber
	}
	return v
}

// writeSyncReport prints the report as text or as one JSON document.
func writeSyncReport(w io.Writer, r *syncReport, output string) error {
	if output == outputJSON {
		return writeJSONDocument(w, r)
	}
	return r.writeText(w)
}
