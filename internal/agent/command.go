// Package agent contains the worker-side logic that runs inside the task
// container: building the user process command, injecting XCom inputs, reading
// the return value, and retry backoff. The gRPC client lives in cmd/dexaflow-agent.
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxRetryAttempts = 5

// NewReturnValuePath returns a unique, agent-owned path for this task's return
// value, plus a cleanup. The agent runs one task per process, so a per-process
// temp dir keeps concurrent tasks and other users from ever sharing a single
// /tmp/leoflow_return_value.json (which collided — permission denied across uids,
// clobbered across parallel tasks). The runtime is pointed here via the
// LEOFLOW_RETURN_VALUE_PATH env the runner injects.
func NewReturnValuePath() (path string, cleanup func() error, err error) {
	dir, derr := os.MkdirTemp("", "leoflow-rv-")
	if derr != nil {
		return "", nil, fmt.Errorf("creating return-value dir: %w", derr)
	}
	return filepath.Join(dir, "return_value.json"), func() error { return os.RemoveAll(dir) }, nil
}

// BuildCommand returns the argv to execute the user's task for the given
// operator. operatorClass is the dotted Airflow operator/sensor class, used only
// for airflow_operator tasks (ADR 0040); it is ignored for the other operators.
func BuildCommand(operator, entrypoint, operatorClass string) ([]string, error) {
	switch operator {
	case "python":
		module, fn, ok := strings.Cut(entrypoint, ":")
		if !ok || module == "" || fn == "" {
			return nil, fmt.Errorf("python entrypoint must be module:callable, got %q", entrypoint)
		}
		// Delegate to the leoflow_runtime helper, which runs the callable and
		// writes its return value for the agent to push as an XCom. The interpreter
		// is configurable (LEOFLOW_PYTHON) because task images standardize on
		// "python" while a dev host may only have "python3".
		// `-u` forces unbuffered stdout/stderr so every print() lands in the
		// agent's pipe immediately — critical when the process is SIGKILLed
		// (pod evict, OOM) before atexit can flush Python's block buffer.
		// PYTHONUNBUFFERED in the env (see runner.go buildEnv) covers re-execed
		// children.
		return []string{pythonInterpreter(), "-u", "-m", "leoflow_runtime", entrypoint}, nil
	case "bash":
		if entrypoint == "" {
			return nil, errors.New("bash operator requires a command")
		}
		// A templated command ({{ ds }}, {{ var.value.X }}) is rendered with the run
		// context by the runtime, then exec'd in place (#382). A plain command stays a
		// direct `bash -c` so bash-only images need no Python. `-u` mirrors the python path.
		if strings.Contains(entrypoint, "{{") {
			return []string{pythonInterpreter(), "-u", "-m", "leoflow_runtime", "--bash", entrypoint}, nil
		}
		return []string{"bash", "-c", entrypoint}, nil
	case "airflow_operator":
		// A captured provider operator/sensor (ADR 0040): the runtime imports the
		// dotted class, instantiates it with the operator args (delivered via
		// LEOFLOW_OPERATOR_ARGS in runner.go buildEnv) and calls execute(). Same
		// `-u` unbuffered rationale as python above.
		if operatorClass == "" {
			return nil, errors.New("airflow_operator task requires an operator class")
		}
		return []string{pythonInterpreter(), "-u", "-m", "leoflow_runtime", "--operator", operatorClass}, nil
	default:
		return nil, fmt.Errorf("unsupported operator %q", operator)
	}
}

// pythonInterpreter returns the Python executable the agent runs, from
// LEOFLOW_PYTHON when set, defaulting to "python" (the task-image convention).
func pythonInterpreter() string {
	if p := os.Getenv("LEOFLOW_PYTHON"); p != "" {
		return p
	}
	return "python"
}

// XComEnvVar formats an XCom input as a LEOFLOW_XCOM_<NAME>=<json> env entry.
func XComEnvVar(name string, value []byte) string {
	return "LEOFLOW_XCOM_" + strings.ToUpper(name) + "=" + string(value)
}

// ReadReturnValue reads the optional return-value file. ok is false (no error)
// when the file does not exist.
func ReadReturnValue(path string) (value []byte, ok bool, err error) {
	data, rerr := os.ReadFile(path) //nolint:gosec // path is the fixed helper output location
	if errors.Is(rerr, os.ErrNotExist) {
		return nil, false, nil
	}
	if rerr != nil {
		return nil, false, fmt.Errorf("reading return value: %w", rerr)
	}
	return data, true, nil
}

// Backoff returns the delay before retry attempt n (1-based: 1s, 2s, 4s, 8s,
// 16s). ok is false once the maximum number of attempts is exceeded.
func Backoff(attempt int) (delay time.Duration, ok bool) {
	if attempt < 1 || attempt > maxRetryAttempts {
		return 0, false
	}
	return (1 << (attempt - 1)) * time.Second, true
}
