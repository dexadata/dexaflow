package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// Exit codes of `dexaflow sync`, besides 0 when the target matches the
// workspace (with --dry-run) or every change was applied.
const (
	// syncExitDrift: with --dry-run, sync would change something; without
	// it, a behind DAG was left alone (no --rollback).
	syncExitDrift = 1
	// syncExitError: the target or a project could not be read, a change
	// failed or was refused, the confirmation was declined, or the command
	// was used wrongly.
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
	rollback   bool
	dryRun     bool
	yes        bool
	// maxPrune is --max-prune; negative means the number of projects.
	maxPrune int
}

// newSyncCommand builds `dexaflow sync <dir>`: make the target in use run
// what the workspace at dir holds (#1708). It compares with the logic of
// `dexaflow status`, deploys through the code of `deploy --all`, with
// --rollback makes an older registered version current again as `dexaflow
// rollback` does, and with --prune pauses the DAGs the workspace does not
// hold.
func newSyncCommand() *cobra.Command {
	return newSyncCommandWith(syncPrompts)
}

// syncPrompts reports whether sync asks before it applies: at a terminal,
// for a server that is not on this machine, as deploy does.
func syncPrompts(cmd *cobra.Command, serverURL string) bool {
	return cmdInteractive(cmd) && !isLoopback(serverURL)
}

// syncLong is the help of `dexaflow sync`.
const syncLong = "Compares the DAG projects under <dir> with the DAGs registered on the target in use and makes " +
	"the target match:\n\n" +
	"  - in sync: the local content label is the current version; nothing is done.\n" +
	"  - drift or not deployed: the content label is not registered; the DAG is deployed exactly as " +
	"`dexaflow deploy --all` deploys it (build or reuse the image, push, register). A new DAG is paused " +
	"or not as the server decides for new DAGs; a redeploy never changes is_paused.\n" +
	"  - behind: the content label is a registered version older than the current one, so the target " +
	"runs something newer than this checkout (it was deployed from a newer commit). It is reported and " +
	"left alone; with --rollback that older version is made current again, as `dexaflow rollback --to " +
	"<label>` does, with nothing built and is_paused kept.\n" +
	"  - not in workspace: the target holds a DAG no project under <dir> defines. It is reported, and " +
	"with --prune it is paused (reversible with `dexaflow admin dags unpause`). Nothing is ever deleted. " +
	"Every DAG the token can see on the target is a candidate: sync one folder per target.\n\n" +
	"--prune applies nothing while a project cannot be read (a dexaflow.yaml that does not parse hides " +
	"its dag_id), or when it would pause more DAGs than --max-prune; without a terminal it needs --yes.\n\n" +
	"Targets, tokens and deploy defaults resolve as for a command run in <dir>: --server, --target, " +
	"then the target selected for the workspace around <dir>, its default_target, then the config file. " +
	"The labels are computed as deploy computes them (git describe, the content and the CLI version), so " +
	"pass the --registry, --platform and --dockerfile a deploy is given.\n\n" +
	"--dry-run builds, pushes, registers and changes nothing, so it can run as a CI check or an " +
	"Argo CD hook.\n\n" +
	"Exit codes: 0 when the target matches the workspace (--dry-run) or every change was applied; " +
	"1 when --dry-run found something to change, or a behind DAG was left alone without --rollback; " +
	"2 when the target or a project could not be read, a change failed or was refused, the confirmation " +
	"was declined, or the command was used wrongly. A DAG not in the workspace counts as a change only " +
	"with --prune, and only while it is not paused."

// newSyncCommandWith is newSyncCommand with prompts deciding whether it
// asks before applying (without --yes).
func newSyncCommandWith(prompts func(cmd *cobra.Command, serverURL string) bool) *cobra.Command {
	var o syncOptions
	cmd := &cobra.Command{
		Use:   "sync <dir>",
		Short: "Make the target run the DAGs of a workspace: deploy what changed, report or pause what is gone (Pro).",
		Long:  syncLong,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &exitCodeError{code: syncExitError, err: fmt.Errorf("sync takes one argument, the workspace folder whose DAGs the target should run; got %d", len(args))}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cmd, args[0], o, prompts)
		},
	}
	cmd.Flags().BoolVar(&o.prune, "prune", false, "pause the DAGs the target holds and the workspace does not (never deletes)")
	cmd.Flags().IntVar(&o.maxPrune, "max-prune", -1, "with --prune, apply nothing when more than this many DAGs would be paused (default: the number of DAG projects under <dir>)")
	cmd.Flags().BoolVar(&o.rollback, "rollback", false, "make current again the older registered version a behind DAG's content equals (default: report it and leave it)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "say per DAG what sync would do and change nothing; exit 1 when something would change")
	cmd.Flags().BoolVarP(&o.yes, "yes", "y", false, "skip the confirmation prompt (for automation; required for --prune without a terminal)")
	cmd.Flags().StringVar(&o.output, "output", outputText, "output format: text, or json for one JSON document on stdout (human lines go to stderr)")
	cmd.Flags().StringVar(&o.serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&o.token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: DEXAFLOW_TOKEN, then the token saved for the server)")
	cmd.Flags().String("registry", "", deployRegistryFlagUsage)
	cmd.Flags().StringVar(&o.builder, "builder", "docker", "image build tool to shell out to (e.g. docker, podman, nerdctl)")
	cmd.Flags().StringVar(&o.dockerfile, "dockerfile", "Dockerfile", "Dockerfile path relative to the DAG directory")
	addPlatformFlag(cmd)
	return cmd
}

