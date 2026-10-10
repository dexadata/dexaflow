package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// writeDagJSON writes a minimal two-task dag.json to a temp file and returns its
// path, for exercising the compile overlay in isolation.
func writeDagJSON(t *testing.T) string {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0",
		DagID:         "proj",
		DagVersion:    "dev",
		Image:         "test:v1",
		Tasks: []domain.TaskSpec{
			{TaskID: "extract", Type: domain.TaskTypePython, Entrypoint: "dag:extract"},
			{TaskID: "transform", Type: domain.TaskTypePython, Entrypoint: "dag:transform"},
		},
	}
	data, err := json.Marshal(&spec)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "dag.json")
	if werr := os.WriteFile(path, data, 0o600); werr != nil {
		t.Fatalf("write fixture: %v", werr)
	}
	return path
}

// readSpec loads a dag.json from disk for assertions.
func readSpec(t *testing.T, path string) domain.DAGSpec {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled temp path.
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		t.Fatalf("unmarshal spec: %v", uerr)
	}
	return spec
}

func taskByID(spec domain.DAGSpec, id string) (domain.TaskSpec, bool) {
	for _, ts := range spec.Tasks {
		if ts.TaskID == id {
			return ts, true
		}
	}
	return domain.TaskSpec{}, false
}

func TestOverlayProjectAppliesTaskOverrides(t *testing.T) {
	path := writeDagJSON(t)
	retries := 5
	cfg := &domain.LeoflowConfig{
		DagID: "proj",
		Tasks: map[string]*domain.TaskConfig{
			"transform": {
				Retries:   &retries,
				Env:       map[string]string{"TZ": "UTC"},
				Resources: &domain.Resources{Requests: &domain.ResourceQuantity{CPU: "2", Memory: "4Gi"}},
			},
		},
	}
	if err := overlayProject(path, cfg); err != nil {
		t.Fatalf("overlayProject: %v", err)
	}
	got := readSpec(t, path)

	transform, ok := taskByID(got, "transform")
	if !ok {
		t.Fatal("transform task missing after overlay")
	}
	if transform.Retries == nil || *transform.Retries != 5 {
		t.Errorf("transform retries = %v, want 5", transform.Retries)
	}
	if transform.Env["TZ"] != "UTC" {
		t.Errorf("transform env TZ = %q, want UTC", transform.Env["TZ"])
	}
	if transform.Resources == nil || transform.Resources.Requests == nil || transform.Resources.Requests.CPU != "2" {
		t.Errorf("transform resources = %+v, want cpu 2", transform.Resources)
	}

	// The untouched task keeps its compiled shape (no override leaked onto it).
	extract, _ := taskByID(got, "extract")
	if extract.Retries != nil || extract.Resources != nil || len(extract.Env) != 0 {
		t.Errorf("extract was modified by overlay: %+v", extract)
	}
}

// The overlay carries the dexaflow.yaml per-task declared secret sets
// (connections/variables) onto the compiled task, narrowing the DAG-level
// declaration (ADR 0045, ADR 0055). A set list replaces the compiled value.
func TestOverlayProjectAppliesDeclaredSecretNarrowing(t *testing.T) {
	path := writeDagJSON(t)
	cfg := &domain.LeoflowConfig{
		DagID: "proj",
		Tasks: map[string]*domain.TaskConfig{
			"transform": {
				Connections: []string{"warehouse"},
				Variables:   []string{"greeting"},
			},
		},
	}
	if err := overlayProject(path, cfg); err != nil {
		t.Fatalf("overlayProject: %v", err)
	}
	got := readSpec(t, path)

	transform, ok := taskByID(got, "transform")
	if !ok {
		t.Fatal("transform task missing after overlay")
	}
	if len(transform.Connections) != 1 || transform.Connections[0] != "warehouse" {
		t.Errorf("transform connections = %v, want [warehouse]", transform.Connections)
	}
	if len(transform.Variables) != 1 || transform.Variables[0] != "greeting" {
		t.Errorf("transform variables = %v, want [greeting]", transform.Variables)
	}

	// The untouched task declares nothing (no narrowing leaked onto it).
	extract, _ := taskByID(got, "extract")
	if len(extract.Connections) != 0 || len(extract.Variables) != 0 {
		t.Errorf("extract gained a declaration from overlay: %+v", extract)
	}
}

// A dexaflow.yaml `defaults` block with resources/node_selector is a DAG-wide
// fallback: every task that declares neither of its own inherits them, so a user
// relying on defaults.resources reaches Guaranteed QoS (requests == limits)
// instead of silently getting BestEffort (EKS validation aresta #6; the QoS story
// of #725). Before the fix these keys were accepted by the config and then
// dropped — never reaching dag.json or the task pod.
func TestOverlayProjectAppliesDAGDefaults(t *testing.T) {
	path := writeDagJSON(t)
	cfg := &domain.LeoflowConfig{
		DagID: "proj",
		Defaults: &domain.ConfigDefaults{
			Resources:    &domain.DefaultResources{CPU: "1", Memory: "1Gi"},
			NodeSelector: map[string]string{"disktype": "ssd"},
		},
	}
	if err := overlayProject(path, cfg); err != nil {
		t.Fatalf("overlayProject: %v", err)
	}
	got := readSpec(t, path)

	for _, id := range []string{"extract", "transform"} {
		ts, ok := taskByID(got, id)
		if !ok {
			t.Fatalf("%s task missing after overlay", id)
		}
		if ts.Resources == nil || ts.Resources.Requests == nil || ts.Resources.Limits == nil {
			t.Fatalf("%s resources not filled from defaults: %+v", id, ts.Resources)
		}
		if ts.Resources.Requests.CPU != "1" || ts.Resources.Requests.Memory != "1Gi" {
			t.Errorf("%s requests = %+v, want cpu 1 / mem 1Gi", id, ts.Resources.Requests)
		}
		if ts.Resources.Limits.CPU != "1" || ts.Resources.Limits.Memory != "1Gi" {
			t.Errorf("%s limits = %+v, want cpu 1 / mem 1Gi (Guaranteed QoS)", id, ts.Resources.Limits)
		}
		if ts.Execution == nil || ts.Execution.NodeSelector["disktype"] != "ssd" {
			t.Errorf("%s node_selector = %+v, want disktype=ssd", id, ts.Execution)
		}
	}
}

