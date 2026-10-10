package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestClassRefModulePathIsNeverNull is the fix for the OpenMetadata integration.
//
// OpenMetadata's REST Airflow connector validates every task through a pydantic
// model whose class_ref is typed `dict[str, str]`. leoflow declared
// classRefDTO.ModulePath as *string and never assigned it, so every task carried
// `"module_path": null`. pydantic does not coerce null into str, the model raises,
// OM catches that per DAG and files it under failures, and NO pipeline is
// ingested. Not a partial result: zero.
//
// The failure is invisible from the operator's side, which is why a field team
// reported "we cannot integrate" rather than a specific error. OM's Test
// Connection probe deliberately accepts any body from /tasks because it is
// checking reachability, so the wizard goes green while ingestion yields nothing.
//
// Airflow really does populate module_path, so this is a compatibility gap as
// well as an OM one.
func TestClassRefModulePathIsNeverNull(t *testing.T) {
	for _, tc := range []struct {
		name       string
		task       domain.TaskSpec
		wantModule string
		wantClass  string
	}{
		{
			name:       "python",
			task:       domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython},
			wantModule: "airflow.providers.standard.operators.python",
			wantClass:  "PythonOperator",
		},
		{
			name:       "bash",
			task:       domain.TaskSpec{TaskID: "t", Type: domain.TaskTypeBash},
			wantModule: "airflow.providers.standard.operators.bash",
			wantClass:  "BashOperator",
		},
		{
			// A captured provider operator already carries its dotted class, so the
			// module is the part before the final dot rather than a lookup.
			name: "captured provider operator",
			task: domain.TaskSpec{
				TaskID:        "t",
				Type:          domain.TaskTypeAirflowOperator,
				OperatorClass: "airflow.providers.postgres.operators.postgres.PostgresOperator",
			},
			wantModule: "airflow.providers.postgres.operators.postgres",
			wantClass:  "PostgresOperator",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toTaskResponse(domain.DAGSpec{DagID: "d", Tasks: []domain.TaskSpec{tc.task}}, tc.task)

			if got.ClassRef.ModulePath == nil {
				t.Fatalf("class_ref.module_path is null; OpenMetadata types it as dict[str, str] and drops the whole DAG")
			}
			if *got.ClassRef.ModulePath != tc.wantModule {
				t.Errorf("module_path = %q, want %q", *got.ClassRef.ModulePath, tc.wantModule)
			}
			if got.ClassRef.ClassName != tc.wantClass {
				t.Errorf("class_name = %q, want %q", got.ClassRef.ClassName, tc.wantClass)
			}
		})
	}
}

// TestTasksResponseCarriesNoNullInClassRef walks the served JSON rather than the
// struct, because what OM validates is the wire bytes. A field that is non-nil in
// Go but omitted or null on the wire fails there and passes here otherwise.
func TestTasksResponseCarriesNoNullInClassRef(t *testing.T) {
	rec := authGet(structureServer(&fakeSpecReader{spec: diamondSpec()}), http.MethodGet, "/api/v2/dags/etl/tasks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET tasks = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Tasks []struct {
			ClassRef map[string]any `json:"class_ref"`
		} `json:"tasks"`
		TotalEntries int `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) == 0 {
		t.Fatal("premise failed: no tasks in the response, so the assertion below would be vacuous")
	}
	for i, task := range got.Tasks {
		for _, k := range []string{"module_path", "class_name"} {
			v, ok := task.ClassRef[k]
			if !ok {
				t.Errorf("task[%d].class_ref is missing %q", i, k)
				continue
			}
			s, isStr := v.(string)
			if !isStr || strings.TrimSpace(s) == "" {
				t.Errorf("task[%d].class_ref.%s = %v; OpenMetadata requires a non-empty string here", i, k, v)
			}
		}
	}
}

// TestListDagsCarriesFileloc covers the endpoint OpenMetadata actually reads for
// the DAG list. OM's get_all_dags paginates GET /api/v2/dags and feeds each entry
// to build_dag_details, which takes pipelineLocation from `fileloc` there and
// never calls the details endpoint. Airflow's own DAGResponse declares fileloc as
// a required string, so an absent key is a compatibility gap too, not only an
// empty field in one catalog.
func TestListDagsCarriesFileloc(t *testing.T) {
	rec := authGet(authedServer(), http.MethodGet, "/api/v2/dags", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET dags = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Dags []map[string]any `json:"dags"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Dags) == 0 {
		t.Fatal("premise failed: no dags in the response, so the assertion below would be vacuous")
	}
	for i, dag := range got.Dags {
		v, ok := dag["fileloc"]
		if !ok {
			t.Errorf("dags[%d] has no fileloc; OpenMetadata reads pipelineLocation from this response", i)
			continue
		}
		if s, isStr := v.(string); !isStr || strings.TrimSpace(s) == "" {
			t.Errorf("dags[%d].fileloc = %v, want a non-empty string", i, v)
		}
	}
}
