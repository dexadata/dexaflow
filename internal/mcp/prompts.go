package mcp

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// Prompt reads are bounded: one DAG list page, and one page of runs per DAG.
const (
	promptFailedRunsPerDag = 25
	promptRunsPerDag       = 100
)

// registerPrompts wires the read-only prompts (#1471). Each one reads the
// control plane with the caller's token to find the runs to look at, then hands
// the model a request that names the tool calls to make. The control plane has
// no cross-DAG run query, so the DAG-wide prompts read one page of runs per DAG.
func (h *handlers) registerPrompts(s *mcpsdk.Server) {
	s.AddPrompt(&mcpsdk.Prompt{
		Name:        "diagnose_latest_failure",
		Description: "Find the most recent failed DAG run (in one DAG, or across all DAGs) and diagnose it.",
		Arguments: []*mcpsdk.PromptArgument{{
			Name:        "dag_id",
			Description: "only look at this DAG; omit to search every DAG",
		}},
	}, h.diagnoseLatestFailurePrompt)
	s.AddPrompt(&mcpsdk.Prompt{
		Name:        "pipeline_health_today",
		Description: "Summarize today's DAG runs (UTC) by state, list the failures, and check control-plane health.",
	}, h.pipelineHealthTodayPrompt)
}

func userPrompt(description, text string) *mcpsdk.GetPromptResult {
	return &mcpsdk.GetPromptResult{
		Description: description,
		Messages:    []*mcpsdk.PromptMessage{{Role: "user", Content: &mcpsdk.TextContent{Text: text}}},
	}
}

// linkSentence ends a prompt with how to link entities, when links are on.
func (h *handlers) linkSentence() string {
	if h.links.base == "" {
		return ""
	}
	return "\nLink every DAG, run, task and log line you mention with the web_url the results carry."
}

type runRef struct {
	dagID, runID, state string
	at                  time.Time // when the run ended or, failing that, started
}

func runRefOf(r apiclient.DAGRun) runRef {
	ref := runRef{dagID: deref(r.DagId), runID: deref(r.DagRunId), state: stateString(r.State)}
	for _, t := range []*time.Time{r.EndDate, r.StartDate, r.QueuedAt, r.LogicalDate} {
		if t != nil {
			ref.at = *t
			break
		}
	}
	return ref
}

// listRuns reads one page of a DAG's runs, optionally only those in state.
func listRuns(ctx context.Context, api *apiclient.ClientWithResponses, dagID, state string, limit int) ([]apiclient.DAGRun, error) {
	params := &apiclient.ListDagRunsParams{Limit: &limit}
	if state != "" {
		params.State = &[]apiclient.ListDagRunsParamsState{apiclient.ListDagRunsParamsState(state)}
	}
	resp, err := api.ListDagRunsWithResponse(ctx, dagID, params)
	if err != nil {
		return nil, fmt.Errorf("listing runs of %s: %w", dagID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, fmt.Errorf("control plane returned %d listing runs of %s", resp.StatusCode(), dagID)
	}
	return deref(resp.JSON200.DagRuns), nil
}

// promptDags returns the DAG ids a prompt covers: the one asked for, or the
// first page of DAGs plus how many were left out.
func (h *handlers) promptDags(ctx context.Context, api *apiclient.ClientWithResponses, dagID string) (ids []string, unchecked int, err error) {
	if dagID != "" {
		return []string{dagID}, 0, nil
	}
	list, err := h.fetchDagList(ctx, api, listDagsInput{Limit: maxDagLim})
	if err != nil {
		return nil, 0, err
	}
	for _, d := range list.Dags {
		ids = append(ids, d.DagID)
	}
	if list.TotalEntries > len(ids) {
		unchecked = list.TotalEntries - len(ids)
	}
	return ids, unchecked, nil
}

func uncheckedNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" Only the first page of DAGs was checked; %d more DAG(s) were not.", n)
}

