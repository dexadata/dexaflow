package cli

import (
	"fmt"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
)

// clusterExecutionLines says, before anything is built, what the cluster
// advertises about where the DAG's tasks run (#1890): the ServiceAccount
// tasks that name none run as, and the idle warm workers kept for the
// version, resolved as the server resolves them
// (config.ExecutionSection.EffectiveMinIdle) from the project's
// min_idle_workers in dir, else the cluster's. Both are reported only: the
// server applies them, so nothing is written into dag.json. Empty when the
// cluster advertises neither, or cluster is nil.
func clusterExecutionLines(dir string, cfg *domain.LeoflowConfig, cluster *defaultsLayer) string {
	if cluster == nil {
		return ""
	}
	var b strings.Builder
	if sa := cluster.taskServiceAccount; sa != "" {
		fmt.Fprintf(&b, "task SA:    %s (from %s), for tasks that set no execution.service_account\n", sa, cluster.name)
	}
	if w := cluster.warmPools; w != nil {
		b.WriteString(warmPoolLine(dir, cfg.MinIdleWorkers, w, cluster.name))
	}
	return b.String()
}

// warmPoolLine words the warm pool a version of the project in dir gets on a
// cluster whose warm pool settings w come from source: the project's
// min_idle_workers (projectIdle) when it declares some, else the cluster's
// floor, capped at the cluster's max_pool_size; with warm pools off, none.
func warmPoolLine(dir string, projectIdle int, w *domain.WarmPoolDefaults, source string) string {
	if !w.Enabled {
		line := fmt.Sprintf("warm pool:  off (from %s): every task runs on a dedicated pod", source)
		if projectIdle > 0 {
			line += fmt.Sprintf("; min_idle_workers: %d in %s keeps no worker warm", projectIdle, projectConfigPath(dir))
		}
		return line + "\n"
	}
	idle, from := w.MinIdleWorkers, source
	if projectIdle > 0 {
		idle, from = projectIdle, projectConfigPath(dir)
	}
	if w.MaxPoolSize > 0 && idle > w.MaxPoolSize {
		idle = w.MaxPoolSize
		from += fmt.Sprintf(", capped at max_pool_size %d of %s", w.MaxPoolSize, source)
	}
	noun := "idle workers"
	if idle == 1 {
		noun = "idle worker"
	}
	return fmt.Sprintf("warm pool:  on, %d %s kept for the current version (min_idle_workers from %s)\n", idle, noun, from)
}
