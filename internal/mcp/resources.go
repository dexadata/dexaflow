package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// resourceLogTail bounds a log resource read; a task log an agent fetches
// wholesale still must not blow the context window.
const resourceLogTail = 200

// registerResources wires the read-only addressable resources (ADR 0050 D7).
// The agent chooses the URI; the control plane authorizes each read via the
// pass-through token, so a resource can only surface what the caller already may
// see. Templates carry their parameters IN the URI.
func (h *handlers) registerResources(s *mcpsdk.Server) {
	s.AddResource(&mcpsdk.Resource{
		Name: "dags", URI: "dag://list", MIMEType: "application/json",
		Description: "All DAGs registered in the control plane (compact).",
	}, h.readDagList)
	s.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		Name: "run-detail", URITemplate: "run://detail/{dag_id}/{run_id}", MIMEType: "application/json",
		Description: "A DAG run's detail (state, type, timing).",
	}, h.readRunDetail)
	s.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		Name: "task-instances", URITemplate: "task://instances/{dag_id}/{run_id}", MIMEType: "application/json",
		Description: "The task instances of a DAG run (state, try, duration).",
	}, h.readTaskInstances)
	s.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		Name: "task-log", URITemplate: "log://task/{dag_id}/{run_id}/{task_id}/{try_number}", MIMEType: "text/plain",
		Description: "A task attempt's log, truncated to the last lines and sanitized.",
	}, h.readTaskLog)
	s.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		Name: "dag-source", URITemplate: "dag://source/{dag_id}", MIMEType: "text/plain",
		Description: "A DAG's source (the dag.py text), sanitized and size-capped.",
	}, h.readDagSource)
	s.AddResourceTemplate(&mcpsdk.ResourceTemplate{
		Name: "dag-spec", URITemplate: "dag://spec/{dag_id}", MIMEType: "application/json",
		Description: "A DAG's compiled spec (the dag.json artifact: the structured task graph).",
	}, h.readDagSpec)
	s.AddResource(&mcpsdk.Resource{
		Name: "control-plane-health", URI: "health://control-plane", MIMEType: "application/json",
		Description: "Control-plane health: component status, executor capability, and version (best-effort per section).",
	}, h.readHealth)
}

// maxSourceBytes caps a dag.py source resource read so a large DAG file can't
// blow the agent's context window.
const maxSourceBytes = 64 * 1024

func (h *handlers) readDagSource(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	p, err := uriParams(req.Params.URI, "dag://source/", 1)
	if err != nil {
		return nil, err
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, err
	}
	resp, err := api.GetDagSourceWithResponse(ctx, p[0])
	if err != nil {
		return nil, fmt.Errorf("fetching dag source: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, fmt.Errorf("control plane returned %d fetching dag source for %s", resp.StatusCode(), p[0])
	}
	src := stripControl(deref(resp.JSON200.Content))
	if len(src) > maxSourceBytes {
		src = src[:maxSourceBytes] + "\n... [truncated]"
	}
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
		URI: req.Params.URI, MIMEType: "text/plain", Text: src,
	}}}, nil
}

// maxSpecBytes guards a dag.json resource read; the compiled artifact is small
// in practice, but a runaway must not blow the context window. JSON can't
// be safely truncated, so an oversize spec yields a structured note instead.
const maxSpecBytes = 128 * 1024

func (h *handlers) readDagSpec(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	p, err := uriParams(req.Params.URI, "dag://spec/", 1)
	if err != nil {
		return nil, err
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, err
	}
	resp, err := api.GetDagSpecWithResponse(ctx, p[0])
	if err != nil {
		return nil, fmt.Errorf("fetching dag spec: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("control plane returned %d fetching dag spec for %s", resp.StatusCode(), p[0])
	}
	if len(resp.Body) > maxSpecBytes {
		return jsonResource(req.Params.URI, map[string]any{
			"error": "compiled spec too large for a resource read", "bytes": len(resp.Body), "dag_id": p[0],
		})
	}
	// The spec is structured, but its string values (task descriptions, owners,
	// tags) are author-controlled free text; an ANSI/control byte stored as the JSON escape \u001b
	// would decode to a live escape in the agent's context (D10). Decode, strip
	// control bytes from every string value, and re-encode — and treat a
	// non-JSON 200 as an error rather than forwarding it as application/json.
	var spec any
	if err := json.Unmarshal(resp.Body, &spec); err != nil {
		return nil, fmt.Errorf("control plane returned a non-JSON spec for %s: %w", p[0], err)
	}
	return jsonResource(req.Params.URI, sanitizeJSONValue(spec))
}

// sanitizeJSONValue recursively strips control/ANSI bytes from every string in a
// decoded JSON tree (map values, slice elements, nested strings), leaving
// structure and non-string scalars untouched. Map keys are field names from the
// compiler, not author free text, so they are left as-is.
func sanitizeJSONValue(v any) any {
	switch t := v.(type) {
	case string:
		return stripControl(t)
	case []any:
		for i := range t {
			t[i] = sanitizeJSONValue(t[i])
		}
		return t
	case map[string]any:
		for k, val := range t {
			t[k] = sanitizeJSONValue(val)
		}
		return t
	default:
		return v
	}
}

// readHealth composes the three monitor endpoints into one snapshot, best-effort
// per section: a section that fails is omitted rather than failing the whole
// read (structured degradation), and an all-empty result is an error so the
// agent knows the control plane is unreachable rather than "healthy".
func (h *handlers) readHealth(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	api, err := apiFor(h, req)
	if err != nil {
		return nil, err
	}
	snap := map[string]any{}
	if r, err := api.GetMonitorHealthWithResponse(ctx); err == nil && r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		snap["health"] = r.JSON200
	}
	if r, err := api.GetMonitorExecutorWithResponse(ctx); err == nil && r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		snap["executor"] = r.JSON200
	}
	if r, err := api.GetVersionWithResponse(ctx); err == nil && r.StatusCode() == http.StatusOK && r.JSON200 != nil {
		snap["version"] = r.JSON200
	}
	if len(snap) == 0 {
		return nil, fmt.Errorf("control plane unreachable (no monitor section responded)")
	}
	return jsonResource(req.Params.URI, snap)
}

