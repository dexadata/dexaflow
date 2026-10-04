package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// materializeWorkDir stages req.Source into a per-task-instance temp dir as a
// `dag.py` file, so the spawned agent's `python -m leoflow_runtime dag:<task>`
// can importlib it without depending on a globally-correct CWD. Used by the
// SubprocessExecutor (Lite). An empty source returns ("", no-op cleanup, nil)
// so the executor falls back to its configured global workdir — preserving the
// Pro/K8s path where the source lives inside the container image.
//
// The dir name embeds the task_instance_id so concurrent dispatches do not
// collide and an operator inspecting /tmp can map a leftover dir back to its
// run. Caller MUST defer cleanup() so dirs don't pile up across tasks.
func materializeWorkDir(source, taskInstanceID string) (workDir string, cleanup func(), err error) {
	noop := func() {}
	if source == "" {
		return "", noop, nil
	}
	prefix := fmt.Sprintf("leoflow-%s-", taskInstanceID)
	dir, mkErr := os.MkdirTemp("", prefix)
	if mkErr != nil {
		return "", noop, fmt.Errorf("staging task workdir: %w", mkErr)
	}
	dagPath := filepath.Join(dir, "dag.py")
	if werr := os.WriteFile(dagPath, []byte(source), 0o600); werr != nil {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			slog.Warn("cleaning up partial workdir after write error", "dir", dir, "error", rmErr)
		}
		return "", noop, fmt.Errorf("writing materialized dag.py: %w", werr)
	}
	cleanup = func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			slog.Warn("removing task workdir", "dir", dir, "error", rmErr)
		}
	}
	return dir, cleanup, nil
}

// SubprocessExecutor runs the agent as a host subprocess with no isolation. It
// is for dev mode only and logs a prominent warning on construction.
type SubprocessExecutor struct {
	agentPath string
	workDir   string
	// liteVenvsRoot, when set (Lite-only via LEOFLOW_LITE_VENVS_ROOT), is the
	// directory where each DAG owns its own venv at
	// <root>/<dag_id>/bin/python. Execute consults it per-task and overrides
	// the agent's inherited LEOFLOW_PYTHON so the right venv runs the right
	// DAG. Empty in Pro / k8s / cluster-mode Lite.
	liteVenvsRoot string
	// pidDir holds one PID record per spawned attempt (see SetPIDDir), the
	// source of AttemptProcessAlive, which the Lite reapers gate on.
	pidDir string
	// orphanGrace and orphanKillWait bound StopOrphanedTask: how long an
	// orphaned task group gets to exit after SIGTERM, then after SIGKILL.
	orphanGrace    time.Duration
	orphanKillWait time.Duration
	logger         *slog.Logger
}

// NewSubprocessExecutor builds a SubprocessExecutor running the given agent
// binary. It warns that user code runs unsandboxed. The per-DAG venv root
// is read from LEOFLOW_LITE_VENVS_ROOT at construction time so the executor
// can pick the right Python for each task without a follow-up call.
func NewSubprocessExecutor(agentPath string, logger *slog.Logger) *SubprocessExecutor {
	logger.Warn("subprocess executor active; user code runs without isolation. Do NOT use in production")
	return &SubprocessExecutor{
		agentPath:      agentPath,
		liteVenvsRoot:  os.Getenv("LEOFLOW_LITE_VENVS_ROOT"),
		pidDir:         defaultAgentPIDDir(),
		orphanGrace:    defaultOrphanGrace,
		orphanKillWait: defaultOrphanKillWait,
		logger:         logger,
	}
}

// resolveLitePythonForDag returns the per-DAG venv interpreter at
// <venvsRoot>/<dagID>/bin/python when both fields are non-empty AND the file
// exists, and "" otherwise (so the caller keeps the inherited LEOFLOW_PYTHON).
// Pulled out so it can be unit-tested without spawning a subprocess (the bug
// class is silent fallback to the wrong venv — exactly what an integration
// test would mask).
func resolveLitePythonForDag(venvsRoot, dagID string) string {
	if venvsRoot == "" || dagID == "" {
		return ""
	}
	candidate := filepath.Join(venvsRoot, dagID, "bin", "python")
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	return candidate
}

// SetWorkDir sets the working directory the agent runs in. In a task pod the
// image's WORKDIR holds the DAG code; on a dev host `dexaflow lite` points this at
// the project directory so the agent can import the user's dag.py. Empty keeps
// the parent process's working directory.
func (e *SubprocessExecutor) SetWorkDir(dir string) { e.workDir = dir }

