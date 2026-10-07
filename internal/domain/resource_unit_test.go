package domain

import (
	"strings"
	"testing"
)

// The operator resource unit (ADR 0066 §3): pool_slots x unit is the most a
// task may use, and what it gets when it declares nothing.

func mustUnit(t *testing.T) *ResourceUnit {
	t.Helper()
	u, err := ParseResourceUnit(ResourceUnitConfig{CPU: "250m", Memory: "512Mi"})
	if err != nil || u == nil {
		t.Fatalf("ParseResourceUnit = %v, %v", u, err)
	}
	return u
}

func TestParseResourceUnitUnsetIsNil(t *testing.T) {
	u, err := ParseResourceUnit(ResourceUnitConfig{Enforce: UnitEnforceWarn, MaxSize: 8})
	if err != nil || u != nil {
		t.Fatalf("ParseResourceUnit(\"\", \"\") = %v, %v; want nil, nil", u, err)
	}
}

func TestParseResourceUnitRejectsBadInput(t *testing.T) {
	cases := map[string]ResourceUnitConfig{
		"cpu only":          {CPU: "250m"},
		"memory only":       {Memory: "512Mi"},
		"bad cpu":           {CPU: "quarter", Memory: "512Mi"},
		"bad memory":        {CPU: "250m", Memory: "512MB"},
		"zero cpu":          {CPU: "0", Memory: "512Mi"},
		"negative memory":   {CPU: "250m", Memory: "-1Gi"},
		"unknown enforce":   {CPU: "250m", Memory: "512Mi", Enforce: "audit"},
		"negative max_size": {CPU: "250m", Memory: "512Mi", MaxSize: -1},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResourceUnit(in); err == nil {
				t.Errorf("ParseResourceUnit(%+v) = nil error", in)
			}
		})
	}
}

// refusedBy is the refusal half of Check.
func refusedBy(u *ResourceUnit, t TaskSpec) error {
	_, refused := u.Check(t)
	return refused
}

func unitWith(t *testing.T, c ResourceUnitConfig) *ResourceUnit {
	t.Helper()
	c.CPU, c.Memory = "250m", "512Mi"
	u, err := ParseResourceUnit(c)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestResourceUnitApplyFillsATaskWithoutResources(t *testing.T) {
	got := mustUnit(t).Apply(TaskSpec{TaskID: "a", PoolSlots: 2})
	for name, q := range map[string]*ResourceQuantity{"requests": got.Requests, "limits": got.Limits} {
		if q == nil || q.CPU != "500m" || q.Memory != "1Gi" {
			t.Errorf("%s = %+v, want 500m / 1Gi (2 x unit)", name, q)
		}
	}
}

func TestResourceUnitApplyDefaultsToOneSlot(t *testing.T) {
	got := mustUnit(t).Apply(TaskSpec{TaskID: "a"})
	if got.Limits == nil || got.Limits.CPU != "250m" || got.Limits.Memory != "512Mi" {
		t.Errorf("limits = %+v, want 250m / 512Mi", got.Limits)
	}
}

// A task's own values are kept; only the dimensions it left out are filled,
// so every task ends up with a cpu and memory limit within its size.
func TestResourceUnitApplyKeepsDeclaredValuesAndFillsGaps(t *testing.T) {
	task := TaskSpec{TaskID: "a", PoolSlots: 2, Resources: &Resources{
		Requests: &ResourceQuantity{CPU: "100m", EphemeralStorage: "1Gi"},
		Limits:   &ResourceQuantity{Memory: "768Mi"},
		Claims:   []map[string]any{{"name": "gpu"}},
	}}
	got := mustUnit(t).Apply(task)
	if got.Requests.CPU != "100m" || got.Requests.Memory != "1Gi" || got.Requests.EphemeralStorage != "1Gi" {
		t.Errorf("requests = %+v, want declared cpu and storage kept, memory filled", got.Requests)
	}
	if got.Limits.CPU != "500m" || got.Limits.Memory != "768Mi" {
		t.Errorf("limits = %+v, want cpu filled, declared memory kept", got.Limits)
	}
	if len(got.Claims) != 1 {
		t.Errorf("claims dropped: %+v", got.Claims)
	}
	if task.Resources.Requests.Memory != "" {
		t.Errorf("Apply mutated the task's own resources: %+v", task.Resources.Requests)
	}
}

func TestResourceUnitCheckFitsAcceptsFittingResources(t *testing.T) {
	task := TaskSpec{TaskID: "a", PoolSlots: 4, Resources: &Resources{
		Requests: &ResourceQuantity{CPU: "1", Memory: "2Gi"},
		Limits:   &ResourceQuantity{CPU: "1", Memory: "2Gi"},
	}}
	if err := refusedBy(mustUnit(t), task); err != nil {
		t.Errorf("Check = %v, want nil (1 CPU / 2Gi is exactly 4 units)", err)
	}
}

func TestResourceUnitCheckFitsNamesTheSizeNeeded(t *testing.T) {
	cases := map[string]struct {
		task TaskSpec
		want string
	}{
		"cpu over": {
			TaskSpec{TaskID: "train", Resources: &Resources{Limits: &ResourceQuantity{CPU: "2"}}},
			"size: 8",
		},
		"memory over, rounded up": {
			TaskSpec{TaskID: "load", PoolSlots: 2, Resources: &Resources{Requests: &ResourceQuantity{Memory: "1100Mi"}}},
			"size: 3",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := refusedBy(mustUnit(t), c.task)
			if err == nil {
				t.Fatal("Check = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), c.task.TaskID) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should name task %q and %q", err, c.task.TaskID, c.want)
			}
		})
	}
}

