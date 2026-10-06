package executor

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/envcompat"
)

// TestPerDagVenvEnvWinsOverMirroredBootVenv reproduces what the agent sees in
// Lite: the server environment carries the boot venv under both names (the
// server mirrors LEOFLOW_PYTHON onto DEXAFLOW_PYTHON at startup), the executor
// appends the per-DAG override, and the agent mirrors its environment again.
// The agent must end up with the per-DAG interpreter under both names.
func TestPerDagVenvEnvWinsOverMirroredBootVenv(t *testing.T) {
	const boot = "/venvs/first/bin/python"
	const perDag = "/venvs/mine/bin/python"
	env := append([]string{
		"LEOFLOW_PYTHON=" + boot,
		"DEXAFLOW_PYTHON=" + boot,
		"PATH=/usr/bin",
	}, perDagVenvEnv(perDag, "/usr/bin")...)

	// os/exec keeps the last value of a duplicated key.
	final := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		final[k] = v
	}
	flat := make([]string, 0, len(final))
	for k, v := range final {
		flat = append(flat, k+"="+v)
	}
	envcompat.Mirror(flat, func(k, v string) error {
		final[k] = v
		return nil
	})

	for _, k := range []string{"LEOFLOW_PYTHON", "DEXAFLOW_PYTHON"} {
		if final[k] != perDag {
			t.Errorf("%s = %q, want the per-DAG venv %q", k, final[k], perDag)
		}
	}
	if !strings.HasPrefix(final["PATH"], "/venvs/mine/bin") {
		t.Errorf("PATH = %q, want the per-DAG venv bin first", final["PATH"])
	}
}
