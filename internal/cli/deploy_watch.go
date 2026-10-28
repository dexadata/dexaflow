package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// deployWatchInterval is how often deploy --watch polls the project files,
// as Lite does (devPollInterval). Tests poll faster (cliDeps.watchInterval).
const deployWatchInterval = devPollInterval

// deployWatchSettle is how long deploy --watch waits after a save for the
// files to stop moving, so a burst of saves (an editor's "save all", a
// formatter on save) is one redeploy. Tests shorten it
// (cliDeps.watchSettle).
const deployWatchSettle = 500 * time.Millisecond

// checkDeployWatch refuses the flags that make no sense on every save.
func checkDeployWatch(o deployOptions) error {
	if !o.watch {
		return nil
	}
	trigger := "--trigger"
	if o.follow {
		trigger = "--trigger --follow"
	}
	for _, c := range []struct {
		set       bool
		flag, why string
	}{
		{o.trigger || o.follow, trigger, "a deploy registers the DAG and never runs it, and a run on every save is not asked for; trigger one when you want it with `dexaflow runs trigger <dag_id>`"},
		{o.output == outputJSON, "--output json", "the JSON document is printed when the command ends, and a watch runs until Ctrl-C"},
		{o.plan, "--plan", "a plan deploys nothing, so there is nothing to redeploy; run `dexaflow deploy --plan` without --watch"},
		{o.changedSince != "", "--changed-since", "the watch already redeploys only the projects whose files are saved"},
		{o.dagVersion != "", "--dag-version", "every save would register other content under the same label, which the server refuses; the watch labels each deploy by its content"},
		{o.skipBuild, "--skip-build", "a save changes what the image is built from, so the image must follow it"},
		{o.force, "--force", "it registers again content that is already deployed, and the watch registers only what a save changed"},
	} {
		if c.set {
			return fmt.Errorf("--watch cannot be combined with %s: %s", c.flag, c.why)
		}
	}
	return nil
}

// watchState is what a deploy --watch session remembers between deploys,
// per project directory (resolved, so "." and its absolute path agree).
type watchState struct {
	// redeploying is false during the first deploy and true after it.
	redeploying bool
	// labels is the content label each project was last deployed as (or
	// found current with).
	labels map[string]string
	// settings is what deploySettings last printed for each project.
	settings map[string]string
}

// newWatchState returns the state of a session before its first deploy.
func newWatchState() *watchState {
	return &watchState{labels: map[string]string{}, settings: map[string]string{}}
}

// watchKey is the key of dir in the state's maps.
func watchKey(dir string) string {
	if r, err := resolvedDir(dir); err == nil {
		return r
	}
	return dir
}

// redeploy reports whether a deploy is a redeploy of a watch, after its
// first deploy. False without --watch.
func (s *watchState) redeploy() bool {
	return s != nil && s.redeploying
}

// noteSettings records the settings printed for dir.
func (s *watchState) noteSettings(dir, settings string) {
	if s != nil {
		s.settings[watchKey(dir)] = settings
	}
}

// deployed records the label dir was deployed as, or found current with.
func (s *watchState) deployed(dir, label string) {
	if s != nil && label != "" {
		s.labels[watchKey(dir)] = label
	}
}

// watchSkip decides a redeploy of a watch once its label is known. The
// label the watch last deployed, when the server confirms it is still the
// current version, is a save with no deployable change: one line and nothing
// else (skip). The server decides because the current version can move under
// the watch (a rollback, another deploy). Otherwise it says the project is
// redeployed, with the deploy settings when they changed since they were
// last printed, and the deploy goes on as any deploy. Outside a redeploy it
// does nothing.
func (d *deployRun) watchSkip(ctx context.Context, label string) (skip bool, err error) {
	s := d.o.watching
	if !s.redeploy() {
		return false, nil
	}
	out, key, now := d.cmd.OutOrStdout(), watchKey(d.dir), time.Now().Format("15:04:05")
	if s.labels[key] == label {
		state, _, cerr := checkDeployed(ctx, d.cmd.ErrOrStderr(), d.serverURL, d.token, d.cfg.DagID, label)
		if cerr == nil && state == deployCurrent {
			_, err = fmt.Fprintf(out, "[%s] %s: saved, no deployable change: version %s is the current version; nothing registered\n", now, d.cfg.DagID, label)
			return true, err
		}
	}
	if _, err = fmt.Fprintf(out, "[%s] %s: saved, deploying version %s\n", now, d.cfg.DagID, label); err != nil {
		return false, err
	}
	if s.settings[key] != d.settings {
		s.settings[key] = d.settings
		_, err = io.WriteString(out, d.settings)
	}
	return false, err
}

