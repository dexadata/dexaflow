package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// WithRunControl registers the run control tools (ADR 0067, ADR 0050 D7
// phase 4): trigger_run, clear_task, pause_dag, unpause_dag and apply_plan.
// key signs plans; nil draws a random key, which suits stdio only, since every
// HTTP replica must share the key (LoadPlanKey). Without this option the tools
// are not registered at all.
//
// A non-nil key shorter than PlanKeyMinBytes panics: an empty or short HMAC
// key would let anyone sign a plan, so it is a programming error, never a
// configuration the server runs with. Load operator keys with LoadPlanKey.
func WithRunControl(key []byte) Option {
	if key != nil && len(key) < PlanKeyMinBytes {
		panic(fmt.Sprintf("mcp.WithRunControl: plan key of %d bytes, want at least %d", len(key), PlanKeyMinBytes))
	}
	return func(h *handlers) {
		if key == nil {
			key = randomPlanKey()
		}
		h.planKey = key
	}
}

func boolPtr(b bool) *bool { return &b }

// registerRunControl adds the run control tools when WithRunControl is set.
// Every call uses the caller's token, so the control plane's roles and the
// token's scopes decide; the MCP adds no privilege.
func (h *handlers) registerRunControl(s *mcpsdk.Server) {
	if h.planKey == nil {
		return
	}
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "trigger_run",
		Description: "Start a new run of a DAG now, optionally with a conf object. Ask the user before calling it.",
		Annotations: &mcpsdk.ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(true)},
	}, h.triggerRun)
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "clear_task",
		Description: "Clear task instances of a DAG run so they run again (by default only failed ones). " +
			"A clear that touches one task instance happens at once. One that touches more only returns a plan: " +
			"show it to the user, and call apply_plan with its plan_id only if they agree.",
		Annotations: &mcpsdk.ToolAnnotations{DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(true)},
	}, h.clearTask)
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "pause_dag",
		Description: "Pause a DAG: the scheduler starts no new runs of it until it is unpaused.",
		Annotations: &mcpsdk.ToolAnnotations{DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
	}, h.pauseDag)
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "unpause_dag",
		Description: "Unpause a DAG. For a DAG with a schedule this only returns a plan, because unpausing starts runs: " +
			"show it to the user, and call apply_plan with its plan_id only if they agree.",
		Annotations: &mcpsdk.ToolAnnotations{DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
	}, h.unpauseDag)
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "apply_plan",
		Description: "Carry out a plan that clear_task or unpause_dag returned, exactly as it was shown, after the user agreed. " +
			"A plan lasts 10 minutes, only its maker can apply it, and it is refused if what it would change has changed since.",
		Annotations: &mcpsdk.ToolAnnotations{DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(true)},
	}, h.applyPlan)
}

// controlTI is a task instance in a run control result.
type controlTI struct {
	TaskID   string `json:"task_id"`
	MapIndex int    `json:"map_index"`
	State    string `json:"state"`
	WebURL   string `json:"web_url,omitempty"`
}

// controlOutput is every run control tool's result. Done says whether the
// change was made; a plan leaves it false and carries the plan_id instead.
type controlOutput struct {
	Done          bool        `json:"done"`
	PlanID        string      `json:"plan_id,omitempty"`
	ExpiresAt     string      `json:"expires_at,omitempty"`
	Summary       string      `json:"summary"`
	DagID         string      `json:"dag_id"`
	RunID         string      `json:"run_id,omitempty"`
	IsPaused      *bool       `json:"is_paused,omitempty"`
	TaskInstances []controlTI `json:"task_instances,omitempty"`
	WebURL        string      `json:"web_url,omitempty"`
}

// cpError turns a refused control plane call into an error that carries the
// problem detail (a missing role permission or scope, say) so the model can
// tell the user why.
func cpError(op string, status int, body []byte) error {
	var problem struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &problem) == nil && problem.Detail != "" {
		return fmt.Errorf("control plane returned %d %s: %s", status, op, capLine(stripControl(problem.Detail)))
	}
	return fmt.Errorf("control plane returned %d %s", status, op)
}

