package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// The values of deploy --output.
const (
	outputText = "text"
	outputJSON = "json"
)

// The status of one DAG in the deploy report.
const (
	deployStatusRegistered = "registered" // a new version was registered
	deployStatusUnchanged  = "unchanged"  // the server already had this exact content
	deployStatusCurrent    = "current"    // the content label is the current version: nothing built
	deployStatusPlanned    = "planned"    // --plan: what a deploy would do
	deployStatusRefused    = "refused"    // --plan: a deploy would be refused
	deployStatusFailed     = "failed"     // --all: this DAG's deploy failed
	deployStatusSkipped    = "skipped"    // --changed-since: unchanged since the ref, not deployed
)

// deployReport is the --output json document: where the deploy went and one
// entry per DAG.
type deployReport struct {
	Server string         `json:"server"`
	Target string         `json:"target,omitempty"`
	DAGs   []deployResult `json:"dags"`
	// Removed lists the projects --changed-since found removed since the ref.
	Removed []removedProject `json:"removed,omitempty"`
	// Moved lists the projects --changed-since found in another folder than
	// at the ref, under the same dag_id.
	Moved []movedProject `json:"moved,omitempty"`
}

// deployResult is what one DAG's deploy did, or with --plan would do.
type deployResult struct {
	DagID      string      `json:"dag_id"`
	Status     string      `json:"status"`
	Version    string      `json:"version,omitempty"`
	Image      string      `json:"image,omitempty"`
	Digest     string      `json:"digest,omitempty"`
	SpecHash   string      `json:"spec_hash,omitempty"`
	Created    bool        `json:"created"`
	DagCreated bool        `json:"dag_created"`
	IsPaused   *bool       `json:"is_paused,omitempty"`
	Next       string      `json:"next,omitempty"`
	Plan       *deployPlan `json:"plan,omitempty"`
	// Execution is the server's report of where each task runs and what the
	// version's warm pool does (#1707), absent from an older server.
	Execution *executionSummary `json:"execution,omitempty"`
	Error     string            `json:"error,omitempty"`
	// Reason says why a skipped DAG was not deployed.
	Reason string `json:"reason,omitempty"`
	// Links and Checks are what the server, or a platform in front of it,
	// added to the register answer (#1772); absent when it added none.
	Links  map[string]string `json:"links,omitempty"`
	Checks []serverCheck     `json:"checks,omitempty"`
}

// setServer records the control plane the deploy goes to. A nil report (text
// output) records nothing.
func (r *deployReport) setServer(choice *serverChoice) {
	if r == nil {
		return
	}
	r.Server = choice.URL
	if choice.Target != nil {
		r.Target = choice.Target.Name
	}
}

// add records one DAG's result. A nil report records nothing.
func (r *deployReport) add(res deployResult) {
	if r != nil {
		r.DAGs = append(r.DAGs, res)
	}
}

// addRemoved records a project removed since the --changed-since ref. A nil
// report records nothing.
func (r *deployReport) addRemoved(p removedProject) {
	if r != nil {
		r.Removed = append(r.Removed, p)
	}
}

// addMoved records a project moved since the --changed-since ref. A nil
// report records nothing.
func (r *deployReport) addMoved(p movedProject) {
	if r != nil {
		r.Moved = append(r.Moved, p)
	}
}

// addFailure records that the deploy of dagID failed with err, keeping one
// entry per DAG: --all and the DAG's own deploy both report the same failure,
// and a --trigger that fails after the register turns the registered entry
// into a failed one that still names the version.
func (r *deployReport) addFailure(dagID string, err error) {
	if r == nil {
		return
	}
	for i := range r.DAGs {
		if r.DAGs[i].DagID != dagID {
			continue
		}
		if r.DAGs[i].Status != deployStatusFailed {
			r.DAGs[i].Status = deployStatusFailed
			r.DAGs[i].Error = err.Error()
		}
		return
	}
	r.add(deployResult{DagID: dagID, Status: deployStatusFailed, Error: err.Error()})
}

// digestOf is the sha256:... part of a digest-pinned reference, "" when it
// has none.
func digestOf(ref string) string {
	if _, d, ok := strings.Cut(ref, "@"); ok {
		return d
	}
	return ""
}