// watchedProject is one project a deploy --watch redeploys.
type watchedProject struct {
	// dir is the project directory, as the deploy is given it.
	dir string
	// name is how messages name it: the dag_id in a workspace, else dir.
	name string
}

// deployWatch is a deploy --watch session once its target is resolved.
type deployWatch struct {
	cmd *cobra.Command
	o   deployOptions
	// dir is the one project watched, "" when the workspace is.
	dir string
	// workspace is the workspace root whose projects are watched, "" when
	// one project is.
	workspace string
	// shared are files every project's deploy reads (the workspace file).
	shared []string
	// projects and files are what the last scan watched: the projects in
	// order and the files of each, by project directory.
	projects []watchedProject
	files    map[string][]string
	// discoveryErr is the last workspace discovery error printed, so a
	// broken workspace is reported once, not on every poll.
	discoveryErr string
}

// runDeployWatch is deploy --watch: it asks once, deploys, then redeploys
// each project whose files are saved until Ctrl-C, and exits 0 then. A failed
// deploy prints its error and the watch goes on.
func runDeployWatch(cmd *cobra.Command, args []string, o deployOptions) error {
	if err := checkDeployFollow(o); err != nil {
		return err
	}
	dir, all, err := resolveDeployTarget(cmd, args, o)
	if err != nil {
		return err
	}
	w := &deployWatch{cmd: cmd, dir: dir, files: map[string][]string{}}
	if err := w.locate(all); err != nil {
		return err
	}
	if err := w.confirmOnce(&o); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmdContext(cmd), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd.SetContext(ctx)
	o.watching = newWatchState()
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if derr := deploySelection(cmd, dir, all, o); derr != nil && ctx.Err() == nil {
		devPrintf(errOut, "deploy failed: %v\n", derr)
	}
	// --paused and --active are for the first deploy: a redeploy never
	// changes is_paused.
	o.paused, o.active = false, false
	o.watching.redeploying = true
	w.o = o
	snap, _ := w.scan()
	if ctx.Err() == nil {
		devPrintf(out, "watching %s: save a file to redeploy what changed (Ctrl-C to stop)\n", w.root())
		d := depsFrom(ctx)
		saveWatcher{interval: d.watchInterval, settle: d.watchSettle, scan: w.scan, onSave: w.redeploy}.run(ctx, snap)
	}
	devPrintln(out, "\nstopped watching")
	return nil
}

// locate finds the workspace an --all watch reads, and the workspace file
// every deploy of the session reads.
func (w *deployWatch) locate(all bool) error {
	start := w.dir
	if all {
		where, err := deployAllWorkspace(w.cmd)
		if err != nil {
			return err
		}
		w.workspace, start = where.dir, where.dir
	}
	loc, err := findWorkspace(start)
	if err != nil {
		return err
	}
	if loc != nil {
		w.shared = []string{loc.File}
	}
	return nil
}

// confirmOnce asks, before the first deploy, whether to deploy on every save
// to the server, as a deploy asks; the deploys of the session never ask.
func (w *deployWatch) confirmOnce(o *deployOptions) error {
	choice, _, err := deployServerChoice(w.cmd, *o)
	if err != nil {
		return err
	}
	if shouldConfirm(choice.URL, o.yes, cmdInteractive(w.cmd)) &&
		!confirmDeploy(w.cmd.InOrStdin(), w.cmd.OutOrStdout(), w.root()+" on every save", choice.URL) {
		return fmt.Errorf("deploy aborted")
	}
	o.yes = true
	return nil
}

