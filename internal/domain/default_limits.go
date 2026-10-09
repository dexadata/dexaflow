package domain

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// DefaultLimits is the operator's default task limit (executor.defaults.limits_cpu
// and limits_memory, #537): the CPU and memory limit a task pod gets in a
// dimension where its resources set none. It closes the gap the L0 resource
// default (ADR 0023) leaves open: that default applies only to a task that
// declares no resources at all, so a task declaring requests alone, or any task
// on a cluster without the default, runs with no limit. A nil *DefaultLimits
// means none is configured and Apply is a no-op.
type DefaultLimits struct {
	cpu    string
	memory string
}

// ParseDefaultLimits parses the configured default limits. Both empty means
// none (nil, nil). Either may be set alone; a set value must be a valid,
// positive Kubernetes quantity.
func ParseDefaultLimits(cpu, memory string) (*DefaultLimits, error) {
	if cpu == "" && memory == "" {
		return nil, nil //nolint:nilnil // no default limits is the shipped default, not an error; nil is the no-op value
	}
	for key, value := range map[string]string{"executor.defaults.limits_cpu": cpu, "executor.defaults.limits_memory": memory} {
		if value == "" {
			continue
		}
		q, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("%s %q is not a Kubernetes quantity: %w", key, value, err)
		}
		if q.Sign() <= 0 {
			return nil, fmt.Errorf("%s %q must be positive", key, value)
		}
	}
	return &DefaultLimits{cpu: cpu, memory: memory}, nil
}

// Apply returns r with every empty CPU or memory limit filled from the
// defaults. A declared limit is never changed, and requests are left as they
// are: where a request is empty Kubernetes sets it to the limit, as it does for
// a LimitRange default. A filled limit is raised to the request of the same
// dimension when that is larger, because the API server rejects a request above
// its limit. r is not modified.
func (l *DefaultLimits) Apply(r Resources) Resources {
	if l == nil {
		return r
	}
	out := r
	limits := ResourceQuantity{}
	if r.Limits != nil {
		limits = *r.Limits
	}
	requests := ResourceQuantity{}
	if r.Requests != nil {
		requests = *r.Requests
	}
	if limits.CPU == "" && l.cpu != "" {
		limits.CPU = larger(l.cpu, requests.CPU)
	}
	if limits.Memory == "" && l.memory != "" {
		limits.Memory = larger(l.memory, requests.Memory)
	}
	if limits != (ResourceQuantity{}) {
		out.Limits = &limits
	}
	return out
}
