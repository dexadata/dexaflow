package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newRunsCommand groups the commands that trigger and inspect DAG runs.
func newRunsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Trigger and inspect DAG runs.",
	}
	// `runs list` is where users instinctively look; it reuses the exact
	// command the operator alias `admin runs list` is built from, so the two
	// share one lister, one set of flags (--state/--dag/--older-than), and one
	// output format — no duplicated API calls.
	cmd.AddCommand(newRunsTriggerCommand(), newRunsStatusCommand(), newRunsLogsCommand(), newAdminRunsListCommand())
	return cmd
}

func newRunsTriggerCommand() *cobra.Command {
	var serverURL, token, conf, confFile string
	var follow bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "trigger <dag_id>",
		Short: "Trigger a new run of a DAG, and optionally follow it to the end.",
		Long: "Trigger a new run of a DAG. With --follow, print each task state change and the logs of each\n" +
			"task as it finishes, then exit with the run's result: 0 when it succeeded, " + strconv.Itoa(exitRunFailed) + " when it\n" +
			"failed (naming the failed tasks), " + strconv.Itoa(exitFollowTimeout) + " when --timeout elapsed first, " +
			strconv.Itoa(exitInterrupted) + " after Ctrl-C.\n" +
			"Ctrl-C and --timeout stop following; neither cancels the run.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFollowTimeout(follow, timeout); err != nil {
				return err
			}
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			body, err := triggerBody(conf, confFile)
			if err != nil {
				return err
			}
			url := strings.TrimRight(base, "/") + "/api/v2/dags/" + args[0] + "/dagRuns"
			status, raw, err := apiRequest(cmdContext(cmd), http.MethodPost, url, bearer, body)
			if err != nil {
				return err
			}
			if status >= http.StatusMultipleChoices {
				return apiStatusError(status, raw)
			}
			var r struct {
				DagRunID string `json:"dag_run_id"`
				State    string `json:"state"`
			}
			if jerr := json.Unmarshal(raw, &r); jerr != nil {
				return fmt.Errorf("parsing response: %w", jerr)
			}
			if _, err = fmt.Fprintf(cmd.OutOrStdout(), "Triggered run %s for %q (state %s)\n", r.DagRunID, args[0], r.State); err != nil {
				return err
			}
			if !follow {
				// With --follow the follower prints this note in its header.
				return notePausedRun(cmdContext(cmd), cmd.OutOrStdout(), base, bearer, args[0])
			}
			return followWithSignals(cmdContext(cmd), cmd.OutOrStdout(),
				followTarget{base: base, token: bearer, dagID: args[0], runID: r.DagRunID}, followOptions{timeout: timeout})
		},
	}
	addRunsFlags(cmd, &serverURL, &token)
	addFollowFlags(cmd, &follow, &timeout)
	cmd.Flags().StringVar(&conf, "conf", "", "run configuration as an inline JSON object, exposed to tasks as params (e.g. --conf '{\"date\":\"2026-01-01\"}')")
	cmd.Flags().StringVar(&confFile, "conf-file", "", "path to a JSON file whose object contents become the run configuration; mutually exclusive with --conf")
	return cmd
}

// triggerBody builds the JSON request body for a trigger, folding an optional
// run configuration into its conf field. The configuration may come inline via
// --conf or from a file via --conf-file, but not both. Whichever source is
// used, the payload must be a JSON object so it maps to the task params the
// runtime exposes as {{ params.X }}; an array or scalar is rejected. With
// neither flag the body is an empty object, preserving the prior behavior.
func triggerBody(conf, confFile string) ([]byte, error) {
	if conf != "" && confFile != "" {
		return nil, fmt.Errorf("--conf and --conf-file are mutually exclusive; pass only one")
	}
	raw := conf
	if confFile != "" {
		data, err := os.ReadFile(confFile)
		if err != nil {
			return nil, fmt.Errorf("reading --conf-file %q: %w", confFile, err)
		}
		raw = string(data)
	}
	if strings.TrimSpace(raw) == "" {
		return []byte("{}"), nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return nil, fmt.Errorf("conf must be a JSON object: %w", err)
	}
	body, err := json.Marshal(map[string]json.RawMessage{"conf": json.RawMessage(raw)})
	if err != nil {
		return nil, fmt.Errorf("building request body: %w", err)
	}
	return body, nil
}

func newRunsStatusCommand() *cobra.Command {
	var serverURL, token, runID string
	cmd := &cobra.Command{
		Use:   "status <dag_id>",
		Short: "Show the state of a DAG run (the latest by default).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			state, id, err := fetchRunState(cmdContext(cmd), base, bearer, args[0], runID)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", id, state)
			return err
		},
	}
	addRunsFlags(cmd, &serverURL, &token)
	cmd.Flags().StringVar(&runID, "run", "", "specific dag_run_id (default: the most recent run)")
	return cmd
}