func (h *handlers) diagnoseLatestFailurePrompt(ctx context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
	var dagID string
	if req != nil && req.Params != nil {
		dagID = strings.TrimSpace(req.Params.Arguments["dag_id"])
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, err
	}
	dags, unchecked, err := h.promptDags(ctx, api, dagID)
	if err != nil {
		return nil, err
	}
	var latest *runRef
	for _, id := range dags {
		runs, err := listRuns(ctx, api, id, string(apiclient.DAGRunStateFailed), promptFailedRunsPerDag)
		if err != nil {
			return nil, err
		}
		for _, r := range runs {
			ref := runRefOf(r)
			if ref.dagID == "" {
				ref.dagID = id
			}
			if latest == nil || ref.at.After(latest.at) {
				latest = &ref
			}
		}
	}

	const desc = "Diagnose the most recent failed DAG run"
	scope := fmt.Sprintf("across %d DAG(s)", len(dags))
	if dagID != "" {
		scope = fmt.Sprintf("in DAG %q", stripControl(dagID))
	}
	if latest == nil {
		return userPrompt(desc, fmt.Sprintf(
			"No failed run was found %s.%s Tell me so; call list_dags if I want to see which DAGs exist.",
			scope, uncheckedNote(unchecked))), nil
	}

	dag, run := stripControl(latest.dagID), stripControl(latest.runID)
	var b strings.Builder
	fmt.Fprintf(&b, "Diagnose the most recent failed Dexaflow run %s: DAG %q, run %q", scope, dag, run)
	if !latest.at.IsZero() {
		fmt.Fprintf(&b, ", which failed at %s", latest.at.UTC().Format(time.RFC3339))
	}
	b.WriteString(".")
	b.WriteString(uncheckedNote(unchecked))
	if u := h.links.run(latest.dagID, latest.runID); u != "" {
		fmt.Fprintf(&b, "\nThe run in the UI: %s", u)
	}
	fmt.Fprintf(&b, "\n\nCall diagnose_run with dag_id %q and run_id %q. ", dag, run)
	b.WriteString("If a failed task's log tail does not show the cause, call search_logs with that task_id, " +
		"its try_number and a word from the error. Then explain the root cause in plain words, " +
		"name the downstream tasks it blocked, and suggest a fix.")
	b.WriteString(h.linkSentence())
	return userPrompt(desc, b.String()), nil
}

func (h *handlers) pipelineHealthTodayPrompt(ctx context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
	api, err := apiFor(h, req)
	if err != nil {
		return nil, err
	}
	now := h.clock().UTC()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	dags, unchecked, err := h.promptDags(ctx, api, "")
	if err != nil {
		return nil, err
	}
	var today []runRef
	for _, id := range dags {
		runs, err := listRuns(ctx, api, id, "", promptRunsPerDag)
		if err != nil {
			return nil, err
		}
		for _, r := range runs {
			if !startedSince(r, since) {
				continue
			}
			ref := runRefOf(r)
			if ref.dagID == "" {
				ref.dagID = id
			}
			today = append(today, ref)
		}
	}
	var failed []runRef
	for _, r := range today {
		if r.state == string(apiclient.DAGRunStateFailed) {
			failed = append(failed, r)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Report the health of my Dexaflow pipelines today (since %s UTC).\n\n", since.Format(time.RFC3339))
	fmt.Fprintf(&b, "%d run(s) today across %d DAG(s)%s.", len(today), len(dags), stateCounts(today))
	b.WriteString(uncheckedNote(unchecked))
	if len(failed) > 0 {
		b.WriteString("\n\nFailed runs:")
		for _, f := range failed {
			fmt.Fprintf(&b, "\n- DAG %q, run %q", stripControl(f.dagID), stripControl(f.runID))
			if u := h.links.run(f.dagID, f.runID); u != "" {
				fmt.Fprintf(&b, " (%s)", u)
			}
		}
	}
	b.WriteString("\n\nRead the health://control-plane resource for component status. ")
	if len(failed) > 0 {
		b.WriteString("For each failed run, call diagnose_run with its dag_id and run_id. ")
	}
	b.WriteString("Then summarize: the overall status, what failed and why, what is still running or queued, " +
		"and anything that needs my attention.")
	b.WriteString(h.linkSentence())
	return userPrompt("Today's pipeline health", b.String()), nil
}

// stateCounts renders ": failed 1, success 3" for runs, states sorted; "" for
// no runs.
func stateCounts(runs []runRef) string {
	if len(runs) == 0 {
		return ""
	}
	byState := map[string]int{}
	for _, r := range runs {
		byState[r.state]++
	}
	states := make([]string, 0, len(byState))
	for s := range byState {
		states = append(states, s)
	}
	sort.Strings(states)
	parts := make([]string, 0, len(states))
	for _, s := range states {
		parts = append(parts, fmt.Sprintf("%s %d", stripControl(s), byState[s]))
	}
	return ": " + strings.Join(parts, ", ")
}

// startedSince reports whether a run belongs to the day starting at since: it
// started then or later, or, not started yet, was queued or scheduled for then.
func startedSince(r apiclient.DAGRun, since time.Time) bool {
	for _, t := range []*time.Time{r.StartDate, r.QueuedAt, r.LogicalDate} {
		if t != nil {
			return !t.Before(since)
		}
	}
	return false
}
