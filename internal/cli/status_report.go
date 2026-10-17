package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// The state of one DAG on one target, as `dexaflow status --output json`
// names it.
const (
	// stateInSync: the local content label is the current version.
	stateInSync = "in_sync"
	// stateBehind: the local content label is an older registered version.
	stateBehind = "behind"
	// stateDrift: the local content label is no registered version.
	stateDrift = "drift"
	// stateNotDeployed: the server does not know the DAG, or holds no
	// current version of it.
	stateNotDeployed = "not_deployed"
	// stateError: the DAG could not be read or its label computed.
	stateError = "error"
)

// statusReport is the document `dexaflow status --output json` prints. Its
// fields are a stable interface: add to it, never rename or remove.
type statusReport struct {
	// Workspace is the workspace the DAGs were read from, "" for DAGs named
	// by folder outside one.
	Workspace string         `json:"workspace"`
	Targets   []statusTarget `json:"targets"`
	Summary   statusSummary  `json:"summary"`
	// ExitCode is the exit code of the command.
	ExitCode int `json:"exit_code"`
}

// statusTarget is the report for one target.
type statusTarget struct {
	// Target is the target's name, "" when the server was not chosen by a
	// target (--server, DEXAFLOW_SERVER_URL, server_url).
	Target string `json:"target"`
	Server string `json:"server"`
	// Source says what chose the server.
	Source string `json:"source,omitempty"`
	// Error is why the target could not be read; Dags is then empty.
	Error string      `json:"error,omitempty"`
	Dags  []dagStatus `json:"dags"`
}

// dagStatus is the report for one DAG on one target.
type dagStatus struct {
	DagID string `json:"dag_id"`
	Path  string `json:"path"`
	// LocalVersion is the content label a deploy would register.
	LocalVersion string `json:"local_version"`
	State        string `json:"state"`
	// CurrentVersion is the version new runs use, nil when there is none.
	CurrentVersion *statusVersion `json:"current_version"`
	// MatchedVersion is the older version the local content equals, for
	// behind.
	MatchedVersion *statusVersion `json:"matched_version,omitempty"`
	IsPaused       *bool          `json:"is_paused"`
	LastRun        *statusRun     `json:"last_run"`
	Error          string         `json:"error,omitempty"`
}

// statusVersion is a registered version of a DAG.
type statusVersion struct {
	Label         string     `json:"label"`
	VersionNumber int        `json:"version_number"`
	CreatedAt     *time.Time `json:"created_at"`
}

// statusRun is the run of a DAG with the latest logical date.
type statusRun struct {
	RunID     string     `json:"run_id"`
	State     string     `json:"state"`
	StartDate *time.Time `json:"start_date"`
}

// statusSummary counts the DAGs of every target by state; Errors also
// counts the targets that could not be read.
type statusSummary struct {
	InSync      int `json:"in_sync"`
	Behind      int `json:"behind"`
	Drift       int `json:"drift"`
	NotDeployed int `json:"not_deployed"`
	Errors      int `json:"errors"`
}

// tally fills the summary and the exit code.
func (r *statusReport) tally() {
	r.Summary = statusSummary{}
	for _, t := range r.Targets {
		if t.Error != "" {
			r.Summary.Errors++
		}
		for _, d := range t.Dags {
			switch d.State {
			case stateInSync:
				r.Summary.InSync++
			case stateBehind:
				r.Summary.Behind++
			case stateDrift:
				r.Summary.Drift++
			case stateNotDeployed:
				r.Summary.NotDeployed++
			default:
				r.Summary.Errors++
			}
		}
	}
	s := r.Summary
	switch {
	case s.Errors > 0:
		r.ExitCode = statusExitError
	case s.Behind+s.Drift+s.NotDeployed > 0:
		r.ExitCode = statusExitNotInSync
	default:
		r.ExitCode = 0
	}
}

// exitError is the error the command returns for the report's exit code.
func (r *statusReport) exitError() error {
	switch r.ExitCode {
	case 0:
		return nil
	case statusExitNotInSync:
		s := r.Summary
		return &exitCodeError{code: statusExitNotInSync, err: fmt.Errorf("%d DAG(s) not in sync: %s",
			s.Behind+s.Drift+s.NotDeployed, r.Summary.String())}
	}
	return &exitCodeError{code: statusExitError, err: fmt.Errorf("status could not read everything: %s", r.Summary.String())}
}

// String is the summary as one line, such as "2 in sync, 1 drift".
func (s statusSummary) String() string {
	var parts []string
	for _, c := range []struct {
		n    int
		what string
	}{{s.InSync, "in sync"}, {s.Behind, "behind"}, {s.Drift, "drift"}, {s.NotDeployed, "not deployed"}, {s.Errors, "error(s)"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	if len(parts) == 0 {
		return "no DAGs"
	}
	return strings.Join(parts, ", ")
}

// writeStatusReport prints the report as text or as one JSON document.
func writeStatusReport(w io.Writer, r *statusReport, output string) error {
	if output == outputJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	if r.Workspace != "" {
		if _, err := fmt.Fprintf(w, "workspace: %s\n", r.Workspace); err != nil {
			return err
		}
	}
	for _, t := range r.Targets {
		if err := writeStatusTarget(w, t); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "\n%s\n", r.Summary.String())
	return err
}

// writeStatusTarget prints one target: a heading, then a table of its DAGs,
// or its error line.
func writeStatusTarget(w io.Writer, t statusTarget) error {
	name := t.Target
	if name == "" {
		name = "(none)"
	}
	heading := fmt.Sprintf("\ntarget %s: %s", name, t.Server)
	if t.Source != "" {
		heading += " (" + t.Source + ")"
	}
	if _, err := fmt.Fprintln(w, heading); err != nil {
		return err
	}
	if t.Error != "" {
		_, err := fmt.Fprintf(w, "  error: %s\n", t.Error)
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "  DAG\tSTATUS\tCURRENT VERSION\tREGISTERED\tPAUSED\tLAST RUN"); err != nil {
		return err
	}
	for _, d := range t.Dags {
		cur, created := "-", "-"
		if d.CurrentVersion != nil {
			cur = fmt.Sprintf("%s (#%d)", d.CurrentVersion.Label, d.CurrentVersion.VersionNumber)
			created = formatStatusTime(d.CurrentVersion.CreatedAt)
		}
		if _, err := fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", d.DagID, d.describe(), cur, created, pausedWord(d.IsPaused), d.lastRunWords()); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// describe is the STATUS column of the text report.
func (d dagStatus) describe() string {
	switch d.State {
	case stateInSync:
		return "in sync"
	case stateBehind:
		return "behind: local equals " + d.LocalVersion
	case stateDrift:
		return "drift: local changes not deployed"
	case stateNotDeployed:
		return "not deployed"
	}
	return "error: " + d.Error
}

// lastRunWords is the LAST RUN column: run id, state and start time.
func (d dagStatus) lastRunWords() string {
	if d.State == stateNotDeployed || d.State == stateError {
		return "-"
	}
	if d.LastRun == nil {
		return "none"
	}
	return fmt.Sprintf("%s %s %s", d.LastRun.RunID, d.LastRun.State, formatStatusTime(d.LastRun.StartDate))
}

// pausedWord is the PAUSED column.
func pausedWord(p *bool) string {
	switch {
	case p == nil:
		return "-"
	case *p:
		return "yes"
	}
	return "no"
}

// formatStatusTime prints t in UTC to the minute, "-" when unset.
func formatStatusTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04")
}
