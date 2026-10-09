package domain

import "testing"

// pool_slots (ADR 0066 §1, §2): dag.json carries it per task, dexaflow.yaml
// sets it as `size` per task and in `defaults`. Both schemas bound it to
// 1..1024 so a typo cannot ask for a pool no install has.

func TestDAGSpecValidateAcceptsPoolSlots(t *testing.T) {
	spec := validDAGSpec()
	spec.Tasks[0].PoolSlots = 4
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate() with pool_slots 4 = %v, want nil", err)
	}
}

func TestDAGSpecValidateRejectsPoolSlotsOutOfRange(t *testing.T) {
	for _, n := range []int{-1, 1025} {
		spec := validDAGSpec()
		spec.Tasks[0].PoolSlots = n
		if err := spec.Validate(); err == nil {
			t.Errorf("Validate() with pool_slots %d = nil, want error", n)
		}
	}
}

func TestLeoflowConfigValidateAcceptsSize(t *testing.T) {
	c := validLeoflowConfig()
	c.Defaults = &ConfigDefaults{Size: ptr(2)}
	c.Tasks = map[string]*TaskConfig{"extract": {Size: ptr(8)}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with size = %v, want nil", err)
	}
}

func TestLeoflowConfigValidateRejectsSizeOutOfRange(t *testing.T) {
	cases := map[string]func(*LeoflowConfig){
		"task size 0":        func(c *LeoflowConfig) { c.Tasks = map[string]*TaskConfig{"extract": {Size: ptr(0)}} },
		"task size 1025":     func(c *LeoflowConfig) { c.Tasks = map[string]*TaskConfig{"extract": {Size: ptr(1025)}} },
		"defaults size 0":    func(c *LeoflowConfig) { c.Defaults = &ConfigDefaults{Size: ptr(0)} },
		"defaults size 2000": func(c *LeoflowConfig) { c.Defaults = &ConfigDefaults{Size: ptr(2000)} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validLeoflowConfig()
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %s", name)
			}
		})
	}
}
