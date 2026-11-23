//go:build integration

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// TestRunDeliversA1MiBXComThroughARealExec runs a Python task through the
// production execRunner and the real leoflow_runtime with a 1 MiB upstream
// value bound to its argument and pulled with ti.xcom_pull (#2455). In the
// environment either one fails the exec with E2BIG ("Argument list too
// long"); as files the task gets both. Skips when python3 is absent.
func TestRunDeliversA1MiBXComThroughARealExec(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 not on PATH: %v", err)
	}
	_, here, _, _ := runtime.Caller(0)
	runtimeDir := filepath.Join(filepath.Dir(here), "..", "..", "runtime", "python")
	dir := t.TempDir()
	module := "def size(blob, ti=None):\n" +
		"    pulled = ti.xcom_pull(task_ids='extract')\n" +
		"    return len(blob) + len(pulled)\n"
	if err := os.WriteFile(filepath.Join(dir, "xcomfile_task.py"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	value := `"` + strings.Repeat("z", 1<<20) + `"`
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator:   "python",
			Entrypoint: "xcomfile_task:size",
			DependsOn:  []string{"extract"},
			XcomInputMapping: map[string]*agentv1.XComUpstreams{
				"blob": {TaskIds: []string{"extract"}},
			},
		},
		xcom: map[string]*agentv1.FetchXComResponse{"extract": {Value: []byte(value)}},
	}
	r := newRunner(client, nil, &recordingSink{})
	r.Cmd = NewExecRunner()
	r.Env = append(os.Environ(),
		"LEOFLOW_PYTHON="+py,
		"PYTHONPATH="+dir+string(os.PathListSeparator)+runtimeDir)
	r.ReturnPath = filepath.Join(dir, "return_value.json")
	r.XComDir = filepath.Join(dir, "xcom")

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := client.states[len(client.states)-1]; got != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Fatalf("final state %v, want success", got)
	}
	want := strconv.Itoa(2 << 20)
	for _, p := range client.pushed {
		if p.GetKey() == "return_value" {
			if string(p.GetValue()) != want {
				t.Fatalf("return value %s, want %s", p.GetValue(), want)
			}
			return
		}
	}
	t.Fatalf("no return value pushed; pushes %v", client.pushed)
}
