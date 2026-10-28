package cli

import (
	"strconv"
	"strings"
)

// executionSummary is the execution report of a register answer (#1707):
// where each task of the version runs, decided as dispatch decides it, and
// what the version's warm pool does. A server that predates it sends none.
type executionSummary struct {
	WarmPools bool            `json:"warm_pools"`
	Tasks     []taskExecution `json:"tasks"`
	Pool      executionPool   `json:"pool"`
}

// taskExecution is one task's mode, warm or dedicated, and why dedicated.
type taskExecution struct {
	TaskID string `json:"task_id"`
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
}

// executionPool is the state of the version's warm pool.
type executionPool struct {
	State          string `json:"state"`
	Detail         string `json:"detail"`
	MinIdleWorkers int    `json:"min_idle_workers,omitempty"`
	MaxPoolSize    int    `json:"max_pool_size,omitempty"`
	PreviousPool   string `json:"previous_pool,omitempty"`
}

// executionLines words e for the deploy summary: how many tasks are
// warm-eligible and which take a dedicated pod and why, then the pool. When
// every task takes a dedicated pod for one reason, that is said in one line;
// the pool line still follows when idle warm workers are kept for the
// version (an older server keeps them even when they serve none of its
// tasks, #2065) or the previous version's pool drains, and is left out
// otherwise when the pool is off or none. Nil (an older server) prints
// nothing.
func executionLines(e *executionSummary) string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	reason, one := oneDedicatedReason(e.Tasks)
	if one {
		b.WriteString("  execution  every task runs on a dedicated pod: " + reason + "\n")
		if (e.Pool.State == "off" || e.Pool.State == "none") && e.Pool.PreviousPool == "" {
			return b.String()
		}
	} else {
		b.WriteString(countLine(e.Tasks))
	}
	if e.Pool.Detail != "" {
		b.WriteString("  warm pool  " + e.Pool.Detail + "\n")
	}
	return b.String()
}

// countLine is the execution line that counts the warm-eligible tasks and
// names each dedicated one with its reason.
func countLine(tasks []taskExecution) string {
	var dedicated []taskExecution
	for _, t := range tasks {
		if t.Mode != "warm" {
			dedicated = append(dedicated, t)
		}
	}
	line := "  execution  " + countTasks(len(tasks)-len(dedicated)) + " warm-eligible"
	switch len(dedicated) {
	case 0:
	case 1:
		line += "; " + dedicated[0].TaskID + " on a dedicated pod: " + dedicated[0].Reason
	default:
		parts := make([]string, 0, len(dedicated))
		for _, t := range dedicated {
			parts = append(parts, t.TaskID+" ("+t.Reason+")")
		}
		line += "; on a dedicated pod: " + strings.Join(parts, ", ")
	}
	return line + "\n"
}

// oneDedicatedReason reports the reason when every task takes a dedicated pod
// for the same one.
func oneDedicatedReason(tasks []taskExecution) (string, bool) {
	if len(tasks) == 0 {
		return "", false
	}
	reason := tasks[0].Reason
	for _, t := range tasks {
		if t.Mode == "warm" || t.Reason != reason {
			return "", false
		}
	}
	return reason, true
}

// countTasks is "1 task" or "N tasks".
func countTasks(n int) string {
	if n == 1 {
		return "1 task"
	}
	return strconv.Itoa(n) + " tasks"
}
