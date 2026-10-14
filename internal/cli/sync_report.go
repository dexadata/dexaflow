package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// stateNotInWorkspace is the state of a DAG the target holds and no project
// of the workspace defines, as `dexaflow sync --output json` names it.
const stateNotInWorkspace = "not_in_workspace"

// What sync does for one DAG.
const (
	// syncActionNone: nothing; the DAG is in sync, could not be read, or is
	// not in the workspace and is left as it is.
	syncActionNone = "none"
	// syncActionDeploy: deploy the local content, as deploy --all does.
	syncActionDeploy = "deploy"
	// syncActionRollback: make the older version the local content equals
	// current again.
	syncActionRollback = "rollback"
	// syncActionPause: pause a DAG the workspace does not hold (--prune).
	syncActionPause = "pause"
)

// The result of a DAG's action.
const (
	// syncResultPlanned: --dry-run, the action was not taken.
	syncResultPlanned = "planned"
	// syncResultApplied: the action went through.
	syncResultApplied = "applied"
	// syncResultFailed: the action failed; error says why.
	syncResultFailed = "failed"
)

// syncReport is the document `dexaflow sync --output json` prints. Its
// fields are a stable interface: add to it, never rename or remove.
type syncReport struct {
	// Workspace is the folder whose projects are the DAGs desired.
	Workspace string `json:"workspace"`
	// Target is the target's name, "" when the server was not chosen by a
	// target (--server, DEXAFLOW_SERVER_URL, server_url).
	Target string `json:"target"`
	Server string `json:"server"`
	// Source says what chose the server.
	Source string `json:"source,omitempty"`
	DryRun bool   `json:"dry_run"`
	Prune  bool   `json:"prune"`
	// Error is why the target could not be read; nothing was changed.
	Error   string      `json:"error,omitempty"`
	Dags    []syncDag   `json:"dags"`
	Summary syncSummary `json:"summary"`
	// ExitCode is the exit code of the command.
	ExitCode int `json:"exit_code"`
}

