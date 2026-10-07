package domain

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// ResourceUnit is the operator's resource unit (executor.unit, ADR 0066 §3):
// the CPU and memory one pool slot stands for. A task of pool_slots N may use
// at most N units, and a task that declares no CPU or memory gets N units. A
// nil *ResourceUnit means no unit is configured and every method is a no-op,
// so callers need not branch on it.
type ResourceUnit struct {
	cpu    resource.Quantity
	memory resource.Quantity
}

// ParseResourceUnit parses the configured unit. Both empty means no unit (nil,
// nil). Otherwise both must be valid, positive Kubernetes quantities: a unit
// with only one dimension would leave the other unbounded, which is the
// opposite of what a unit is for.
func ParseResourceUnit(cpu, memory string) (*ResourceUnit, error) {
	if cpu == "" && memory == "" {
		return nil, nil //nolint:nilnil // no unit configured is the default, not an error; nil is the no-op unit
	}
	if cpu == "" || memory == "" {
		return nil, errors.New("executor.unit needs both cpu and memory, or neither")
	}
	c, err := resource.ParseQuantity(cpu)
	if err != nil {
		return nil, fmt.Errorf("executor.unit.cpu %q is not a Kubernetes quantity: %w", cpu, err)
	}
	m, err := resource.ParseQuantity(memory)
	if err != nil {
		return nil, fmt.Errorf("executor.unit.memory %q is not a Kubernetes quantity: %w", memory, err)
	}
	if c.Sign() <= 0 || m.Sign() <= 0 {
		return nil, fmt.Errorf("executor.unit must be positive (got cpu %q, memory %q)", cpu, memory)
	}
	return &ResourceUnit{cpu: c, memory: m}, nil
}

// times returns n units as Kubernetes quantity strings.
func (u *ResourceUnit) times(n int) (cpu, memory string) {
	c := resource.NewMilliQuantity(u.cpu.MilliValue()*int64(n), resource.DecimalSI)
	m := resource.NewQuantity(u.memory.Value()*int64(n), resource.BinarySI)
	return c.String(), m.String()
}

// Apply returns the resources a task's pod gets: its declared values, with
// every CPU and memory request or limit it left out set to pool_slots x unit,
// so every task ends up bounded by its size. The task is not modified. With no
// unit it returns the task's resources unchanged (nil when it declares none).
func (u *ResourceUnit) Apply(t TaskSpec) *Resources {
	if u == nil {
		return t.Resources
	}
	cpu, memory := u.times(t.EffectivePoolSlots())
	out := Resources{}
	if t.Resources != nil {
		out.Claims = t.Resources.Claims
		if t.Resources.Requests != nil {
			r := *t.Resources.Requests
			out.Requests = &r
		}
		if t.Resources.Limits != nil {
			l := *t.Resources.Limits
			out.Limits = &l
		}
	}
	for _, q := range []**ResourceQuantity{&out.Requests, &out.Limits} {
		if *q == nil {
			*q = &ResourceQuantity{}
		}
		if (*q).CPU == "" {
			(*q).CPU = cpu
		}
		if (*q).Memory == "" {
			(*q).Memory = memory
		}
	}
	return &out
}

// CheckFits reports an error when a task declares more CPU or memory, in any
// request or limit, than pool_slots x unit, naming the size that would fit.
// Values that do not parse are left to DAGSpec.Validate. Nil unit: always nil.
func (u *ResourceUnit) CheckFits(t TaskSpec) error {
	if u == nil || t.Resources == nil {
		return nil
	}
	slots := t.EffectivePoolSlots()
	need := slots
	for _, q := range []*ResourceQuantity{t.Resources.Requests, t.Resources.Limits} {
		if q == nil {
			continue
		}
		need = max(need, unitsFor(q.CPU, u.cpu.MilliValue(), true), unitsFor(q.Memory, u.memory.Value(), false))
	}
	if need <= slots {
		return nil
	}
	cpu, memory := u.times(1)
	return fmt.Errorf("task %q declares resources larger than its size: it is size %d, and one unit is %s CPU and %s memory, "+
		"so it needs size: %d (set it in dexaflow.yaml tasks.%s.size, or pool_slots in dag.py)",
		t.TaskID, slots, cpu, memory, need, t.TaskID)
}

// unitsFor is how many units a quantity takes, rounded up; 0 for an empty or
// unparsable value. milli reads the quantity in millis (CPU) instead of whole
// units (memory), matching how the unit was stored.
func unitsFor(value string, unit int64, milli bool) int {
	if value == "" {
		return 0
	}
	q, err := resource.ParseQuantity(value)
	if err != nil {
		return 0
	}
	v := q.Value()
	if milli {
		v = q.MilliValue()
	}
	return int((v + unit - 1) / unit)
}

// CheckSpec runs CheckFits on every task of the DAG and returns the first
// failure. Nil unit: always nil.
func (u *ResourceUnit) CheckSpec(d *DAGSpec) error {
	if u == nil {
		return nil
	}
	for _, t := range d.Tasks {
		if err := u.CheckFits(t); err != nil {
			return err
		}
	}
	return nil
}