type triggerRunInput struct {
	DagID string         `json:"dag_id" jsonschema:"the DAG to run"`
	Conf  map[string]any `json:"conf,omitempty" jsonschema:"optional conf object passed to the run"`
	Note  string         `json:"note,omitempty" jsonschema:"optional note shown on the run"`
}

func (h *handlers) triggerRun(ctx context.Context, req *mcpsdk.CallToolRequest, in triggerRunInput) (*mcpsdk.CallToolResult, controlOutput, error) {
	if in.DagID == "" {
		return nil, controlOutput{}, errors.New("dag_id is required")
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, controlOutput{}, err
	}
	body := apiclient.DAGRunCreate{}
	if in.Conf != nil {
		body.Conf = &in.Conf
	}
	if in.Note != "" {
		body.Note = &in.Note
	}
	resp, err := api.TriggerDagRunWithResponse(ctx, in.DagID, body)
	if err != nil {
		return nil, controlOutput{}, fmt.Errorf("triggering %s: %w", in.DagID, err)
	}
	// The control plane answers 201; the spec says 200. Accept both.
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusCreated {
		return nil, controlOutput{}, cpError("triggering "+in.DagID, resp.StatusCode(), resp.Body)
	}
	var run apiclient.DAGRun
	if err := json.Unmarshal(resp.Body, &run); err != nil {
		return nil, controlOutput{}, fmt.Errorf("decoding the new run: %w", err)
	}
	runID := deref(run.DagRunId)
	return nil, controlOutput{
		Done:    true,
		Summary: fmt.Sprintf("Started run %q of DAG %q (state %s).", stripControl(runID), stripControl(in.DagID), stateString(run.State)),
		DagID:   in.DagID,
		RunID:   runID,
		WebURL:  h.links.run(in.DagID, runID),
	}, nil
}

type dagInput struct {
	DagID string `json:"dag_id" jsonschema:"the DAG id"`
}

