package admission_test

import (
	"testing"

	"github.com/dexadata/dexaflow/pkg/admission"
)

func TestDecisions(t *testing.T) {
	var zero admission.Decision
	if _, refused := zero.Refused(); refused || !zero.Changes().IsZero() {
		t.Error("the zero Decision should admit unchanged")
	}
	if _, refused := admission.Admit().Refused(); refused {
		t.Error("Admit refused")
	}
	d := admission.AdmitWith(admission.Changes{PriorityClassName: "low"})
	if _, refused := d.Refused(); refused || d.Changes().PriorityClassName != "low" {
		t.Errorf("AdmitWith = %+v", d)
	}
	if reason, refused := admission.Refuse("over budget").Refused(); !refused || reason != "over budget" {
		t.Errorf("Refuse = %q, %v", reason, refused)
	}
	h := admission.Hold("freeze")
	if reason, held := h.Held(); !held || reason != "freeze" {
		t.Errorf("Hold = %q, %v", reason, held)
	}
	if _, refused := h.Refused(); refused {
		t.Error("Hold refused")
	}
}

func TestIsReservedKey(t *testing.T) {
	for key, want := range map[string]bool{
		"leoflow.io/task-id":     true,
		"dexaflow.io/anything":   true,
		"team":                   false,
		"example.com/leoflow.io": false,
	} {
		if got := admission.IsReservedKey(key); got != want {
			t.Errorf("IsReservedKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestReservedPrefixesIsACopy(t *testing.T) {
	p := admission.ReservedPrefixes()
	p[0] = "x/"
	if !admission.IsReservedKey("leoflow.io/a") {
		t.Error("changing the returned slice changed the reserved prefixes")
	}
}
