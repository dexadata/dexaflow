package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeSpecReader struct {
	spec domain.DAGSpec
	err  error
}

func (f *fakeSpecReader) GetCurrentSpec(context.Context, string, string) (domain.DAGSpec, error) {
	if f.err != nil {
		return domain.DAGSpec{}, f.err
	}
	return f.spec, nil
}

func structureServer(specs DagSpecReader) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Specs:         specs,
	})
}

// diamond: extract -> {transform_a, transform_b} -> load
func diamondSpec() domain.DAGSpec {
	return domain.DAGSpec{
		DagID: "etl",
		Tasks: []domain.TaskSpec{
			{TaskID: "load", Type: "python", DependsOn: []string{"transform_a", "transform_b"}},
			{TaskID: "transform_b", Type: "bash", DependsOn: []string{"extract"}},
			{TaskID: "transform_a", Type: "python", DependsOn: []string{"extract"}},
			{TaskID: "extract", Type: "python"},
		},
	}
}

func TestTopoSortRespectsDependencies(t *testing.T) {
	order := topoSortTasks(diamondSpec().Tasks)
	pos := map[string]int{}
	for i, t := range order {
		pos[t.TaskID] = i
	}
	if pos["extract"] != 0 {
		t.Errorf("extract should be first, order=%v", order)
	}
	if pos["load"] != 3 {
		t.Errorf("load should be last, order=%v", order)
	}
	// Ties broken by task_id: transform_a before transform_b.
	if pos["transform_a"] > pos["transform_b"] {
		t.Errorf("ties should break by id: %v", order)
	}
}

func TestGridStructureShape(t *testing.T) {
	rec := authGet(structureServer(&fakeSpecReader{spec: diamondSpec()}), http.MethodGet, "/ui/grid/structure/etl", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("grid structure = %d (%s)", rec.Code, rec.Body.String())
	}
	var nodes []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 || nodes[0]["id"] != "extract" {
		t.Fatalf("unexpected grid nodes: %v", nodes)
	}
	for _, f := range []string{"id", "label", "is_mapped", "children"} {
		if _, ok := nodes[0][f]; !ok {
			t.Errorf("grid node missing required field %q", f)
		}
	}
}

func TestStructureDataNodesAndEdges(t *testing.T) {
	rec := authGet(structureServer(&fakeSpecReader{spec: diamondSpec()}), http.MethodGet, "/ui/structure/structure_data?dag_id=etl", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("structure_data = %d (%s)", rec.Code, rec.Body.String())
	}
	var sd struct {
		Nodes []map[string]any    `json:"nodes"`
		Edges []map[string]string `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sd); err != nil {
		t.Fatal(err)
	}
	if len(sd.Nodes) != 4 {
		t.Errorf("want 4 nodes, got %d", len(sd.Nodes))
	}
	if sd.Nodes[0]["type"] != "task" {
		t.Errorf("node type = %v, want task", sd.Nodes[0]["type"])
	}
	// 4 edges: extract->a, extract->b, a->load, b->load.
	if len(sd.Edges) != 4 {
		t.Fatalf("want 4 edges, got %d: %v", len(sd.Edges), sd.Edges)
	}
	want := map[string]bool{"extract->transform_a": true, "extract->transform_b": true, "transform_a->load": true, "transform_b->load": true}
	for _, e := range sd.Edges {
		k := e["source_id"] + "->" + e["target_id"]
		if !want[k] {
			t.Errorf("unexpected edge %q", k)
		}
	}
}

func TestStructureDataRequiresDagID(t *testing.T) {
	rec := authGet(structureServer(&fakeSpecReader{spec: diamondSpec()}), http.MethodGet, "/ui/structure/structure_data", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing dag_id = %d, want 400", rec.Code)
	}
}

func TestGridStructureNotFound(t *testing.T) {
	rec := authGet(structureServer(&fakeSpecReader{err: ErrNotFound}), http.MethodGet, "/ui/grid/structure/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown dag structure = %d, want 404", rec.Code)
	}
}

func TestTasksEndpoint(t *testing.T) {
	rec := authGet(structureServer(&fakeSpecReader{spec: diamondSpec()}), http.MethodGet, "/api/v2/dags/etl/tasks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/tasks = %d (%s)", rec.Code, rec.Body.String())
	}
	var col struct {
		Tasks []map[string]any `json:"tasks"`
		Total int              `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.Total != 4 || len(col.Tasks) != 4 {
		t.Fatalf("want 4 tasks, got total=%d len=%d", col.Total, len(col.Tasks))
	}
	// extract is first (topo-sorted) and feeds transform_a + transform_b.
	if col.Tasks[0]["task_id"] != "extract" {
		t.Errorf("first task = %v, want extract", col.Tasks[0]["task_id"])
	}
	for _, f := range []string{"operator_name", "trigger_rule", "downstream_task_ids", "class_ref", "is_mapped"} {
		if _, ok := col.Tasks[0][f]; !ok {
			t.Errorf("task missing required field %q", f)
		}
	}
	dn, _ := json.Marshal(col.Tasks[0]["downstream_task_ids"])
	if string(dn) != `["transform_a","transform_b"]` {
		t.Errorf("extract downstream = %s", dn)
	}
}

func TestTaskDetailNotFound(t *testing.T) {
	rec := authGet(structureServer(&fakeSpecReader{spec: diamondSpec()}), http.MethodGet, "/api/v2/dags/etl/tasks/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown task = %d, want 404", rec.Code)
	}
}

func TestDagSourceEndpoint(t *testing.T) {
	rec := authGet(structureServer(&fakeSpecReader{spec: diamondSpec()}), http.MethodGet, "/api/v2/dagSources/etl", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/dagSources = %d", rec.Code)
	}
	var src map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"content", "dag_id", "version_number", "dag_display_name"} {
		if _, ok := src[f]; !ok {
			t.Errorf("dagSource missing %q", f)
		}
	}
	if s, _ := src["content"].(string); !strings.Contains(s, "extract") {
		t.Errorf("source content should include the spec, got %q", s)
	}
}

func TestDagSourceReturnsPythonWhenCaptured(t *testing.T) {
	spec := diamondSpec()
	spec.Source = "from airflow.sdk import dag\n\n@dag\ndef etl():\n    pass\n"
	rec := authGet(structureServer(&fakeSpecReader{spec: spec}), http.MethodGet, "/api/v2/dagSources/etl", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/dagSources = %d", rec.Code)
	}
	var src map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	content, _ := src["content"].(string)
	// The Code tab must show the Python, not the compiled spec JSON.
	if !strings.Contains(content, "@dag") || !strings.Contains(content, "from airflow.sdk") {
		t.Errorf("expected Python source, got %q", content)
	}
	if strings.Contains(content, "\"schema_version\"") {
		t.Errorf("content leaked the compiled spec JSON instead of the Python source")
	}
}
