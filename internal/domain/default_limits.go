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

// WarmResources is what a warm pod (ADR 0058) gets from the default limits
// when no executor.unit is set: the limits of a task that declares nothing,
// with Kubernetes setting the requests to them. A warm pod is created before
// any task is known, so this is the only way the default limits reach a task
// placed on a warm worker. Nil without default limits, leaving warm pods
// unsized as before.
func (l *DefaultLimits) WarmResources() *Resources {
	if l == nil {
		return nil
	}
	r := l.Apply(Resources{})
	return &r
}

// WarmEligible reports whether a task fits a warm pod sized by WarmResources:
// in every dimension with a default limit, the limit the task would get on a
// dedicated pod (its declared limit, or the default raised to its request) is
// not above the default. A larger task takes a dedicated pod, where it keeps
// that limit, instead of running under the warm pod's smaller one. Without
// default limits every task is eligible, as before.
func (l *DefaultLimits) WarmEligible(t TaskSpec) bool {
	if l == nil || t.Resources == nil {
		return true
	}
	got := l.Apply(*t.Resources).Limits
	if got == nil {
		return true
	}
	return notAbove(got.CPU, l.cpu) && notAbove(got.Memory, l.memory)
}

// notAbove reports whether quantity v is at most bound. An empty bound (no
// default limit in that dimension) or an unparsable value never excludes.
func notAbove(v, bound string) bool {
	if bound == "" || v == "" {
		return true
	}
	qv, errV := resource.ParseQuantity(v)
	qb, errB := resource.ParseQuantity(bound)
	if errV != nil || errB != nil {
		return true
	}
	return qv.Cmp(qb) <= 0
}