// syncDag is one DAG of the report: a project of the workspace, or a DAG of
// the target no project defines (no path, no local version).
type syncDag struct {
	DagID string `json:"dag_id"`
	Path  string `json:"path,omitempty"`
	// State is a state of `dexaflow status` (in_sync, behind, drift,
	// not_deployed, error), or not_in_workspace.
	State        string `json:"state"`
	LocalVersion string `json:"local_version,omitempty"`
	// CurrentVersion is the version new runs used before the sync.
	CurrentVersion *statusVersion `json:"current_version,omitempty"`
	// MatchedVersion is the older version the local content equals, for
	// behind: the one a rollback makes current.
	MatchedVersion *statusVersion `json:"matched_version,omitempty"`
	IsPaused       *bool          `json:"is_paused"`
	Action         string         `json:"action"`
	// Result is planned, applied or failed; "" when the action is none.
	Result string `json:"result,omitempty"`
	// Deploy is what the deploy of the DAG did.
	Deploy *deployResult `json:"deploy,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// syncSummary counts the DAGs of the report: by action for the changes, and
// those in sync and not in the workspace. Errors counts the DAGs that could
// not be read, the changes that failed and a target that could not be read.
type syncSummary struct {
	InSync         int `json:"in_sync"`
	Deploy         int `json:"deploy"`
	Rollback       int `json:"rollback"`
	Pause          int `json:"pause"`
	NotInWorkspace int `json:"not_in_workspace"`
	Errors         int `json:"errors"`
}

// changes counts the DAGs with an action.
func (r *syncReport) changes() int {
	n := 0
	for _, d := range r.Dags {
		if d.Action != syncActionNone {
			n++
		}
	}
	return n
}

// tally fills the summary, each planned result and the exit code.
func (r *syncReport) tally() {
	s := syncSummary{}
	if r.Error != "" {
		s.Errors++
	}
	for i := range r.Dags {
		d := &r.Dags[i]
		if d.Action != syncActionNone && d.Result == "" && r.DryRun {
			d.Result = syncResultPlanned
		}
		s.add(*d)
	}
	r.Summary = s
	switch {
	case s.Errors > 0:
		r.ExitCode = syncExitError
	case r.DryRun && r.changes() > 0:
		r.ExitCode = syncExitDrift
	default:
		r.ExitCode = 0
	}
}

// add counts one DAG.
func (s *syncSummary) add(d syncDag) {
	switch d.State {
	case stateInSync:
		s.InSync++
	case stateNotInWorkspace:
		s.NotInWorkspace++
	case stateError:
		s.Errors++
	}
	if d.Result == syncResultFailed {
		s.Errors++
	}
	switch d.Action {
	case syncActionDeploy:
		s.Deploy++
	case syncActionRollback:
		s.Rollback++
	case syncActionPause:
		s.Pause++
	}
}

// exitError is the error the command returns for the report's exit code.
func (r *syncReport) exitError() error {
	switch r.ExitCode {
	case 0:
		return nil
	case syncExitDrift:
		return &exitCodeError{code: syncExitDrift, err: fmt.Errorf("the target does not match the workspace: %d change(s) would be made (%s)",
			r.changes(), r.Summary.changeWords())}
	}
	return &exitCodeError{code: syncExitError, err: fmt.Errorf("sync did not complete: %d error(s)", r.Summary.Errors)}
}

// changeWords is the changes counted by kind, such as "2 deploy, 1 pause".
func (s syncSummary) changeWords() string {
	var parts []string
	for _, c := range []struct {
		n    int
		what string
	}{{s.Deploy, "deploy"}, {s.Rollback, "rollback"}, {s.Pause, "pause"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	return strings.Join(parts, ", ")
}

// closingLine is the last line of the text report.
func (r *syncReport) closingLine() string {
	n := r.changes()
	var line string
	switch {
	case r.Error != "":
		line = "nothing was changed"
	case n == 0:
		line = "in sync, nothing to change"
	case r.DryRun:
		line = fmt.Sprintf("%d change(s) would be made (%s)", n, r.Summary.changeWords())
	default:
		line = fmt.Sprintf("%d change(s) made (%s)", n, r.Summary.changeWords())
	}
	if r.DryRun {
		line = "dry run: " + line
	}
	if r.Summary.Errors > 0 {
		line += fmt.Sprintf("; %d error(s)", r.Summary.Errors)
	}
	return line
}

// writeText prints the report: the workspace, the target, one row per DAG
// and the closing line.
func (r *syncReport) writeText(w io.Writer) error {
	name := r.Target
	if name == "" {
		name = "(none)"
	}
	heading := fmt.Sprintf("workspace: %s\ntarget %s: %s", r.Workspace, name, r.Server)
	if r.Source != "" {
		heading += " (" + r.Source + ")"
	}
	if _, err := fmt.Fprintln(w, heading); err != nil {
		return err
	}
	if len(r.Dags) > 0 {
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		if _, err := fmt.Fprintln(tw, "\n  DAG\tSTATE\tCURRENT VERSION\tPAUSED\tACTION"); err != nil {
			return err
		}
		for _, d := range r.Dags {
			cur := "-"
			if d.CurrentVersion != nil {
				cur = fmt.Sprintf("%s (#%d)", d.CurrentVersion.Label, d.CurrentVersion.VersionNumber)
			}
			if _, err := fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", d.DagID, syncStateWords(d.State), cur, pausedWord(d.IsPaused), d.actionWords(r.Prune)); err != nil {
				return err
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if r.Error != "" {
		if _, err := fmt.Fprintf(w, "  error: %s\n", r.Error); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "\n%s\n", r.closingLine())
	return err
}

// syncStateWords is the STATE column.
func syncStateWords(state string) string {
	switch state {
	case stateInSync:
		return "in sync"
	case stateNotDeployed:
		return "not deployed"
	case stateNotInWorkspace:
		return "not in workspace"
	}
	return state
}

// actionWords is the ACTION column: what sync would do, did, or why it did
// nothing.
func (d syncDag) actionWords(prune bool) string {
	if d.Result == syncResultFailed {
		return d.Action + " failed: " + d.Error
	}
	applied := d.Result == syncResultApplied
	switch d.Action {
	case syncActionDeploy:
		if applied {
			return "deployed " + d.LocalVersion
		}
		return "deploy " + d.LocalVersion
	case syncActionRollback:
		to := describeStatusVersion(d.LocalVersion, d.MatchedVersion)
		if applied {
			return "rolled back to " + to
		}
		return "rollback to " + to
	case syncActionPause:
		if applied {
			return "paused"
		}
		return "pause"
	}
	switch {
	case d.State == stateError:
		return "none: error: " + d.Error
	case d.State != stateNotInWorkspace:
		return "none"
	case valueOf(d.IsPaused):
		return "none: already paused"
	case !prune:
		return "none: pass --prune to pause it"
	}
	return "none"
}

// describeStatusVersion names a version as "label (#n)", or the label alone
// when its number is not known.
func describeStatusVersion(label string, v *statusVersion) string {
	if v == nil {
		return label
	}
	return fmt.Sprintf("%s (#%d)", label, v.VersionNumber)
}

// writeJSONDocument prints v as one indented JSON document.
func writeJSONDocument(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