// runSync compares, reports, and unless --dry-run applies. The error
// carries the exit code.
func runSync(cmd *cobra.Command, dir string, o syncOptions, prompts func(*cobra.Command, string) bool) error {
	if o.output != outputText && o.output != outputJSON {
		return &exitCodeError{code: syncExitError, err: fmt.Errorf("--output must be %s or %s, not %q", outputText, outputJSON, o.output)}
	}
	stdout := cmd.OutOrStdout()
	if o.output == outputJSON {
		cmd.SetOut(cmd.ErrOrStderr())
		defer cmd.SetOut(stdout)
	}
	rep, err := syncWorkspace(cmd, dir, o, prompts)
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
// unless --dry-run applies the plan. An error is a misuse, a workspace that
// cannot be read or a declined confirmation; what the target answers is in
// the report.
func syncWorkspace(cmd *cobra.Command, dir string, o syncOptions, prompts func(*cobra.Command, string) bool) (*syncReport, error) {
	ws, err := loadSyncWorkspace(dir)
	if err != nil {
		return nil, err
	}
	defer withWorkDir(cmd, ws.Path)()
	choice, token, err := deployServerChoice(cmd, deployOptions{serverURL: o.serverURL, token: o.token})
	if err != nil {
		return nil, err
	}
	prompt := !o.dryRun && !o.yes && prompts(cmd, choice.URL)
	if perr := checkSyncPrompt(o, prompt); perr != nil {
		return nil, perr
	}
	rep := &syncReport{Workspace: ws.Path, Server: choice.URL, Source: choice.Source, DryRun: o.dryRun, Prune: o.prune, Dags: []syncDag{}}
	if choice.Target != nil {
		rep.Target = choice.Target.Name
	}
	planSync(cmd, rep, token, ws, o)
	rep.Refused = pruneRefusal(rep, ws, o)
	if o.dryRun || rep.Error != "" || rep.Refused != "" || rep.changes() == 0 {
		return rep, nil
	}
	if prompt {
		if perr := confirmSync(cmd, rep); perr != nil {
			return nil, perr
		}
	}
	applySync(cmd, rep, token, ws.Projects, o)
	return rep, nil
}

// loadSyncWorkspace reads the workspace folder dir. A folder with no DAG
// project is refused: it is never "nothing is desired", which with --prune
// would pause every DAG.
func loadSyncWorkspace(dir string) (*WorkspaceSpec, error) {
	abs, err := resolvedDir(dir)
	if err != nil {
		return nil, err
	}
	if info, serr := os.Stat(abs); serr != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder: sync takes the workspace folder whose DAGs the target should run", dir)
	}
	ws, err := ResolveWorkspace(abs)
	if err != nil {
		return nil, err
	}
	if len(ws.Projects) == 0 && len(ws.broken) == 0 {
		return nil, fmt.Errorf("no DAG projects found in %s; sync refuses to take an empty folder as the DAGs the target should run", abs)
	}
	return ws, nil
}

// checkSyncPrompt refuses what cannot be confirmed: a prune with no prompt
// and no --yes, and a prompt that would break the JSON on stdout.
func checkSyncPrompt(o syncOptions, prompt bool) error {
	if o.dryRun || o.yes {
		return nil
	}
	if o.prune && !prompt {
		return errors.New("--prune pauses DAGs, and there is no terminal to confirm it on: pass --yes, or --dry-run to see what it would pause")
	}
	if prompt && o.output == outputJSON {
		return errors.New("--output json does not ask for a confirmation: pass --yes to apply, or --dry-run")
	}
	return nil
}