func TestResourceUnitCheckSpecChecksEveryTask(t *testing.T) {
	spec := validDAGSpec()
	spec.Tasks[1].Resources = &Resources{Limits: &ResourceQuantity{CPU: "300m"}}
	_, err := mustUnit(t).CheckSpec(spec)
	if err == nil || !strings.Contains(err.Error(), "notify") {
		t.Errorf("CheckSpec = %v, want an error naming task notify", err)
	}
	var nilUnit *ResourceUnit
	if w, err := nilUnit.CheckSpec(spec); w != nil || err != nil {
		t.Errorf("nil unit CheckSpec = %v, want nil (no unit configured)", err)
	}
}

// enforce: warn lets a task that does not fit through, reported as warned so
// the caller logs and counts it (ADR 0066 §3, rollout).
func TestResourceUnitWarnToleratesAMisfit(t *testing.T) {
	u := unitWith(t, ResourceUnitConfig{Enforce: UnitEnforceWarn})
	task := TaskSpec{TaskID: "train", Resources: &Resources{Limits: &ResourceQuantity{CPU: "2"}}}
	warned, refused := u.Check(task)
	if refused != nil {
		t.Fatalf("refused = %v, want nil under warn", refused)
	}
	if warned == nil || !strings.Contains(warned.Error(), "size: 8") {
		t.Errorf("warned = %v, want the misfit naming size: 8", warned)
	}
	spec := validDAGSpec()
	spec.Tasks[0].Resources = task.Resources
	spec.Tasks[1].Resources = task.Resources
	if w, err := u.CheckSpec(spec); err != nil || len(w) != 2 {
		t.Errorf("CheckSpec = %v, %v; want both misfits warned and no refusal", w, err)
	}
}

// max_size bounds pool_slots while a unit is set, under warn too: a task that
// asks for more slots than the install allows is refused whatever its pool.
func TestResourceUnitRefusesASizeAboveMaxSize(t *testing.T) {
	cases := map[string]struct {
		cfg   ResourceUnitConfig
		slots int
		want  bool
	}{
		"default 64, size 64":   {ResourceUnitConfig{}, 64, false},
		"default 64, size 65":   {ResourceUnitConfig{}, 65, true},
		"max 8, size 9":         {ResourceUnitConfig{MaxSize: 8}, 9, true},
		"max 8, size 9, warn":   {ResourceUnitConfig{MaxSize: 8, Enforce: UnitEnforceWarn}, 9, true},
		"max 8, size 8, refuse": {ResourceUnitConfig{MaxSize: 8, Enforce: UnitEnforceRefuse}, 8, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := refusedBy(unitWith(t, c.cfg), TaskSpec{TaskID: "big", PoolSlots: c.slots})
			if (err != nil) != c.want {
				t.Fatalf("refused = %v, want refusal %v", err, c.want)
			}
			if err != nil && !strings.Contains(err.Error(), "executor.unit.max_size") {
				t.Errorf("error %q should name executor.unit.max_size", err)
			}
		})
	}
}

// A warm pod is one unit, so only a size-1 task without resources may use it;
// with no unit every task may, as before (ADR 0066 §3, warm workers).
func TestResourceUnitWarmPlacement(t *testing.T) {
	u := mustUnit(t)
	got := u.WarmResources()
	if got == nil || got.Requests.CPU != "250m" || got.Limits.Memory != "512Mi" {
		t.Errorf("WarmResources = %+v, want 1 x unit", got)
	}
	cases := map[string]struct {
		task TaskSpec
		want bool
	}{
		"size 1":         {TaskSpec{TaskID: "a"}, true},
		"size 2":         {TaskSpec{TaskID: "a", PoolSlots: 2}, false},
		"own resources":  {TaskSpec{TaskID: "a", Resources: &Resources{Limits: &ResourceQuantity{CPU: "100m"}}}, false},
		"explicit 1 set": {TaskSpec{TaskID: "a", PoolSlots: 1}, true},
	}
	for name, c := range cases {
		if got := u.WarmEligible(c.task); got != c.want {
			t.Errorf("%s: WarmEligible = %v, want %v", name, got, c.want)
		}
	}
	var none *ResourceUnit
	if none.WarmResources() != nil || !none.WarmEligible(TaskSpec{PoolSlots: 9}) {
		t.Error("with no unit warm pods stay unsized and every task stays eligible")
	}
}