// fetchRunState returns the state and id of the named run, or of the most recent
// run when runID is empty.
func fetchRunState(ctx context.Context, base, token, dagID, runID string) (state, id string, err error) {
	root := strings.TrimRight(base, "/") + "/api/v2/dags/" + neturl.PathEscape(dagID) + "/dagRuns"
	if runID != "" {
		return decodeRun(ctx, root+"/"+neturl.PathEscape(runID), token)
	}
	// Ask for the newest run explicitly. The control plane does not read
	// order_by yet (Airflow does) and already lists a DAG's runs newest first
	// (logical_date DESC, run_id DESC); the parameter keeps this right if that
	// default changes or the server is Airflow itself.
	status, raw, err := apiRequest(ctx, http.MethodGet, root+"?order_by=-logical_date&limit=1", token, nil)
	if err != nil {
		return "", "", err
	}
	if status >= http.StatusMultipleChoices {
		return "", "", apiStatusError(status, raw)
	}
	var list struct {
		DagRuns []struct {
			DagRunID string `json:"dag_run_id"`
			State    string `json:"state"`
		} `json:"dag_runs"`
	}
	if jerr := json.Unmarshal(raw, &list); jerr != nil {
		return "", "", fmt.Errorf("parsing response: %w", jerr)
	}
	if len(list.DagRuns) == 0 {
		return "", "", fmt.Errorf("no runs found for %q", dagID)
	}
	return list.DagRuns[0].State, list.DagRuns[0].DagRunID, nil
}

func decodeRun(ctx context.Context, url, token string) (state, id string, err error) {
	status, raw, err := apiRequest(ctx, http.MethodGet, url, token, nil)
	if err != nil {
		return "", "", err
	}
	if status >= http.StatusMultipleChoices {
		return "", "", apiStatusError(status, raw)
	}
	var r struct {
		DagRunID string `json:"dag_run_id"`
		State    string `json:"state"`
	}
	if jerr := json.Unmarshal(raw, &r); jerr != nil {
		return "", "", fmt.Errorf("parsing response: %w", jerr)
	}
	return r.State, r.DagRunID, nil
}

// newRunsLogsCommand builds `dexaflow runs logs <dag_id> [run_id [task_id]]`:
// read task logs from the CLI. The control plane persists the logs the agent
// captures and serves them at the Airflow-compatible taskInstances logs route.
//
// With a task_id it streams that task's attempt: `--try` defaults to the task
// instance's current try_number, and `--follow` streams live because the
// endpoint tails a running attempt. Without a task_id it prints every task of
// the run (the latest run when run_id is omitted too), and `--follow` follows
// the run to its end as `runs trigger --follow` does.
func newRunsLogsCommand() *cobra.Command {
	var serverURL, token string
	var try int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <dag_id> [run_id [task_id]]",
		Short: "Print task logs: one task's attempt, or every task of a run (the latest run by default).",
		Long: "Print task logs. With a task_id, print that task's attempt (the latest by default; -f streams it while\n" +
			"it runs). Without one, print every task of the run, or of the latest run when run_id is omitted too.\n" +
			"Without a task_id, -f follows the run to its end: the command exits 0 when the run succeeded,\n" +
			"exits " + strconv.Itoa(exitRunFailed) + " when the run failed (naming the failed tasks), and exits " +
			strconv.Itoa(exitInterrupted) + " after Ctrl-C,\nwhich stops following without canceling the run.",
		Args: cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 3 && try > 0 {
				return fmt.Errorf("--try needs a task_id: dexaflow runs logs <dag_id> <run_id> <task_id> --try N")
			}
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			if len(args) < 3 {
				return runLogsOfRun(cmd, base, bearer, args, follow)
			}
			ctx := cmdContext(cmd)
			dagID, runID, taskID := args[0], args[1], args[2]
			attempt := try
			if attempt <= 0 {
				attempt, err = latestTryNumber(ctx, base, bearer, dagID, runID, taskID)
				if err != nil {
					return err
				}
			}
			return streamTaskLogs(ctx, cmd.OutOrStdout(), "", base, bearer, dagID, runID, taskID, attempt, follow)
		},
	}
	addRunsFlags(cmd, &serverURL, &token)
	cmd.Flags().IntVar(&try, "try", 0, "attempt number to read, with a task_id (default: the task's latest attempt)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "with a task_id, keep streaming its new log lines; without one, follow the run to its end")
	return cmd
}

// runLogsOfRun prints, or with follow follows, every task of a run: the run
// named in args[1], or the latest run of args[0].
func runLogsOfRun(cmd *cobra.Command, base, bearer string, args []string, follow bool) error {
	ctx, out := cmdContext(cmd), cmd.OutOrStdout()
	t := followTarget{base: base, token: bearer, dagID: args[0]}
	if len(args) == 2 {
		t.runID = args[1]
	} else {
		_, id, err := fetchRunState(ctx, base, bearer, t.dagID, "")
		if err != nil {
			return err
		}
		t.runID = id
		if _, err := fmt.Fprintf(out, "Latest run of %s: %s\n", t.dagID, id); err != nil {
			return err
		}
	}
	if follow {
		return followWithSignals(ctx, out, t, followOptions{})
	}
	return printRunLogs(ctx, out, t)
}