// root is the folder the session watches.
func (w *deployWatch) root() string {
	if w.workspace != "" {
		return w.workspace
	}
	return w.dir
}

// scan lists the projects and their files, and returns the modification
// time of every watched file.
func (w *deployWatch) scan() (map[string]time.Time, bool) {
	w.projects = w.currentProjects()
	w.files = map[string][]string{}
	var all []string
	for _, p := range w.projects {
		files := append(projectWatchFiles(p.dir, w.o.dockerfile), w.shared...)
		w.files[p.dir] = files
		all = append(all, files...)
	}
	return projectMtimes(all), true
}

// currentProjects is the one project watched, or the workspace's projects
// now, so a project added while watching is deployed when it appears. A
// workspace that cannot be read keeps the last projects, and its error is
// printed once.
func (w *deployWatch) currentProjects() []watchedProject {
	if w.workspace == "" {
		return []watchedProject{{dir: w.dir, name: w.singleName()}}
	}
	ws, err := ResolveWorkspace(w.workspace)
	if err != nil {
		if msg := err.Error(); msg != w.discoveryErr {
			w.discoveryErr = msg
			devPrintf(w.cmd.ErrOrStderr(), "workspace discovery: %v\n", err)
		}
		return w.projects
	}
	w.discoveryErr = ""
	out := make([]watchedProject, 0, len(ws.Projects))
	for _, p := range ws.Projects {
		out = append(out, watchedProject{dir: p.Path, name: p.DagID})
	}
	return out
}

// singleName names the one project watched: its dag_id, else (a config
// that does not load) the name it had before, else its directory.
func (w *deployWatch) singleName() string {
	if cfg, err := loadProjectConfig(w.dir); err == nil && cfg.DagID != "" {
		return cfg.DagID
	}
	if len(w.projects) == 1 {
		return w.projects[0].name
	}
	return w.dir
}

// projectWatchFiles lists the files of the project at dir a save in which
// may change its deploy: those Lite watches (WorkspaceSpec.WatchedPaths) and
// every file its content label is computed from (labelInputFiles), which
// leaves out what a deploy itself writes (dag.json, dbt parse outputs). A
// project whose config does not load is watched by its config and its DAG
// source, so the save that fixes it is seen.
func projectWatchFiles(dir, dockerfile string) []string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	p, ok := projectAt(abs)
	if !ok {
		return []string{projectConfigPath(abs), filepath.Join(abs, dagSourceFile)}
	}
	files := (&WorkspaceSpec{Projects: []Project{p}}).WatchedPaths()
	if cfg, cerr := loadProjectConfig(abs); cerr == nil {
		if inputs, ierr := labelInputFiles(abs, cfg, dockerfile); ierr == nil {
			files = append(files, inputs...)
		}
	}
	sort.Strings(files)
	return files
}

// redeploy deploys, one after the other, each project a save touched. A
// failed deploy prints its error; the watch goes on.
func (w *deployWatch) redeploy(before, after map[string]time.Time) {
	changed := changedPaths(before, after)
	ctx := cmdContext(w.cmd)
	for _, p := range w.projects {
		if ctx.Err() != nil {
			return
		}
		if !touches(p.dir, w.files[p.dir], changed) {
			continue
		}
		if err := runDeploy(w.cmd, p.dir, w.o); err != nil && ctx.Err() == nil {
			devPrintf(w.cmd.ErrOrStderr(), "deploy of %s failed: %v\n", p.name, err)
		}
	}
}

// touches reports whether a changed path is one of the project's files, or
// lies under its directory (a file it read that is gone now).
func touches(dir string, files []string, changed map[string]bool) bool {
	for _, f := range files {
		if changed[f] {
			return true
		}
	}
	var prefixes []string
	if abs, err := filepath.Abs(dir); err == nil {
		prefixes = append(prefixes, abs+string(filepath.Separator))
	}
	if r, err := resolvedDir(dir); err == nil {
		prefixes = append(prefixes, r+string(filepath.Separator))
	}
	for c := range changed {
		for _, prefix := range prefixes {
			if strings.HasPrefix(c, prefix) {
				return true
			}
		}
	}
	return false
}