// Per-task resources/node_selector always beat the DAG-wide defaults (most
// specific wins). The default must not overwrite a task's explicit choice nor
// merge partial fields (e.g. inject default Limits into an explicit request-only
// resources block).
func TestOverlayProjectPerTaskOverridesBeatDAGDefaults(t *testing.T) {
	path := writeDagJSON(t)
	cfg := &domain.LeoflowConfig{
		DagID: "proj",
		Defaults: &domain.ConfigDefaults{
			Resources:    &domain.DefaultResources{CPU: "1", Memory: "1Gi"},
			NodeSelector: map[string]string{"disktype": "ssd"},
		},
		Tasks: map[string]*domain.TaskConfig{
			"transform": {
				Resources: &domain.Resources{Requests: &domain.ResourceQuantity{CPU: "8", Memory: "16Gi"}},
				Execution: &domain.Execution{NodeSelector: map[string]string{"gpu": "true"}},
			},
		},
	}
	if err := overlayProject(path, cfg); err != nil {
		t.Fatalf("overlayProject: %v", err)
	}
	got := readSpec(t, path)

	transform, _ := taskByID(got, "transform")
	if transform.Resources == nil || transform.Resources.Requests == nil || transform.Resources.Requests.CPU != "8" {
		t.Errorf("per-task resources should win: %+v", transform.Resources)
	}
	// The default (which sets both requests and limits) must not have its Limits
	// merged onto the explicit request-only per-task resources.
	if transform.Resources != nil && transform.Resources.Limits != nil {
		t.Errorf("default limits leaked onto explicit per-task resources: %+v", transform.Resources.Limits)
	}
	if transform.Execution == nil || transform.Execution.NodeSelector["gpu"] != "true" {
		t.Errorf("per-task node_selector should win: %+v", transform.Execution)
	}
	if transform.Execution != nil && transform.Execution.NodeSelector["disktype"] != "" {
		t.Errorf("default node_selector merged into an explicit one: %+v", transform.Execution.NodeSelector)
	}

	// The task with no override still inherits the DAG-wide defaults.
	extract, _ := taskByID(got, "extract")
	if extract.Resources == nil || extract.Resources.Requests == nil || extract.Resources.Requests.CPU != "1" {
		t.Errorf("extract should inherit default resources: %+v", extract.Resources)
	}
	if extract.Execution == nil || extract.Execution.NodeSelector["disktype"] != "ssd" {
		t.Errorf("extract should inherit default node_selector: %+v", extract.Execution)
	}
}

func TestOverlayProjectUnknownTaskIDErrors(t *testing.T) {
	path := writeDagJSON(t)
	cfg := &domain.LeoflowConfig{
		DagID: "proj",
		Tasks: map[string]*domain.TaskConfig{"typo": {}},
	}
	err := overlayProject(path, cfg)
	if err == nil {
		t.Fatal("expected error for unknown task_id, got nil")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Errorf("error %q should name the unknown task_id 'typo'", err.Error())
	}
}

func TestOverlayProjectPreservesStaging(t *testing.T) {
	path := writeDagJSON(t)
	cfg := &domain.LeoflowConfig{
		DagID:   "proj",
		Staging: &domain.StagingConfig{Enabled: true, Size: "5Gi"},
	}
	if err := overlayProject(path, cfg); err != nil {
		t.Fatalf("overlayProject: %v", err)
	}
	got := readSpec(t, path)
	if got.Staging == nil || !got.Staging.Enabled || got.Staging.Size != "5Gi" {
		t.Errorf("staging = %+v, want enabled 5Gi", got.Staging)
	}
}

// The overlay carries the dexaflow.yaml alerts: block onto the compiled dag.json
// (#424), so the scheduler can fire it without re-reading dexaflow.yaml.
func TestOverlayProjectCarriesAlerts(t *testing.T) {
	path := writeDagJSON(t)
	cfg := &domain.LeoflowConfig{
		DagID: "proj",
		Alerts: &domain.AlertsConfig{OnFailure: []domain.AlertRule{
			{Type: "slack", Conn: "slack_prod", Message: "{{dag}} failed"},
			{Type: "webhook", Conn: "pagerduty"},
		}},
	}
	if err := overlayProject(path, cfg); err != nil {
		t.Fatalf("overlayProject: %v", err)
	}
	got := readSpec(t, path)
	if got.Alerts == nil || len(got.Alerts.OnFailure) != 2 {
		t.Fatalf("alerts not carried: %+v", got.Alerts)
	}
	if got.Alerts.OnFailure[0].Type != "slack" || got.Alerts.OnFailure[0].Conn != "slack_prod" {
		t.Errorf("first rule = %+v", got.Alerts.OnFailure[0])
	}
}
