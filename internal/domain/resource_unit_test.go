package domain

import (
	"strings"
	"testing"
)

// The operator resource unit (ADR 0066 §3): pool_slots x unit is the most a
// task may use, and what it gets when it declares nothing.

func mustUnit(t *testing.T) *ResourceUnit {
	t.Helper()
	u, err := ParseResourceUnit("250m", "512Mi")
	if err != nil || u == nil {
		t.Fatalf("ParseResourceUnit = %v, %v", u, err)
	}
	return u
}

func TestParseResourceUnitUnsetIsNil(t *testing.T) {
	u, err := ParseResourceUnit("", "")
	if err != nil || u != nil {
		t.Fatalf("ParseResourceUnit(\"\", \"\") = %v, %v; want nil, nil", u, err)
	}
}

func TestParseResourceUnitRejectsBadInput(t *testing.T) {
	cases := map[string][2]string{
		"cpu only":        {"250m", ""},
		"memory only":     {"", "512Mi"},
		"bad cpu":         {"quarter", "512Mi"},
		"bad memory":      {"250m", "512MB"},
		"zero cpu":        {"0", "512Mi"},
		"negative memory": {"250m", "-1Gi"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResourceUnit(in[0], in[1]); err == nil {
				t.Errorf("ParseResourceUnit(%q, %q) = nil error", in[0], in[1])
			}
		})
	}
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
	if err := mustUnit(t).CheckFits(task); err != nil {
		t.Errorf("CheckFits = %v, want nil (1 CPU / 2Gi is exactly 4 units)", err)
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
			err := mustUnit(t).CheckFits(c.task)
			if err == nil {
				t.Fatal("CheckFits = nil, want error")
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
	err := mustUnit(t).CheckSpec(spec)
	if err == nil || !strings.Contains(err.Error(), "notify") {
		t.Errorf("CheckSpec = %v, want an error naming task notify", err)
	}
	var nilUnit *ResourceUnit
	if err := nilUnit.CheckSpec(spec); err != nil {
		t.Errorf("nil unit CheckSpec = %v, want nil (no unit configured)", err)
	}
}
