package main

import (
	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/observability"
)

// resourceUnit is the parsed executor.unit (ADR 0066), nil when unset. The
// config was validated at boot (ServerConfig.Validate parses the same values
// and fails startup on an error), so a parse error cannot reach here; nil is
// the safe reading of one anyway.
func resourceUnit(cfg *config.ServerConfig) *domain.ResourceUnit {
	unit, err := domain.ParseResourceUnit(cfg.Executor.Unit.ResourceUnitConfig())
	if err != nil {
		return nil
	}
	return unit
}

// defaultLimits is the configured default task limits (#537). Config
// validation already refused an invalid value at boot, so the error branch is
// unreachable on a running server; nil (no default limits) is its safe reading.
func defaultLimits(c config.PlatformDefaultsSection) *domain.DefaultLimits {
	limits, err := domain.ParseDefaultLimits(c.LimitsCPU, c.LimitsMemory)
	if err != nil {
		return nil
	}
	return limits
}

// executionPolicy is the parsed executor.defaults.execution_allowlist (issue
// #629), nil when it allows everything. ServerConfig.Validate parses the same
// values and fails boot on an error, so none reaches here; should one, the
// most restrictive reading (no field allowed) is the safe one.
func executionPolicy(c config.PlatformDefaultsSection) *domain.ExecutionPolicy {
	policy, err := domain.ParseExecutionPolicy(c.ExecutionAllowlist.ExecutionPolicyConfig())
	if err != nil {
		return domain.DenyAllExecutionPolicy()
	}
	return policy
}

// unitMisfits is the API's misfit counter, nil (not a typed nil) without
// metrics so the handler's nil check holds.
func unitMisfits(m *observability.Metrics) api.UnitMisfitRecorder {
	if m == nil {
		return nil
	}
	return m
}

// unpinnedImages is the API's count of images deploy.require_digest warned
// about or refused, nil (not a typed nil) without metrics so the handler's
// nil check holds.
func unpinnedImages(m *observability.Metrics) api.UnpinnedImageRecorder {
	if m == nil {
		return nil
	}
	return m
}

// platformDefaults maps the executor.defaults config (L0 task defaults, ADR
// 0023) into the dispatcher's PlatformDefaults. Resources are set only when a
// quantity is configured, so an unset section leaves req.Resources untouched;
// when set, both requests and limits are populated from the same quantity.
//
// That reaches Guaranteed QoS only when BOTH cpu and memory are configured.
// A partial pair yields Burstable, the empty dimension is dropped from the pod
// spec entirely, which is why platformDefaultWarnings emits a boot WARN for it
// rather than letting the gap stay invisible.
func platformDefaults(c config.PlatformDefaultsSection) dispatch.PlatformDefaults {
	d := dispatch.PlatformDefaults{
		StagingSize:         c.StagingSize,
		StagingStorageClass: c.StagingStorageClass,
		StagingAccessMode:   c.StagingAccessMode,
		PodSecurity: executor.PodSecurity{
			RunAsNonRoot:           c.RunTasksAsNonRoot,
			ReadOnlyRootFilesystem: c.ReadOnlyTaskRootFilesystem,
		},
		ExecutionPolicy: executionPolicy(c),
	}
	d.Limits = defaultLimits(c)
	if c.ResourcesCPU != "" || c.ResourcesMemory != "" {
		// Set requests AND limits to the same quantity so a task that relies on the
		// platform default reaches Guaranteed QoS (requests == limits, cpu AND
		// memory, a partial pair only reaches Burstable, and warns at boot).
		// Requests alone yields Burstable and is first to be throttled/evicted
		// under node pressure, the opposite of what pinning a per-cluster default
		// is for.
		// A DAG/task that declares its own resources still overrides this wholesale
		// (see internal/dispatch), so this only shapes the no-declaration case.
		d.Resources = &domain.Resources{
			Requests: &domain.ResourceQuantity{CPU: c.ResourcesCPU, Memory: c.ResourcesMemory},
			Limits:   &domain.ResourceQuantity{CPU: c.ResourcesCPU, Memory: c.ResourcesMemory},
		}
	}
	return d
}
