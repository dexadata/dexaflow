package domain

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Enforcement modes of executor.unit.enforce (ADR 0066 §3).
const (
	// UnitEnforceRefuse refuses a task that does not fit its size: registration
	// fails and a DAG registered earlier fails at dispatch. The default.
	UnitEnforceRefuse = "refuse"
	// UnitEnforceWarn accepts such a task and runs it with its own resources,
	// logging and counting it, so an operator can roll a unit out without
	// failing tasks first.
	UnitEnforceWarn = "warn"
)

// DefaultUnitMaxSize is the largest pool_slots a task may have while a unit is
// set and executor.unit.max_size is not (ADR 0066 §3).
const DefaultUnitMaxSize = 64

// ResourceUnitConfig is the executor.unit section as configured.
type ResourceUnitConfig struct {
	CPU     string
	Memory  string
	Enforce string // "" means UnitEnforceRefuse
	MaxSize int    // 0 means DefaultUnitMaxSize
}

// ResourceUnit is the operator's resource unit (executor.unit, ADR 0066 §3):
// the CPU and memory one pool slot stands for. A task of pool_slots N may use
// at most N units, and a task that declares no CPU or memory gets N units. A
// nil *ResourceUnit means no unit is configured and every method is a no-op,
// so callers need not branch on it.
type ResourceUnit struct {
	cpu     resource.Quantity
	memory  resource.Quantity
	warn    bool
	maxSize int
}

// ParseResourceUnit parses the configured unit. CPU and memory both empty
// means no unit (nil, nil), whatever enforce and max_size say. Otherwise both
// must be valid, positive Kubernetes quantities: a unit with only one
// dimension would leave the other unbounded, which is the opposite of what a
// unit is for.
func ParseResourceUnit(c ResourceUnitConfig) (*ResourceUnit, error) {
	if c.CPU == "" && c.Memory == "" {
		return nil, nil //nolint:nilnil // no unit configured is the default, not an error; nil is the no-op unit
	}
	if c.CPU == "" || c.Memory == "" {
		return nil, errors.New("executor.unit needs both cpu and memory, or neither")
	}
	cpu, err := resource.ParseQuantity(c.CPU)
	if err != nil {
		return nil, fmt.Errorf("executor.unit.cpu %q is not a Kubernetes quantity: %w", c.CPU, err)
	}
	memory, err := resource.ParseQuantity(c.Memory)
	if err != nil {
		return nil, fmt.Errorf("executor.unit.memory %q is not a Kubernetes quantity: %w", c.Memory, err)
	}
	if cpu.Sign() <= 0 || memory.Sign() <= 0 {
		return nil, fmt.Errorf("executor.unit must be positive (got cpu %q, memory %q)", c.CPU, c.Memory)
	}
	u := &ResourceUnit{cpu: cpu, memory: memory, maxSize: c.MaxSize}
	switch c.Enforce {
	case "", UnitEnforceRefuse:
	case UnitEnforceWarn:
		u.warn = true
	default:
		return nil, fmt.Errorf("executor.unit.enforce %q must be %q or %q", c.Enforce, UnitEnforceRefuse, UnitEnforceWarn)
	}
	switch {
	case c.MaxSize == 0:
		u.maxSize = DefaultUnitMaxSize
	case c.MaxSize < 1:
		return nil, fmt.Errorf("executor.unit.max_size %d must be positive", c.MaxSize)
	}
	return u, nil
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

// UnitMisfitError is a task that does not fit the unit: it declares more than
// pool_slots x unit, or its pool_slots is above executor.unit.max_size.
type UnitMisfitError struct {
	TaskID  string
	Size    int // the task's pool_slots
	Need    int // the size its resources need; 0 when over max_size
	MaxSize int // set when the task is over executor.unit.max_size
	unitCPU string
	unitMem string
}

func (e *UnitMisfitError) Error() string {
	if e.MaxSize > 0 {
		return fmt.Sprintf("task %q is size %d, above this install's largest size %d (executor.unit.max_size)",
			e.TaskID, e.Size, e.MaxSize)
	}
	return fmt.Sprintf("task %q declares resources larger than its size: it is size %d, and one unit is %s CPU and %s memory, "+
		"so it needs size: %d (set it in dexaflow.yaml tasks.%s.size, or pool_slots in dag.py)",
		e.TaskID, e.Size, e.unitCPU, e.unitMem, e.Need, e.TaskID)
}

// misfit returns the task's misfit, or nil when it fits. Values that do not
// parse are left to DAGSpec.Validate.
func (u *ResourceUnit) misfit(t TaskSpec) *UnitMisfitError {
	slots := t.EffectivePoolSlots()
	if slots > u.maxSize {
		return &UnitMisfitError{TaskID: t.TaskID, Size: slots, MaxSize: u.maxSize}
	}
	if t.Resources == nil {
		return nil
	}
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
	return &UnitMisfitError{TaskID: t.TaskID, Size: slots, Need: need, unitCPU: cpu, unitMem: memory}
}

// Check decides a task against the unit. refused is a misfit that must stop
// it (registration fails, dispatch refuses). warned is a misfit tolerated under
// enforce: warn, which the caller logs and counts and then lets through. A task
// over max_size is always refused: pool_slots is new with the unit, so no task
// that ran before can trip it. Nil unit: both nil.
func (u *ResourceUnit) Check(t TaskSpec) (warned, refused error) {
	if u == nil {
		return nil, nil
	}
	m := u.misfit(t)
	switch {
	case m == nil:
		return nil, nil
	case u.warn && m.MaxSize == 0:
		return m, nil
	}
	return nil, m
}

// CheckSpec runs Check on every task of the DAG: refused is the first refusal,
// warned every tolerated misfit. Nil unit: both nil.
func (u *ResourceUnit) CheckSpec(d *DAGSpec) (warned []error, refused error) {
	if u == nil {
		return nil, nil
	}
	for _, t := range d.Tasks {
		w, r := u.Check(t)
		if r != nil {
			return warned, r
		}
		if w != nil {
			warned = append(warned, w)
		}
	}
	return warned, nil
}

// WarmResources is what a warm pod (ADR 0058) gets while a unit is set: one
// unit as requests and limits. Nil without a unit, leaving warm pods as before.
func (u *ResourceUnit) WarmResources() *Resources {
	if u == nil {
		return nil
	}
	cpu, memory := u.times(1)
	return &Resources{
		Requests: &ResourceQuantity{CPU: cpu, Memory: memory},
		Limits:   &ResourceQuantity{CPU: cpu, Memory: memory},
	}
}

// WarmEligible reports whether a task may be placed on a warm pod. A warm pod
// is one unit, so with a unit set only a size-1 task that declares no
// resources fits on it; any other task takes a dedicated pod. Without a unit
// every task is eligible, as before.
func (u *ResourceUnit) WarmEligible(t TaskSpec) bool {
	return u == nil || (t.EffectivePoolSlots() == 1 && t.Resources == nil)
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
