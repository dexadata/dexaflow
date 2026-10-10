package storage

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func TestApplyDefaultRetries(t *testing.T) {
	explicit := 5
	spec := &domain.DAGSpec{
		DefaultArgs: &domain.DefaultArgs{Retries: 2},
		Tasks: []domain.TaskSpec{
			{TaskID: "a"},                     // inherits default 2
			{TaskID: "b", Retries: &explicit}, // keeps explicit 5
		},
	}
	applyDefaultRetries(spec)

	if spec.Tasks[0].Retries == nil || *spec.Tasks[0].Retries != 2 {
		t.Errorf("task a retries = %v, want 2 (from default_args)", spec.Tasks[0].Retries)
	}
	if spec.Tasks[1].Retries == nil || *spec.Tasks[1].Retries != 5 {
		t.Errorf("task b retries = %v, want 5 (explicit)", spec.Tasks[1].Retries)
	}
}

func TestApplyDefaultRetriesNoDefaults(t *testing.T) {
	spec := &domain.DAGSpec{Tasks: []domain.TaskSpec{{TaskID: "a"}}}
	applyDefaultRetries(spec)
	if spec.Tasks[0].Retries != nil {
		t.Error("with no default_args, retries should stay nil")
	}
}
