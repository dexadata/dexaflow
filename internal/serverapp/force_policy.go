package serverapp

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/dispatch"
)

// forcePolicy parses executor.policy (ADR 0063) and checks it against the
// rest of the configuration. No policy is nil. An error stops the boot, so a
// policy is never silently left unenforced.
func forcePolicy(cfg *config.ServerConfig) (*dispatch.ForcePolicy, error) {
	p, err := dispatch.ParseForcePolicy(cfg.Executor.Policy)
	if err != nil || p == nil {
		return p, err
	}
	// Warm pods are built by the warm pool reconciler from the version's image,
	// outside dispatch, so they would not carry what the policy forces (a
	// sandboxed runtime class, a node pool). Refuse the pair rather than run
	// warm pods the policy does not cover.
	if cfg.Execution.WarmPoolsEnabled {
		return nil, errors.New("executor.policy cannot be combined with execution.warm_pools_enabled: " +
			"warm pods are not built by dispatch and would not carry the forced fields (ADR 0063); turn one of them off")
	}
	// A forced service account obeys the allowlist like one a hook sets
	// (ADR 0100 §4), so the executor's re-check never refuses every task.
	if sa := p.ServiceAccount(); sa != "" && sa != cfg.Executor.TaskServiceAccount {
		allowed := cfg.Executor.Defaults.ExecutionAllowlist.ServiceAccounts
		if len(allowed) > 0 && !slices.Contains(allowed, sa) {
			return nil, fmt.Errorf("executor.policy force.service_account %q is not in executor.defaults.execution_allowlist.service_accounts", sa)
		}
	}
	return p, nil
}

// applyForcePolicy installs the executor policy on a pod dispatcher.
func applyForcePolicy(d *dispatch.Dispatcher, p *dispatch.ForcePolicy, logger *slog.Logger) {
	if p == nil {
		return
	}
	d.SetForcePolicy(p)
	logger.Info("executor policy installed", "forces", p.Summary())
}
