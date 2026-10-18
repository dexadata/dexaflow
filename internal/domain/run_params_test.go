package domain

import (
	"encoding/json"
	"testing"
)

// runParamsSpec declares DAG params: table and region with defaults, and a
// required param (no default) that contributes nothing until a run supplies it.
func runParamsSpec() DAGSpec {
	return DAGSpec{
		Params: map[string]ParamSpec{
			"table":    {Default: json.RawMessage(`"dag_table"`)},
			"region":   {Default: json.RawMessage(`"us"`)},
			"required": {Schema: json.RawMessage(`{"type":"string"}`)},
		},
	}
}

func decodeParams(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("params %q is not a JSON object: %v", raw, err)
	}
	return got
}

// TestRunParamsPrecedence pins Airflow's order with dag_run_conf_overrides_params
// on (#1841, #1844): DAG defaults, then task params, then the run conf.
func TestRunParamsPrecedence(t *testing.T) {
	task := TaskSpec{OperatorArgs: map[string]any{
		"params": map[string]any{"table": "task_table", "dataset": "raw"},
	}}
	conf := json.RawMessage(`{"dataset":"conf_dataset","extra":1}`)

	raw, err := runParamsSpec().RunParams(task, conf)
	if err != nil {
		t.Fatalf("RunParams: %v", err)
	}
	got := decodeParams(t, raw)
	want := map[string]any{
		"region":  "us",           // DAG default, nobody overrides it
		"table":   "task_table",   // task param over the DAG default
		"dataset": "conf_dataset", // conf over the task param
		"extra":   float64(1),     // conf key nobody declared
	}
	if len(got) != len(want) {
		t.Fatalf("params = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("params[%q] = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
}

// TestRunParamsDagDefaultsWithEmptyConf is the scheduled run (#1844): its conf
// is the empty object, and the DAG defaults must still reach the task.
func TestRunParamsDagDefaultsWithEmptyConf(t *testing.T) {
	for _, conf := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`null`)} {
		raw, err := runParamsSpec().RunParams(TaskSpec{}, conf)
		if err != nil {
			t.Fatalf("RunParams(conf=%q): %v", conf, err)
		}
		got := decodeParams(t, raw)
		if len(got) != 2 || got["table"] != "dag_table" || got["region"] != "us" {
			t.Errorf("conf=%q: params = %v, want the two DAG defaults", conf, got)
		}
	}
}

// TestRunParamsNothingDeclaredKeepsConf pins that a DAG and task without params
// deliver the run conf byte for byte, as before.
func TestRunParamsNothingDeclaredKeepsConf(t *testing.T) {
	for _, conf := range []json.RawMessage{nil, json.RawMessage(`{"b": 2, "a": 1}`)} {
		raw, err := DAGSpec{}.RunParams(TaskSpec{}, conf)
		if err != nil {
			t.Fatalf("RunParams: %v", err)
		}
		if string(raw) != string(conf) {
			t.Errorf("params = %q, want the conf unchanged %q", raw, conf)
		}
	}
}

// TestRunParamsIgnoresNonObjectTaskParams pins that an operator_args params
// value that is not an object is not a param layer.
func TestRunParamsIgnoresNonObjectTaskParams(t *testing.T) {
	task := TaskSpec{OperatorArgs: map[string]any{"params": "not-an-object"}}
	raw, err := DAGSpec{}.RunParams(task, json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatalf("RunParams: %v", err)
	}
	if string(raw) != `{"a":1}` {
		t.Errorf("params = %q, want the conf unchanged", raw)
	}
}

// TestRunParamsRejectsNonObjectConf pins that a conf that is not a JSON object
// is an error rather than silently dropped.
func TestRunParamsRejectsNonObjectConf(t *testing.T) {
	if _, err := runParamsSpec().RunParams(TaskSpec{}, json.RawMessage(`[1,2]`)); err == nil {
		t.Fatal("RunParams accepted a conf that is not a JSON object")
	}
}

// TestRunParamsRejectsUnencodableTaskParam pins that a task param that cannot be
// encoded is an error rather than silently dropped.
func TestRunParamsRejectsUnencodableTaskParam(t *testing.T) {
	task := TaskSpec{OperatorArgs: map[string]any{"params": map[string]any{"bad": make(chan int)}}}
	if _, err := (DAGSpec{}).RunParams(task, nil); err == nil {
		t.Fatal("RunParams accepted a task param that cannot be encoded")
	}
}

// TestRunParamsBashAndTaskFlowTaskParams covers the tasks that carry their own
// params in the task's params field, not in operator_args: a bash task and a
// @task (#1843). They layer like an operator's: over the DAG defaults, under
// the run conf.
func TestRunParamsBashAndTaskFlowTaskParams(t *testing.T) {
	task := TaskSpec{Type: TaskTypeBash, Params: map[string]any{
		"table": "task_table", "dataset": "raw",
	}}
	conf := json.RawMessage(`{"dataset":"conf_dataset"}`)

	raw, err := runParamsSpec().RunParams(task, conf)
	if err != nil {
		t.Fatalf("RunParams: %v", err)
	}
	got := decodeParams(t, raw)
	want := map[string]any{"region": "us", "table": "task_table", "dataset": "conf_dataset"}
	if len(got) != len(want) {
		t.Fatalf("params = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("params[%q] = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
}

// TestRunParamsTaskParamsWithoutDagParams: a DAG with no params still layers
// a bash task's own params under the conf.
func TestRunParamsTaskParamsWithoutDagParams(t *testing.T) {
	task := TaskSpec{Type: TaskTypePython, Params: map[string]any{"table": "orders"}}

	raw, err := DAGSpec{}.RunParams(task, nil)
	if err != nil {
		t.Fatalf("RunParams: %v", err)
	}
	if got := decodeParams(t, raw); len(got) != 1 || got["table"] != "orders" {
		t.Errorf("params = %v, want {table: orders}", got)
	}
}

// TestDAGSpecTaskParamsValidate: a compiled bash or python task carries its own
// params (#1843); the spec must validate with them and keep them round trip.
func TestDAGSpecTaskParamsValidate(t *testing.T) {
	in := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[` +
		`{"task_id":"b","type":"bash","entrypoint":"echo {{ params.table }}","params":{"table":"orders"}}]}`
	var spec DAGSpec
	if err := json.Unmarshal([]byte(in), &spec); err != nil {
		t.Fatal(err)
	}
	if got := spec.Tasks[0].Params["table"]; got != "orders" {
		t.Fatalf("decoded params = %v", spec.Tasks[0].Params)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("a task with params must validate: %v", err)
	}
}

// TestDAGSpecSchemaRejectsNonObjectTaskParams: task params are an object.
func TestDAGSpecSchemaRejectsNonObjectTaskParams(t *testing.T) {
	s, err := schemas()
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal([]byte(`{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1",`+
		`"tasks":[{"task_id":"b","type":"bash","entrypoint":"echo","params":["x"]}]}`), &doc); err != nil {
		t.Fatal(err)
	}
	if err := validateAgainst(s.dag, doc); err == nil {
		t.Fatal("params: [...] must be refused by the schema")
	}
}