func (h *handlers) readDagList(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	api, err := apiFor(h, req)
	if err != nil {
		return nil, err
	}
	out, err := h.fetchDagList(ctx, api, listDagsInput{})
	if err != nil {
		return nil, err
	}
	return jsonResource(req.Params.URI, out)
}

func (h *handlers) readRunDetail(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	p, err := uriParams(req.Params.URI, "run://detail/", 2)
	if err != nil {
		return nil, err
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, err
	}
	resp, err := api.GetDagRunWithResponse(ctx, p[0], p[1])
	if err != nil {
		return nil, fmt.Errorf("fetching run: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, fmt.Errorf("control plane returned %d for run %s/%s", resp.StatusCode(), p[0], p[1])
	}
	r := resp.JSON200
	detail := map[string]any{
		"dag_id":   deref(r.DagId),
		"run_id":   deref(r.DagRunId),
		"state":    stateString(r.State),
		"run_type": runTypeString(r.RunType),
	}
	if r.StartDate != nil {
		detail["start_date"] = r.StartDate
	}
	if r.EndDate != nil {
		detail["end_date"] = r.EndDate
	}
	return jsonResource(req.Params.URI, detail)
}

type taskInstanceSummary struct {
	TaskID          string  `json:"task_id"`
	State           string  `json:"state"`
	TryNumber       int     `json:"try_number"`
	DurationSeconds float32 `json:"duration_seconds,omitempty"`
}

func (h *handlers) readTaskInstances(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	p, err := uriParams(req.Params.URI, "task://instances/", 2)
	if err != nil {
		return nil, err
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, err
	}
	resp, err := api.ListTaskInstancesWithResponse(ctx, p[0], p[1])
	if err != nil {
		return nil, fmt.Errorf("listing task instances: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, fmt.Errorf("control plane returned %d listing task instances", resp.StatusCode())
	}
	var tis []apiclient.TaskInstance
	if resp.JSON200.TaskInstances != nil {
		tis = *resp.JSON200.TaskInstances
	}
	out := make([]taskInstanceSummary, 0, len(tis))
	for _, ti := range tis {
		out = append(out, taskInstanceSummary{
			TaskID:          deref(ti.TaskId),
			State:           stateString(ti.State),
			TryNumber:       deref(ti.TryNumber),
			DurationSeconds: deref(ti.Duration),
		})
	}
	return jsonResource(req.Params.URI, map[string]any{"task_instances": out, "total_entries": len(out)})
}

func (h *handlers) readTaskLog(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	p, err := uriParams(req.Params.URI, "log://task/", 4)
	if err != nil {
		return nil, err
	}
	try, err := strconv.Atoi(p[3])
	if err != nil || try < 1 {
		return nil, fmt.Errorf("uri %q: try_number must be a positive integer", req.Params.URI)
	}
	api, err := apiFor(h, req)
	if err != nil {
		return nil, err
	}
	resp, err := api.GetTaskLogsWithResponse(ctx, p[0], p[1], p[2], try)
	if err != nil {
		return nil, fmt.Errorf("fetching task log: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("control plane returned %d fetching task log", resp.StatusCode())
	}
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
		URI: req.Params.URI, MIMEType: "text/plain", Text: sanitizeLogTail(string(resp.Body), resourceLogTail),
	}}}, nil
}

// uriParams strips scheme+prefix from a resource URI and returns exactly n
// non-empty, URL-decoded path segments. A path segment supplied by the agent is
// data, never a raw selector: it is decoded here and passed to the typed
// client, which URL-encodes it back into the request path — so a "../" or a stray
// slash cannot escape the intended endpoint.
func uriParams(uri, prefix string, n int) ([]string, error) {
	rest, ok := strings.CutPrefix(uri, prefix)
	if !ok {
		return nil, fmt.Errorf("uri %q does not match template %q", uri, prefix)
	}
	segs := strings.Split(rest, "/")
	if len(segs) != n {
		return nil, fmt.Errorf("uri %q: want %d segments after %q, got %d", uri, n, prefix, len(segs))
	}
	out := make([]string, n)
	for i, s := range segs {
		d, derr := url.PathUnescape(s)
		if derr != nil {
			return nil, fmt.Errorf("uri %q: bad segment %q: %w", uri, s, derr)
		}
		if d == "" {
			return nil, fmt.Errorf("uri %q: empty path segment", uri)
		}
		out[i] = d
	}
	return out, nil
}

func jsonResource(uri string, v any) (*mcpsdk.ReadResourceResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding resource %q: %w", uri, err)
	}
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
		URI: uri, MIMEType: "application/json", Text: string(b),
	}}}, nil
}

func stateString[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func runTypeString(p *apiclient.DAGRunRunType) string {
	if p == nil {
		return ""
	}
	return string(*p)
}
