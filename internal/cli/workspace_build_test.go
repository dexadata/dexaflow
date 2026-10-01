package cli

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func regCfg(url, name string) *domain.LeoflowConfig {
	c := &domain.LeoflowConfig{DagID: "d"}
	c.ApplyDefaults()
	c.Registry.URL = url
	c.Registry.ImageName = name
	return c
}

// TestBuildTargets covers the decision half of #1115: which projects in a
// workspace can be built, and with what image reference.
//
// Building a workspace is only useful if it is per-project — each project
// declares its own `registry:`, and deployImageRef already derives a reference
// from it, so nothing new is invented here. What the decision has to handle is
// the project that declares no registry: it cannot be built, and silently
// skipping it would mean a "build everything" that quietly built less than
// everything.
func TestBuildTargets(t *testing.T) {
	t.Run("a project with a registry gets a derived image", func(t *testing.T) {
		got, skipped := buildTargets([]Project{
			{Path: "/ws/sales", DagID: "sales", Config: regCfg("reg.io/team", "sales")},
		}, "v1", "abc1234")
		if len(got) != 1 {
			t.Fatalf("targets = %+v", got)
		}
		// The WHOLE reference, not substrings of it: asserting only that the url
		// and name appear is satisfied by `reg.io/team/sales:` — an empty tag,
		// which docker refuses — and that is exactly the defect that reached the
		// builder while this test stayed green.
		if want := "reg.io/team/sales:v1"; got[0].image != want { // tag_strategy defaults to "version"
			t.Errorf("image = %q, want %q", got[0].image, want)
		}
		if len(skipped) != 0 {
			t.Errorf("nothing to skip: %v", skipped)
		}
	})

	t.Run("a project without a registry is reported, not silently dropped", func(t *testing.T) {
		// "build everything" that builds less than everything, without saying
		// so, is the failure this reporting exists to prevent.
		got, skipped := buildTargets([]Project{
			{Path: "/ws/a", DagID: "a", Config: regCfg("reg.io/team", "a")},
			{Path: "/ws/b", DagID: "b", Config: regCfg("", "")},
		}, "v1", "abc1234")
		if len(got) != 1 || got[0].dagID != "a" {
			t.Errorf("targets = %+v, want only a", got)
		}
		if len(skipped) != 1 || !strings.Contains(skipped[0], "b") {
			t.Fatalf("skipped = %v, want it to name b", skipped)
		}
		if !strings.Contains(skipped[0], "registry") {
			t.Errorf("the skip must say what is missing; got %q", skipped[0])
		}
	})

	t.Run("a project with no config at all is reported", func(t *testing.T) {
		_, skipped := buildTargets([]Project{{Path: "/ws/c", DagID: "c"}}, "v1", "abc1234")
		if len(skipped) != 1 || !strings.Contains(skipped[0], "c") {
			t.Fatalf("skipped = %v, want it to name c", skipped)
		}
	})

	t.Run("targets are ordered so output is stable", func(t *testing.T) {
		got, _ := buildTargets([]Project{
			{Path: "/ws/z", DagID: "z", Config: regCfg("r", "z")},
			{Path: "/ws/a", DagID: "a", Config: regCfg("r", "a")},
		}, "v1", "abc1234")
		if len(got) != 2 || got[0].dagID != "a" {
			t.Errorf("targets = %+v, want a before z", got)
		}
	})

	t.Run("an empty workspace yields nothing and no complaint", func(t *testing.T) {
		got, skipped := buildTargets(nil, "v1", "abc1234")
		if len(got) != 0 || len(skipped) != 0 {
			t.Errorf("got %v / %v", got, skipped)
		}
	})
}
