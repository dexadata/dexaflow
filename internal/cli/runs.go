package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	cmd := &cobra.Command{
		Use:   "trigger <dag_id>",
		Short: "Trigger a new run of a DAG.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Triggered run %s for %q (state %s)\n", r.DagRunID, args[0], r.State)
			return err
		},
	}
	addRunsFlags(cmd, &serverURL, &token)
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
	root := strings.TrimRight(base, "/") + "/api/v2/dags/" + dagID + "/dagRuns"
	if runID != "" {
		return decodeRun(ctx, root+"/"+runID, token)
	}
	status, raw, err := apiRequest(ctx, http.MethodGet, root, token, nil)
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

// newRunsLogsCommand builds `dexaflow runs logs <dag_id> <run_id> <task_id>`:
// read one task attempt's logs from the CLI. The logs already exist end to end —
// the agent captures stdout/stderr, the control plane persists and serves them
// at the Airflow-compatible taskInstances logs route — but until now only the
// web UI could read them, leaving a CLI/agent workflow with no path in.
//
// The task_id is required (not listed-when-omitted): it keeps this command a
// single, predictable stream, consistent with `runs status` requiring its
// dag_id. `--try` defaults to the task instance's current try_number, so the
// common case ("show me the latest attempt") needs no attempt bookkeeping.
// `--follow` streams live because the endpoint tails a running attempt.
func newRunsLogsCommand() *cobra.Command {
	var serverURL, token string
	var try int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <dag_id> <run_id> <task_id>",
		Short: "Stream a task attempt's logs (the latest attempt by default).",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
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
			return streamTaskLogs(ctx, cmd.OutOrStdout(), base, bearer, dagID, runID, taskID, attempt, follow)
		},
	}
	addRunsFlags(cmd, &serverURL, &token)
	cmd.Flags().IntVar(&try, "try", 0, "attempt number to read (default: the task's latest attempt)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming new log lines while the task is still running")
	return cmd
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
func streamTaskLogs(ctx context.Context, w io.Writer, base, token, dagID, runID, taskID string, try int, follow bool) error {
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
	resp, err := (&http.Client{}).Do(req)
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
	if _, err = io.Copy(w, resp.Body); err != nil {
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
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
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
