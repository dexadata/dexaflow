package domain

import (
	"reflect"
	"testing"
)

func TestParseDefaultLimits(t *testing.T) {
	t.Run("both empty means no default limits", func(t *testing.T) {
		l, err := ParseDefaultLimits("", "")
		if err != nil || l != nil {
			t.Fatalf("ParseDefaultLimits(\"\", \"\") = %v, %v; want nil, nil", l, err)
		}
	})
	t.Run("one dimension alone is allowed", func(t *testing.T) {
		l, err := ParseDefaultLimits("", "1Gi")
		if err != nil || l == nil {
			t.Fatalf("ParseDefaultLimits(\"\", \"1Gi\") = %v, %v; want limits, nil", l, err)
		}
	})
	for name, c := range map[string][2]string{
		"unparsable cpu":    {"lots", ""},
		"unparsable memory": {"", "1 gig"},
		"zero cpu":          {"0", ""},
		"negative memory":   {"", "-1Gi"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDefaultLimits(c[0], c[1]); err == nil {
				t.Errorf("ParseDefaultLimits(%q, %q) accepted an invalid quantity", c[0], c[1])
			}
		})
	}
}

func TestDefaultLimitsApply(t *testing.T) {
	limits, err := ParseDefaultLimits("2", "4Gi")
	if err != nil {
		t.Fatal(err)
	}
	claims := []map[string]any{{"name": "gpu"}}
	cases := []struct {
		name string
		in   Resources
		want Resources
	}{
		{
			name: "no resources: limits only, requests left for Kubernetes to default",
			in:   Resources{},
			want: Resources{Limits: &ResourceQuantity{CPU: "2", Memory: "4Gi"}},
		},
		{
			name: "requests only: limits filled, requests kept",
			in:   Resources{Requests: &ResourceQuantity{CPU: "500m", Memory: "1Gi", EphemeralStorage: "1Gi"}},
			want: Resources{
				Requests: &ResourceQuantity{CPU: "500m", Memory: "1Gi", EphemeralStorage: "1Gi"},
				Limits:   &ResourceQuantity{CPU: "2", Memory: "4Gi"},
			},
		},
		{
			name: "a declared limit is never changed",
			in:   Resources{Limits: &ResourceQuantity{CPU: "8"}},
			want: Resources{Limits: &ResourceQuantity{CPU: "8", Memory: "4Gi"}},
		},
		{
			name: "a request above the default raises the filled limit to the request",
			in:   Resources{Requests: &ResourceQuantity{CPU: "4", Memory: "8Gi"}},
			want: Resources{
				Requests: &ResourceQuantity{CPU: "4", Memory: "8Gi"},
				Limits:   &ResourceQuantity{CPU: "4", Memory: "8Gi"},
			},
		},
		{
			name: "claims are carried over",
			in:   Resources{Claims: claims},
			want: Resources{Limits: &ResourceQuantity{CPU: "2", Memory: "4Gi"}, Claims: claims},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := limits.Apply(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Apply(%+v) = %+v (limits %+v); want %+v (limits %+v)", c.in, got, got.Limits, c.want, c.want.Limits)
			}
		})
	}
}

// The task's resources come from the artifact, which is cached and shared by
// every attempt, so Apply must return a copy rather than write through.
func TestDefaultLimitsApplyDoesNotMutateInput(t *testing.T) {
	limits, err := ParseDefaultLimits("1", "1Gi")
	if err != nil {
		t.Fatal(err)
	}
	in := Resources{Limits: &ResourceQuantity{CPU: "3"}}
	_ = limits.Apply(in)
	if in.Limits.Memory != "" {
		t.Errorf("Apply wrote through to the input: %+v", in.Limits)
	}
}

func TestDefaultLimitsOnlyCPU(t *testing.T) {
	limits, err := ParseDefaultLimits("1", "")
	if err != nil {
		t.Fatal(err)
	}
	got := limits.Apply(Resources{})
	want := Resources{Limits: &ResourceQuantity{CPU: "1"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Apply = %+v; want %+v", got.Limits, want.Limits)
	}
}

func TestNilDefaultLimitsIsANoOp(t *testing.T) {
	var limits *DefaultLimits
	in := Resources{Requests: &ResourceQuantity{CPU: "1"}}
	if got := limits.Apply(in); !reflect.DeepEqual(got, in) {
		t.Errorf("nil Apply = %+v; want input unchanged", got)
	}
}

// A warm pod (ADR 0058) is created before any task is known, so without a unit
// it is sized as a task that declares nothing: the default limits, with
// Kubernetes setting the requests to them. Nil without default limits, leaving
// warm pods unsized as before.
func TestDefaultLimitsWarmResources(t *testing.T) {
	var none *DefaultLimits
	if got := none.WarmResources(); got != nil {
		t.Errorf("nil WarmResources = %+v; want nil", got)
	}
	limits, err := ParseDefaultLimits("2", "4Gi")
	if err != nil {
		t.Fatal(err)
	}
	got := limits.WarmResources()
	want := &Resources{Limits: &ResourceQuantity{CPU: "2", Memory: "4Gi"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WarmResources = %+v; want %+v", got, want)
	}
}

// A task whose own resources would get a larger limit than the warm pod's (a
// declared limit or a request above the default) does not fit a warm pod sized
// by the default limits, so it takes a dedicated pod where it keeps that limit.
func TestDefaultLimitsWarmEligible(t *testing.T) {
	limits, err := ParseDefaultLimits("2", "4Gi")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		res  *Resources
		want bool
	}{
		"no resources":                {nil, true},
		"requests within the default": {&Resources{Requests: &ResourceQuantity{CPU: "500m", Memory: "1Gi"}}, true},
		"limit equal to the default":  {&Resources{Limits: &ResourceQuantity{Memory: "4Gi"}}, true},
		"request above the default":   {&Resources{Requests: &ResourceQuantity{Memory: "8Gi"}}, false},
		"limit above the default":     {&Resources{Limits: &ResourceQuantity{CPU: "4"}}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := limits.WarmEligible(TaskSpec{TaskID: "t", Resources: c.res}); got != c.want {
				t.Errorf("WarmEligible = %v; want %v", got, c.want)
			}
		})
	}
	var none *DefaultLimits
	if !none.WarmEligible(TaskSpec{TaskID: "t", Resources: &Resources{Limits: &ResourceQuantity{CPU: "64"}}}) {
		t.Error("nil default limits must leave every task warm-eligible, as before")
	}
	cpuOnly, err := ParseDefaultLimits("2", "")
	if err != nil {
		t.Fatal(err)
	}
	if !cpuOnly.WarmEligible(TaskSpec{TaskID: "t", Resources: &Resources{Requests: &ResourceQuantity{Memory: "64Gi"}}}) {
		t.Error("a dimension without a default limit must not make a task warm-ineligible")
	}
}