// confirmSync prints the plan and asks, naming what will be done.
func confirmSync(cmd *cobra.Command, rep *syncReport) error {
	rep.tally()
	out := cmd.OutOrStdout()
	if err := writeSyncReport(out, rep, outputText); err != nil {
		return err
	}
	question := fmt.Sprintf("\nApply %s on %s?", rep.Summary.promptWords(), rep.Server)
	if !confirmPrompt(cmd.InOrStdin(), out, question) {
		return errors.New("sync aborted: nothing was changed")
	}
	return nil
}

// planSync fills the report: each project's state and action, from the
// reads `dexaflow status` makes, each project whose dexaflow.yaml does not
// parse, then each DAG of the target that no project defines. A target that
// cannot be read sets rep.Error; a DAG list that cannot be read does too
// with --prune, and is a warning without.
func planSync(cmd *cobra.Command, rep *syncReport, token string, ws *WorkspaceSpec, o syncOptions) {
	sp := make([]statusProject, 0, len(ws.Projects))
	for _, p := range ws.Projects {
		sp = append(sp, statusProject{dagID: p.DagID, dir: p.Path})
	}
	// No last run: sync does not show it, and a deploy token may not read runs.
	dags, err := dagStatuses(cmd, rep.Server, token, sp, o.dockerfile, false)
	if err != nil {
		rep.Error = err.Error() + signInHint(err, rep.Target, rep.Server, token, false)
		return
	}
	for _, d := range dags {
		rep.Dags = append(rep.Dags, syncDagOf(d, o.rollback))
	}
	for _, b := range ws.broken {
		if !b.Discovered {
			rep.Dags = append(rep.Dags, syncDag{Path: b.Path, State: stateError, Action: syncActionNone, Error: b.Err.Error()})
		}
	}
	absent, err := absentDags(cmd, rep.Server, token, ws.Projects, o.prune)
	switch {
	case err != nil && o.prune:
		rep.Error = err.Error()
	case err != nil:
		//nolint:errcheck // advisory: without --prune the list only names the absent DAGs
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not list the DAGs of %s (%v); the DAGs the workspace does not hold are not reported\n", rep.Server, err)
	default:
		rep.Dags = append(rep.Dags, absent...)
	}
}

// pruneRefusal is why a --prune sync applies nothing, "" when it may: a
// project that cannot be read (its DAG would read as not in the workspace
// and be paused), or more pauses than --max-prune.
func pruneRefusal(rep *syncReport, ws *WorkspaceSpec, o syncOptions) string {
	if !o.prune || rep.Error != "" {
		return ""
	}
	var unreadable []string
	for _, b := range ws.broken {
		unreadable = append(unreadable, b.Path)
	}
	for _, d := range rep.Dags {
		if d.State == stateError && d.DagID != "" && !slices.Contains(unreadable, d.Path) {
			unreadable = append(unreadable, d.Path)
		}
	}
	if len(unreadable) > 0 {
		return fmt.Sprintf("--prune applies nothing while a project cannot be read, since its DAG would read as not in the workspace and be paused: %s. "+
			"Fix it, or drop --prune to apply the other changes", strings.Join(unreadable, ", "))
	}
	limit := o.maxPrune
	if limit < 0 {
		limit = len(ws.Projects)
	}
	pauses := 0
	for _, d := range rep.Dags {
		if d.Action == syncActionPause {
			pauses++
		}
	}
	if pauses > limit {
		return fmt.Sprintf("--prune would pause %d DAG(s), more than --max-prune %d (default: the number of DAG projects under %s). "+
			"Every DAG the token can see on the target counts: check that the target holds only this workspace's DAGs, or raise --max-prune", pauses, limit, rep.Workspace)
	}
	return ""
}

// syncDagOf is the action a project's status asks for. A behind DAG is
// rolled back only with --rollback.
func syncDagOf(d dagStatus, rollback bool) syncDag {
	s := syncDag{DagID: d.DagID, Path: d.Path, State: d.State, LocalVersion: d.LocalVersion,
		CurrentVersion: d.CurrentVersion, MatchedVersion: d.MatchedVersion, IsPaused: d.IsPaused,
		Action: syncActionNone, Error: d.Error}
	switch d.State {
	case stateDrift, stateNotDeployed:
		s.Action = syncActionDeploy
	case stateBehind:
		if rollback {
			s.Action = syncActionRollback
		}
	}
	return s
}

// absentDags lists the DAGs of the target that no project defines, each
// with the pause --prune asks for when it is not paused yet.
func absentDags(cmd *cobra.Command, serverURL, token string, projects []Project, prune bool) ([]syncDag, error) {
	c, err := cliAPIClient(serverURL, token, apiclient.WithHTTPClient(cliHTTPClient(statusReadTimeout)))
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
	c, cerr := cliAPIClient(rep.Server, token)
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
				err = setCurrentVersion(ctx, out, cmd.ErrOrStderr(), c, d.DagID, d.rollbackTo())
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