// checkFollowTimeout refuses a negative --timeout, and a --timeout without
// --follow.
func checkFollowTimeout(follow bool, timeout time.Duration) error {
	switch {
	case timeout < 0:
		return fmt.Errorf("--timeout must not be negative")
	case timeout != 0 && !follow:
		return fmt.Errorf("--timeout applies to --follow")
	}
	return nil
}

// addFollowFlags registers --follow and --timeout for a command that triggers
// a run.
func addFollowFlags(cmd *cobra.Command, follow *bool, timeout *time.Duration) {
	cmd.Flags().BoolVar(follow, "follow", false, "follow the run: print task state changes and logs, the UI link, and exit with the run's result")
	cmd.Flags().DurationVar(timeout, "timeout", 0, "with --follow, stop following after this long (e.g. 30m) and exit "+strconv.Itoa(exitFollowTimeout)+"; the run keeps going (default: no limit)")
}

// taskInstanceURL builds the Airflow-compatible task-instance base path shared
// by the instance lookup and the logs sub-resource.
func taskInstanceURL(base, dagID, runID, taskID string) string {
	return strings.TrimRight(base, "/") + "/api/v2/dags/" + dagID +
		"/dagRuns/" + runID + "/taskInstances/" + taskID
}

// latestTryNumber resolves the attempt to read when --try is omitted, from the
// task instance's current try_number. A task that has not recorded an attempt
// (nil/zero try_number) still has its first attempt served under try 1, so we
// fall back to 1 rather than erroring.
func latestTryNumber(ctx context.Context, base, token, dagID, runID, taskID string) (int, error) {
	status, raw, err := apiRequest(ctx, http.MethodGet, taskInstanceURL(base, dagID, runID, taskID), token, nil)
	if err != nil {
		return 0, err
	}
	if status >= http.StatusMultipleChoices {
		return 0, apiStatusError(status, raw)
	}
	var ti struct {
		TryNumber *int `json:"try_number"`
	}
	if jerr := json.Unmarshal(raw, &ti); jerr != nil {
		return 0, fmt.Errorf("parsing task instance: %w", jerr)
	}
	if ti.TryNumber == nil || *ti.TryNumber < 1 {
		return 1, nil
	}
	return *ti.TryNumber, nil
}

// streamTaskLogs streams one attempt's logs to w. It reuses newAPIRequest's
// bearer-token injection (the same auth path as apiRequest) but copies the body
// through as a stream rather than buffering, so `--follow` shows live lines as
// the endpoint tails a running attempt. The endpoint answers an attempt with no
// stored logs with a 200 and the "No logs available for this attempt." body,
// which therefore passes straight through here.
//
// The logs are user output: they pass through a logLineWriter, which strips
// terminal control characters and starts each line with prefix.
func streamTaskLogs(ctx context.Context, w io.Writer, prefix, base, token, dagID, runID, taskID string, try int, follow bool) (err error) {
	url := taskInstanceURL(base, dagID, runID, taskID) + "/logs/" + strconv.Itoa(try)
	if follow {
		url += "?follow=true"
	}
	req, err := newAPIRequest(ctx, http.MethodGet, url, token, nil)
	if err != nil {
		return err
	}
	// No client timeout: a followed stream is intentionally long-lived, and an
	// un-followed read completes as soon as the body is drained.
	resp, err := cliHTTPClient(0).Do(req)
	if err != nil {
		return fmt.Errorf("requesting %s: %w", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing response: %w", cerr)
		}
	}()
	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("server returned %d (and its error body could not be read: %w)", resp.StatusCode, readErr)
		}
		return apiStatusError(resp.StatusCode, raw)
	}
	lw := &logLineWriter{w: w, prefix: prefix}
	if _, err = io.Copy(lw, resp.Body); err != nil {
		return fmt.Errorf("streaming logs: %w", err)
	}
	if err = lw.finish(); err != nil {
		return fmt.Errorf("streaming logs: %w", err)
	}
	return nil
}

// addRunsFlags registers the --server/--token flags shared by the runs
// subcommands. The token default carries LEOFLOW_TOKEN so that resolveServerToken
// falls back to the persisted session only when neither flag nor env supplies one.
func addRunsFlags(cmd *cobra.Command, serverURL, token *string) {
	cmd.Flags().StringVar(serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
}

// newAPIRequest builds a control-plane request with the shared bearer-token
// injection, so every call path (buffered apiRequest and the streaming logs
// reader) authenticates identically.
func newAPIRequest(ctx context.Context, method, url, token string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

// apiRequest performs a JSON HTTP request to the control plane and returns the
// status code and raw body.
func apiRequest(ctx context.Context, method, url, token string, body []byte) (status int, raw []byte, err error) {
	req, err := newAPIRequest(ctx, method, url, token, body)
	if err != nil {
		return 0, nil, err
	}
	resp, err := cliHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("requesting %s: %w", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing response: %w", cerr)
		}
	}()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, nil, fmt.Errorf("reading response: %w", readErr)
	}
	return resp.StatusCode, data, nil
}