// validateDeployOptions refuses flag combinations deploy cannot honor.
func validateDeployOptions(o deployOptions) error {
	if o.output != outputText && o.output != outputJSON {
		return fmt.Errorf("--output must be %s or %s, not %q", outputText, outputJSON, o.output)
	}
	if err := checkDeployWatch(o); err != nil {
		return err
	}
	if o.plan && o.trigger {
		return fmt.Errorf("--plan builds, pushes and registers nothing, so it cannot --trigger a run; drop one of them")
	}
	return nil
}

// runDeployCommand runs deploy with the chosen output. With --output json the
// human lines (and the builder's) go to stderr, and stdout carries one JSON
// document: the report, printed after a failure too, with each failed DAG
// (status failed and its error), so a pipeline sees what went through.
func runDeployCommand(cmd *cobra.Command, args []string, o deployOptions) error {
	if err := validateDeployOptions(o); err != nil {
		return err
	}
	o.cluster = &clusterDefaultsCache{}
	if o.watch {
		return runDeployWatch(cmd, args, o)
	}
	if o.output != outputJSON {
		return runDeployTarget(cmd, args, o)
	}
	stdout := cmd.OutOrStdout()
	cmd.SetOut(cmd.ErrOrStderr())
	o.report = &deployReport{DAGs: []deployResult{}}
	err := runDeployTarget(cmd, args, o)
	if err != nil && len(o.report.DAGs) == 0 {
		// Failed before any DAG was known (no project, no server).
		o.report.addFailure("", err)
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if eerr := enc.Encode(o.report); eerr != nil && err == nil {
		err = fmt.Errorf("writing the deploy report: %w", eerr)
	}
	return err
}

// deployServerChoice resolves where a deploy goes and its token, refusing the
// built-in localhost default unless the machine is set up for Lite: with no
// server set anywhere, a deploy used to go to http://localhost:8080 without a
// prompt (#1705). On Lite the implicit server is the Lite port (#1912).
func deployServerChoice(cmd *cobra.Command, o deployOptions) (*serverChoice, string, error) {
	choice, token, err := resolveServerChoiceToken(cmd, o.serverURL, o.token)
	if err != nil {
		return nil, "", err
	}
	if choice.Implicit && !choice.Lite {
		return nil, "", fmt.Errorf(`deploy has no control plane to go to: no --server, --target, DEXAFLOW_SERVER_URL or server_url is set, and the built-in default (%s) is not assumed for a deploy.
  Name the server, for this deploy or for every one:

      this deploy:             --server https://dexaflow.example.com   (or --target <name>)
      this shell or CI job:    export DEXAFLOW_SERVER_URL=https://dexaflow.example.com
      a named target:          dexaflow target add prod https://dexaflow.example.com && dexaflow target use prod

  For the local Lite server, pass --server %s`, choice.URL, devURL(devDefaultPort))
	}
	return choice, token, nil
}

// printNextStep reads the DAG's state back and prints what happens next,
// recording it in res. wasPaused is the state before an --active deploy. A
// server that cannot answer leaves the line out.
func printNextStep(ctx context.Context, out io.Writer, serverURL, token string, res *deployResult, wasPaused bool) error {
	state, ok := fetchDagState(ctx, serverURL, token, res.DagID)
	if !ok {
		return nil
	}
	paused := state.IsPaused
	res.IsPaused = &paused
	res.Next = nextStepLine(state, dagHistory{created: res.DagCreated, wasPaused: wasPaused}, time.Now())
	_, err := fmt.Fprintln(out, "next: "+res.Next)
	return err
}

// patchDagPausedRaw sets is_paused with the Airflow-compatible PATCH
// /api/v2/dags/{dag_id}, for a deploy of content that is already current,
// which registers nothing to carry --paused or --active.
func patchDagPausedRaw(ctx context.Context, serverURL, token, dagID string, paused bool) error {
	u := strings.TrimRight(serverURL, "/") + "/api/v2/dags/" + url.PathEscape(dagID)
	body := []byte(fmt.Sprintf(`{"is_paused":%t}`, paused))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, u, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building the is_paused request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := cliHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("setting is_paused of %s: %w", dagID, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only response body
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading the is_paused response: %w", err)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("setting is_paused of %s: %w", dagID, apiStatusError(resp.StatusCode, raw))
	}
	return nil
}
