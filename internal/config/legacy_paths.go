package config

import (
	"fmt"
	"os"
	"os/exec"
)

// Defaults that carry the product name. They named leoflow before 0.6.0
// (#2155); the Legacy constants are those older defaults, which an install
// upgraded in place may still depend on.
const (
	// DefaultDatabaseURL is database.url when nothing sets it: a local
	// development Postgres.
	DefaultDatabaseURL = "postgres://dexaflow:dexaflow@localhost:5432/dexaflow?sslmode=disable"
	// DefaultTaskNamespace is executor.task_namespace when nothing sets it.
	DefaultTaskNamespace = "dexaflow"
	// DefaultAgentPath is executor.agent_path when nothing sets it.
	DefaultAgentPath = "dexaflow-agent"
	// LegacyAgentPath is the agent binary name before 0.6.0.
	LegacyAgentPath = "leoflow-agent"
	// DefaultTaskSecretMountPath is executor.task_secret_mount_path when
	// nothing sets it.
	DefaultTaskSecretMountPath = "/etc/dexaflow/secrets"
	// LegacyTaskSecretMountPath is the task secret mount path before 0.6.0. A
	// task pod also gets the secret there while the default is in use, so a DAG
	// that reads the old path keeps working.
	LegacyTaskSecretMountPath = "/etc/leoflow/secrets"
	// DefaultLogsDir is logs.dir when nothing sets it.
	DefaultLogsDir = "/var/log/dexaflow"
	// LegacyLogsDir is the logs directory before 0.6.0.
	LegacyLogsDir = "/var/log/leoflow"
)

// ResolveLegacyPaths keeps an install upgraded in place on the paths the
// leoflow defaults gave it, when the setting was left at its default and only
// the old path exists on this host:
//
//   - logs.dir: /var/log/leoflow exists and /var/log/dexaflow does not;
//   - executor.agent_path, under the subprocess executor: leoflow-agent is on
//     PATH and dexaflow-agent is not.
//
// It returns one warning per path it kept, naming the setting to pin. Neither
// executor.task_namespace nor database.url has such a fallback: a namespace or
// a database cannot be probed safely from here, so the upgrade notes ask an
// install that relies on the old defaults to pin them.
func (c *ServerConfig) ResolveLegacyPaths() []string {
	return c.resolveLegacyPaths(dirExists, exec.LookPath)
}

func (c *ServerConfig) resolveLegacyPaths(exists func(string) bool, lookPath func(string) (string, error)) []string {
	var warnings []string
	if c.Logs.Dir == DefaultLogsDir && !exists(DefaultLogsDir) && exists(LegacyLogsDir) {
		c.Logs.Dir = LegacyLogsDir
		warnings = append(warnings, fmt.Sprintf(
			"logs.dir: %s does not exist and %s does, so logs stay in %s; set logs.dir (DEXAFLOW_LOGS_DIR) to keep this path, or move the logs",
			DefaultLogsDir, LegacyLogsDir, LegacyLogsDir))
	}
	if c.Executor.Type == "subprocess" && c.Executor.AgentPath == DefaultAgentPath {
		if _, err := lookPath(DefaultAgentPath); err != nil {
			if _, legacyErr := lookPath(LegacyAgentPath); legacyErr == nil {
				c.Executor.AgentPath = LegacyAgentPath
				warnings = append(warnings, fmt.Sprintf(
					"executor.agent_path: %s is not on PATH and %s is, so the subprocess executor runs %s; set executor.agent_path (DEXAFLOW_EXECUTOR_AGENT_PATH) to keep it, or install %s",
					DefaultAgentPath, LegacyAgentPath, LegacyAgentPath, DefaultAgentPath))
			}
		}
	}
	return warnings
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
