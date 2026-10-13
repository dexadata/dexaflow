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
)

// deployReport is the --output json document: where the deploy went and one
// entry per DAG.
type deployReport struct {
	Server string         `json:"server"`
	Target string         `json:"target,omitempty"`
	DAGs   []deployResult `json:"dags"`
}

// deployResult is what one DAG's deploy did, or with --plan would do.
type deployResult struct {
	DagID    string      `json:"dag_id"`
	Status   string      `json:"status"`
	Version  string      `json:"version,omitempty"`
	Image    string      `json:"image,omitempty"`
	Digest   string      `json:"digest,omitempty"`
	SpecHash string      `json:"spec_hash,omitempty"`
	Created  bool        `json:"created"`
	IsPaused *bool       `json:"is_paused,omitempty"`
	Next     string      `json:"next,omitempty"`
	Plan     *deployPlan `json:"plan,omitempty"`
	Error    string      `json:"error,omitempty"`
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
	if o.plan && o.trigger {
		return fmt.Errorf("--plan builds, pushes and registers nothing, so it cannot --trigger a run; drop one of them")
	}
	return nil
}

// runDeployCommand runs deploy with the chosen output. With --output json the
// human lines (and the builder's) go to stderr, and stdout carries one JSON
// document: the report, printed also after a failure of --all so a pipeline
// sees which DAGs went through.
func runDeployCommand(cmd *cobra.Command, args []string, o deployOptions) error {
	if err := validateDeployOptions(o); err != nil {
		return err
	}
	if o.output != outputJSON {
		return runDeployTarget(cmd, args, o)
	}
	stdout := cmd.OutOrStdout()
	cmd.SetOut(cmd.ErrOrStderr())
	o.report = &deployReport{DAGs: []deployResult{}}
	err := runDeployTarget(cmd, args, o)
	if err != nil && len(o.report.DAGs) == 0 {
		return err
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
// prompt (#1705).
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

  For the local Lite server, pass --server %s`, choice.URL, choice.URL)
	}
	return choice, token, nil
}

// printNextStep reads the DAG's state back and prints what happens next,
// recording it in res. A server that cannot answer leaves the line out.
func printNextStep(ctx context.Context, out io.Writer, serverURL, token string, res *deployResult) error {
	state, ok := fetchDagState(ctx, serverURL, token, res.DagID)
	if !ok {
		return nil
	}
	paused := state.IsPaused
	res.IsPaused = &paused
	res.Next = nextStepLine(state, res.Created, time.Now())
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
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
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