// prependVenvBin puts a per-DAG venv's bin dir ahead of basePATH so
// venv-installed console scripts (e.g. `dbt`) resolve for bash tasks — the PATH
// analog of resolveLitePythonForDag, which only wires `python` for Python
// tasks. perDagPy is the venv interpreter (…/bin/python); an empty one returns
// basePATH unchanged. Without this a dbt model task runs bare `dbt` against the
// system PATH and fails with exit 127.
func prependVenvBin(perDagPy, basePATH string) string {
	if perDagPy == "" {
		return basePATH
	}
	bin := filepath.Dir(perDagPy)
	if basePATH == "" {
		return bin
	}
	return bin + string(os.PathListSeparator) + basePATH
}

// agentEnv builds the environment injected into the agent process.
func agentEnv(req Request) []string {
	env := make([]string, 0, 3+len(req.Env))
	env = append(env,
		"LEOFLOW_CONTROL_PLANE_ADDR="+req.ControlPlaneAddr,
		"LEOFLOW_AGENT_TOKEN="+req.AgentToken,
		"LEOFLOW_TASK_INSTANCE_ID="+req.TaskInstanceID,
	)
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// dbtScratchEnv points dbt's profiles/target/logs at a private per-task scratch
// dir, mirroring the pod base image's ENV (runtime/Dockerfile) so a Lite dbt task
// never writes profiles.yml (which carries the connection secret) into its CWD —
// the dbt project in the user's working tree, where it would clobber the repo's
// versioned profiles.yml (#882). Harmless for non-dbt tasks (they ignore it).
func dbtScratchEnv(scratch string) []string {
	return []string{
		"DBT_PROFILES_DIR=" + scratch,
		"DBT_TARGET_PATH=" + filepath.Join(scratch, "target"),
		"DBT_LOG_PATH=" + filepath.Join(scratch, "logs"),
	}
}

// Execute launches the agent subprocess and returns once it has started, like
// the Kubernetes executor creating a pod. The agent reports its own task state
// over gRPC, so the scheduler can record the task as queued before the agent
// finishes; running it synchronously here would let the agent report success
// before the scheduler recorded queued, and the queued write would clobber it.
// A non-zero exit is therefore NOT a synchronous error; only a failure to start
// is. The process is reaped in the background.
//
// A subprocess dispatch failure is always Rejected: a Lite executor talks to no
// apiserver, so its errors are never cluster backpressure — this preserves
// today's "every Lite error is permanent" behavior across the typed seam (ADR
// 0051 Phase 4). Success is Dispatched (the agent reports its terminal state
// over gRPC).
func (e *SubprocessExecutor) Execute(ctx context.Context, req Request) (Disposition, error) {
	// Materialize the DAG's source into a per-TI temp dir so `python -m
	// leoflow_runtime dag:<task>` can importlib it. Mirrors the Pro container's
	// "source is staged at the worker's CWD" contract — but without Docker.
	// Empty source falls back to the configured global workdir for backwards
	// compatibility with the scaffolded single-DAG Lite layout.
	workDir, cleanupWorkDir, err := materializeWorkDir(req.Source, req.TaskInstanceID)
	if err != nil {
		e.logger.Error("staging task workdir failed",
			"task", req.TaskID, "task_instance_id", req.TaskInstanceID, "error", err)
		return Rejected, err
	}
	if workDir == "" {
		workDir = e.workDir
	}
	// Entry log (Lima Bug 2 diagnostic, 2026-05-31): if a manual trigger sits
	// in queued forever and this line is absent, the scheduler never reached
	// Execute(); if present and the next line is absent, cmd.Start() failed.
	e.logger.Info("spawning agent subprocess",
		"task", req.TaskID, "run", req.RunID, "agent_path", e.agentPath, "work_dir", workDir,
		"materialized_source", req.Source != "")
	// WithoutCancel detaches the agent from the dispatch context: like a pod, it
	// must run to completion and report its own terminal state over gRPC. Binding
	// it to ctx would SIGKILL the agent when the dispatch ctx is canceled (surfacing
	// as "signal: killed" and a falsely failed task), mirroring the inline runner.
	// #882: give the Lite task the SAME private dbt scratch the pod base image
	// provides (runtime/Dockerfile DBT_PROFILES_DIR/TARGET/LOG), so dbt writes
	// profiles.yml/target/logs there — never the task's CWD (the dbt project in the
	// user's working tree), where the generated profiles.yml would clobber the
	// repo's versioned one with the connection secret in clear. A private per-TI
	// dir (MkdirTemp is 0700), removed with the workdir when the task exits.
	dbtScratch, err := os.MkdirTemp("", "leoflow-dbt-")
	if err != nil {
		cleanupWorkDir()
		e.logger.Error("creating dbt scratch dir failed", "task", req.TaskID, "error", err)
		return Rejected, fmt.Errorf("creating dbt scratch for task %s: %w", req.TaskID, err)
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), e.agentPath) //nolint:gosec // dev-only executor running the trusted agent binary
	cmd.Env = append(os.Environ(), agentEnv(req)...)
	cmd.Env = append(cmd.Env, dbtScratchEnv(dbtScratch)...)
	// Lite-only: when a per-DAG venv exists, override the inherited
	// LEOFLOW_PYTHON so the agent's `python -m leoflow_runtime` resolves the
	// DAG's own dependencies. Last write wins in os/exec, so appending here
	// is enough to beat any earlier server-inherited LEOFLOW_PYTHON.
	if perDagPy := resolveLitePythonForDag(e.liteVenvsRoot, req.DagID); perDagPy != "" {
		// Wire the per-DAG venv: LEOFLOW_PYTHON for `python -m ...` tasks, and the
		// venv's bin ahead of PATH so venv console scripts (dbt, etc.) resolve for
		// bash tasks. Last write wins in os/exec, so these beat the inherited env.
		cmd.Env = append(cmd.Env,
			"LEOFLOW_PYTHON="+perDagPy,
			"PATH="+prependVenvBin(perDagPy, os.Getenv("PATH")),
		)
	}
	// The agent records its task's process group next to this server's record
	// of the agent itself (#916): the task leads its own group and outlives an
	// agent killed outright, and only the agent learns the group id. The
	// directory must exist before the agent starts, since the agent may write
	// before recordPID below runs.
	if derr := e.ensurePIDDir(); derr != nil {
		cleanupWorkDir()
		_ = os.RemoveAll(dbtScratch) //nolint:errcheck // best-effort cleanup of the dbt scratch dir
		e.logger.Error("preparing agent pid dir failed", "task", req.TaskID, "error", derr)
		return Rejected, fmt.Errorf("preparing agent pid dir for task %s: %w", req.TaskID, derr)
	}
	cmd.Env = append(cmd.Env, "LEOFLOW_TASK_PGID_FILE="+e.groupPath(req.RunID, req.TaskID, req.TryNumber))
	cmd.Dir = workDir
	// Surface the agent's own diagnostics (it logs to stderr); otherwise an agent
	// that fails to start or connect fails silently. The task's stdout/stderr are
	// shipped separately over gRPC.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		cleanupWorkDir()
		_ = os.RemoveAll(dbtScratch) //nolint:errcheck // best-effort cleanup of the dbt scratch dir
		e.logger.Error("agent subprocess failed to start",
			"task", req.TaskID, "agent_path", e.agentPath, "error", err)
		return Rejected, fmt.Errorf("starting agent subprocess for task %s: %w", req.TaskID, err)
	}
	pid := cmd.Process.Pid
	e.logger.Info("agent subprocess started",
		"task", req.TaskID, "run", req.RunID, "pid", pid)
	// Record the agent's PID for its attempt so the reapers can tell a live agent
	// from a dead one, across a restart of this server too (#916, #911). An agent
	// without a record would read dead while it runs, and the dispatch-lost reaper
	// could re-place its attempt beside it, so a failed record stops the agent
	// before it reports anything and fails the dispatch instead.
	if rerr := e.recordPID(req.RunID, req.TaskID, req.TryNumber, pid); rerr != nil {
		_ = cmd.Process.Kill() //nolint:errcheck // best-effort: Wait below collects the exit either way
		_ = cmd.Wait()         //nolint:errcheck // the agent was killed on purpose; its exit status is noise
		cleanupWorkDir()
		_ = os.RemoveAll(dbtScratch) //nolint:errcheck // best-effort cleanup of the dbt scratch dir
		e.logger.Error("recording agent pid failed; agent stopped",
			"task", req.TaskID, "run", req.RunID, "pid", pid, "error", rerr)
		return Rejected, fmt.Errorf("recording agent pid for task %s: %w", req.TaskID, rerr)
	}
	go func() {
		defer e.forgetPID(req.RunID, req.TaskID, req.TryNumber, pid)
		defer cleanupWorkDir()
		defer func() { _ = os.RemoveAll(dbtScratch) }() //nolint:errcheck // best-effort cleanup of the dbt scratch dir
		werr := cmd.Wait()
		if werr != nil {
			e.logger.Warn("agent subprocess exited non-zero (the agent reports task state over gRPC)",
				"task", req.TaskID, "pid", cmd.Process.Pid, "error", werr)
			return
		}
		e.logger.Debug("agent subprocess exited cleanly",
			"task", req.TaskID, "pid", cmd.Process.Pid)
	}()
	return Dispatched, nil
}