// setPaused PATCHes is_paused and reports the DAG's state after it.
func (h *handlers) setPaused(ctx context.Context, api *apiclient.ClientWithResponses, dagID string, paused bool) (controlOutput, error) {
	resp, err := api.UpdateDagWithResponse(ctx, dagID, apiclient.DAGUpdate{IsPaused: &paused})
	if err != nil {
		return controlOutput{}, fmt.Errorf("updating %s: %w", dagID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return controlOutput{}, cpError("updating "+dagID, resp.StatusCode(), resp.Body)
	}
	now := deref(resp.JSON200.IsPaused)
	verb := "Unpaused"
	if now {
		verb = "Paused"
	}
	return controlOutput{
		Done:     true,
		Summary:  fmt.Sprintf("%s DAG %q.", verb, stripControl(dagID)),
		DagID:    dagID,
		IsPaused: &now,
		WebURL:   h.links.dag(dagID),
	}, nil
}

func (h *handlers) pauseDag(ctx context.Context, req *mcpsdk.CallToolRequest, in dagInput) (*mcpsdk.CallToolResult, controlOutput, error) {
	if in.DagID == "" {
		return nil, controlOutput{}, errors.New("dag_id is required")
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, controlOutput{}, err
	}
	out, err := h.setPaused(ctx, api, in.DagID, true)
	return nil, out, err
}

// dagState reads what an unpause plan shows: the paused flag, the schedule
// ("" when the DAG has none) and catchup.
func dagState(ctx context.Context, api *apiclient.ClientWithResponses, dagID string) (paused bool, schedule string, catchup bool, err error) {
	resp, err := api.GetDagWithResponse(ctx, dagID)
	if err != nil {
		return false, "", false, fmt.Errorf("reading %s: %w", dagID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return false, "", false, cpError("reading "+dagID, resp.StatusCode(), resp.Body)
	}
	d := resp.JSON200
	if d.ScheduleInterval != nil && len(*d.ScheduleInterval) > 0 {
		if v, ok := (*d.ScheduleInterval)["value"].(string); ok {
			schedule = v
		} else if b, merr := json.Marshal(*d.ScheduleInterval); merr == nil {
			schedule = string(b)
		} else {
			schedule = "unreadable schedule"
		}
	}
	return deref(d.IsPaused), schedule, deref(d.Catchup), nil
}

func (h *handlers) unpauseDag(ctx context.Context, req *mcpsdk.CallToolRequest, in dagInput) (*mcpsdk.CallToolResult, controlOutput, error) {
	if in.DagID == "" {
		return nil, controlOutput{}, errors.New("dag_id is required")
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, controlOutput{}, err
	}
	paused, schedule, catchup, err := dagState(ctx, api, in.DagID)
	if err != nil {
		return nil, controlOutput{}, err
	}
	if !paused || schedule == "" {
		out, perr := h.setPaused(ctx, api, in.DagID, false)
		return nil, out, perr
	}
	summary := fmt.Sprintf("Unpausing DAG %q starts its schedule %q.", stripControl(in.DagID), stripControl(schedule))
	if catchup {
		summary += " Catchup is on, so runs for intervals missed while it was paused may start too."
	}
	out, err := h.planned(req, plan{Action: planUnpause, DagID: in.DagID, Schedule: schedule}, summary)
	if err != nil {
		return nil, controlOutput{}, err
	}
	out.IsPaused = &paused
	out.WebURL = h.links.dag(in.DagID)
	return nil, out, nil
}

type clearTaskInput struct {
	DagID              string   `json:"dag_id" jsonschema:"the DAG id"`
	RunID              string   `json:"run_id" jsonschema:"the DAG run whose task instances to clear"`
	TaskIDs            []string `json:"task_ids,omitempty" jsonschema:"tasks to clear; omit to clear the whole run"`
	IncludeDownstream  bool     `json:"include_downstream,omitempty" jsonschema:"also clear the tasks downstream of task_ids"`
	IncludeUpstream    bool     `json:"include_upstream,omitempty" jsonschema:"also clear the tasks upstream of task_ids"`
	OnlyFailed         *bool    `json:"only_failed,omitempty" jsonschema:"only clear failed task instances (default true)"`
	RunOnLatestVersion bool     `json:"run_on_latest_version,omitempty" jsonschema:"re-run on the DAG's current version instead of the run's own"`
}

// clearParams is a clear as the plan records it: everything but dry_run.
type clearParams struct {
	RunID              string   `json:"r"`
	TaskIDs            []string `json:"t,omitempty"`
	IncludeDownstream  bool     `json:"dn,omitempty"`
	IncludeUpstream    bool     `json:"up,omitempty"`
	OnlyFailed         bool     `json:"of"`
	RunOnLatestVersion bool     `json:"lv,omitempty"`
}

// body spells every flag out, so the preview and the clear send the same
// request but for dry_run.
func (p clearParams) body(dryRun bool) apiclient.ClearTaskInstancesJSONRequestBody {
	b := apiclient.ClearTaskInstancesJSONRequestBody{
		DagRunId:           &p.RunID,
		DryRun:             &dryRun,
		IncludeDownstream:  &p.IncludeDownstream,
		IncludeUpstream:    &p.IncludeUpstream,
		OnlyFailed:         &p.OnlyFailed,
		RunOnLatestVersion: &p.RunOnLatestVersion,
	}
	if len(p.TaskIDs) > 0 {
		ids := p.TaskIDs
		b.TaskIds = &ids
	}
	return b
}

// narrowedTo is the clear that runs after a preview: it names exactly the
// previewed tasks and expands to nothing more, keeping every other flag. A
// task that fails between the preview and the clear is then not swept in
// unseen, as a whole-run or downstream clear would.
func (p clearParams) narrowedTo(preview []planTI) clearParams {
	n := p
	n.TaskIDs = nil
	for _, ti := range preview {
		if !slices.Contains(n.TaskIDs, ti.TaskID) {
			n.TaskIDs = append(n.TaskIDs, ti.TaskID)
		}
	}
	n.IncludeDownstream, n.IncludeUpstream = false, false
	return n
}

// clearedSummary reports a clear, and says so when the control plane cleared
// more task instances than the preview showed (their state moved meanwhile).
func clearedSummary(runID string, cleared, previewed int) string {
	s := fmt.Sprintf("Cleared %d task instance(s) of run %q; they will run again.", cleared, stripControl(runID))
	if cleared > previewed {
		s += fmt.Sprintf(" That is more than the %d the preview showed: more of the same tasks failed in between.", previewed)
	}
	return s
}

// sendClear runs the clear (or its preview) and returns the task instances it
// touches (or would).
func sendClear(ctx context.Context, api *apiclient.ClientWithResponses, dagID string, p clearParams, dryRun bool) ([]planTI, error) {
	resp, err := api.ClearTaskInstancesWithResponse(ctx, dagID, p.body(dryRun))
	if err != nil {
		return nil, fmt.Errorf("clearing task instances of %s: %w", dagID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, cpError("clearing task instances of "+dagID, resp.StatusCode(), resp.Body)
	}
	tis := deref(resp.JSON200.TaskInstances)
	out := make([]planTI, 0, len(tis))
	for _, ti := range tis {
		out = append(out, planTI{TaskID: deref(ti.TaskId), MapIndex: deref(ti.MapIndex), State: stateString(ti.State), Try: deref(ti.TryNumber)})
	}
	return out, nil
}

func (h *handlers) clearTask(ctx context.Context, req *mcpsdk.CallToolRequest, in clearTaskInput) (*mcpsdk.CallToolResult, controlOutput, error) {
	if in.DagID == "" || in.RunID == "" {
		return nil, controlOutput{}, errors.New("dag_id and run_id are required")
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, controlOutput{}, err
	}
	p := clearParams{
		RunID: in.RunID, TaskIDs: in.TaskIDs,
		IncludeDownstream: in.IncludeDownstream, IncludeUpstream: in.IncludeUpstream,
		OnlyFailed: in.OnlyFailed == nil || *in.OnlyFailed, RunOnLatestVersion: in.RunOnLatestVersion,
	}
	preview, err := sendClear(ctx, api, in.DagID, p, true)
	if err != nil {
		return nil, controlOutput{}, err
	}
	switch {
	case len(preview) == 0:
		return nil, h.clearOutput(in.DagID, in.RunID, nil, false, "Nothing to clear: no task instance of the run matches."), nil
	case len(preview) == 1:
		done, cerr := sendClear(ctx, api, in.DagID, p.narrowedTo(preview), false)
		if cerr != nil {
			return nil, controlOutput{}, cerr
		}
		return nil, h.clearOutput(in.DagID, in.RunID, done, true, clearedSummary(in.RunID, len(done), len(preview))), nil
	case len(preview) > maxPlanTaskInstances:
		return nil, controlOutput{}, fmt.Errorf("this clear touches %d task instances, more than %d; narrow it with task_ids or use the UI", len(preview), maxPlanTaskInstances)
	}
	summary := fmt.Sprintf("Clearing would make %d task instances of run %q run again (listed).", len(preview), stripControl(in.RunID))
	out, err := h.planned(req, plan{Action: planClear, DagID: in.DagID, Clear: &p, Expect: preview}, summary)
	if err != nil {
		return nil, controlOutput{}, err
	}
	shown := h.clearOutput(in.DagID, in.RunID, preview, false, "")
	out.RunID, out.TaskInstances, out.WebURL = shown.RunID, shown.TaskInstances, shown.WebURL
	return nil, out, nil
}

func (h *handlers) clearOutput(dagID, runID string, tis []planTI, done bool, summary string) controlOutput {
	out := controlOutput{Done: done, Summary: summary, DagID: dagID, RunID: runID, WebURL: h.links.run(dagID, runID)}
	for _, ti := range tis {
		out.TaskInstances = append(out.TaskInstances, controlTI{
			TaskID: ti.TaskID, MapIndex: ti.MapIndex, State: ti.State,
			WebURL: h.links.task(dagID, runID, ti.TaskID, ti.Try),
		})
	}
	return out
}

// planned seals p for the caller and wraps it as a plan result.
func (h *handlers) planned(req *mcpsdk.CallToolRequest, p plan, summary string) (controlOutput, error) {
	caller, err := h.callerKey(req)
	if err != nil {
		return controlOutput{}, err
	}
	expires := h.clock().Add(planTTL).UTC()
	p.Caller, p.Expires = caller, expires.Unix()
	id, err := sealPlan(h.planKey, p)
	if err != nil {
		return controlOutput{}, err
	}
	return controlOutput{
		PlanID:    id,
		ExpiresAt: expires.Format(time.RFC3339),
		Summary:   summary + " Nothing has changed yet: show this to the user and call apply_plan with this plan_id only if they agree.",
		DagID:     p.DagID,
	}, nil
}

type applyPlanInput struct {
	PlanID string `json:"plan_id" jsonschema:"the plan_id that clear_task or unpause_dag returned"`
}

func (h *handlers) applyPlan(ctx context.Context, req *mcpsdk.CallToolRequest, in applyPlanInput) (*mcpsdk.CallToolResult, controlOutput, error) {
	caller, err := h.callerKey(req)
	if err != nil {
		return nil, controlOutput{}, err
	}
	p, err := openPlan(h.planKey, in.PlanID, caller, h.clock())
	if err != nil {
		return nil, controlOutput{}, err
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, controlOutput{}, err
	}
	switch p.Action {
	case planClear:
		out, err := h.applyClear(ctx, api, p)
		return nil, out, err
	case planUnpause:
		out, err := h.applyUnpause(ctx, api, p)
		return nil, out, err
	default:
		return nil, controlOutput{}, fmt.Errorf("unknown plan action %q", p.Action)
	}
}

// applyClear re-previews the clear and runs it only if it would touch exactly
// the task instances, in the states, that the plan showed.
func (h *handlers) applyClear(ctx context.Context, api *apiclient.ClientWithResponses, p plan) (controlOutput, error) {
	if p.Clear == nil {
		return controlOutput{}, errors.New("plan has no clear")
	}
	now, err := sendClear(ctx, api, p.DagID, *p.Clear, true)
	if err != nil {
		return controlOutput{}, err
	}
	if !sameTIs(now, p.Expect) {
		return controlOutput{}, errors.New("the task instances changed since the plan was made; make a new plan")
	}
	done, err := sendClear(ctx, api, p.DagID, p.Clear.narrowedTo(p.Expect), false)
	if err != nil {
		return controlOutput{}, err
	}
	return h.clearOutput(p.DagID, p.Clear.RunID, done, true, clearedSummary(p.Clear.RunID, len(done), len(p.Expect))), nil
}

// sameTIs compares two task instance sets regardless of order: the same task
// instances, in the same states, on the same attempts.
func sameTIs(a, b []planTI) bool {
	key := func(t planTI) string {
		return fmt.Sprintf("%s\x00%d\x00%s\x00%d", t.TaskID, t.MapIndex, t.State, t.Try)
	}
	ka := make([]string, 0, len(a))
	for _, t := range a {
		ka = append(ka, key(t))
	}
	kb := make([]string, 0, len(b))
	for _, t := range b {
		kb = append(kb, key(t))
	}
	slices.Sort(ka)
	slices.Sort(kb)
	return slices.Equal(ka, kb)
}

// applyUnpause unpauses only a DAG that is still paused on the schedule the
// plan showed.
func (h *handlers) applyUnpause(ctx context.Context, api *apiclient.ClientWithResponses, p plan) (controlOutput, error) {
	paused, schedule, _, err := dagState(ctx, api, p.DagID)
	if err != nil {
		return controlOutput{}, err
	}
	if !paused || schedule != p.Schedule {
		return controlOutput{}, fmt.Errorf("DAG %q changed since the plan was made (paused %v, schedule %q); make a new plan",
			stripControl(p.DagID), paused, strings.TrimSpace(stripControl(schedule)))
	}
	return h.setPaused(ctx, api, p.DagID, false)
}
