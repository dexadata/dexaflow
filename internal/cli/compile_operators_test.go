package cli

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func opSpec(class string) *domain.DAGSpec {
	return &domain.DAGSpec{Tasks: []domain.TaskSpec{{
		TaskID: "q", Type: domain.TaskTypeAirflowOperator, OperatorClass: class,
	}}}
}

// TestValidateOperatorProvidersDeclaredViaDependencies: a provider named directly
// in dependencies: satisfies the check (no error).
func TestValidateOperatorProvidersDeclaredViaDependencies(t *testing.T) {
	spec := opSpec("airflow.providers.snowflake.operators.snowflake.SQLExecuteQueryOperator")
	cfg := &domain.LeoflowConfig{Dependencies: []string{"apache-airflow-providers-snowflake"}}
	if err := validateOperatorProviders(spec, cfg); err != nil {
		t.Errorf("expected no error when the provider is in dependencies, got %v", err)
	}
}

// TestValidateOperatorProvidersDeclaredViaConnectors: the connectors: sugar
// expands to the provider package, which satisfies the check.
func TestValidateOperatorProvidersDeclaredViaConnectors(t *testing.T) {
	spec := opSpec("airflow.providers.postgres.operators.postgres.SQLExecuteQueryOperator")
	cfg := &domain.LeoflowConfig{Connectors: []string{"postgres"}}
	if err := validateOperatorProviders(spec, cfg); err != nil {
		t.Errorf("expected no error when the provider is a declared connector, got %v", err)
	}
}

// TestValidateOperatorProvidersMultiSegment: a dotted provider module
// (cncf.kubernetes) declared via dependencies is matched by prefix.
func TestValidateOperatorProvidersMultiSegment(t *testing.T) {
	spec := opSpec("airflow.providers.cncf.kubernetes.operators.pod.KubernetesPodOperator")
	cfg := &domain.LeoflowConfig{Dependencies: []string{"apache-airflow-providers-cncf-kubernetes"}}
	if err := validateOperatorProviders(spec, cfg); err != nil {
		t.Errorf("expected no error for a declared dotted provider, got %v", err)
	}
}

// TestValidateOperatorProvidersBundledIsImplicit: a provider baked into the task
// base image (e.g. standard) needs no declaration.
func TestValidateOperatorProvidersBundledIsImplicit(t *testing.T) {
	spec := opSpec("airflow.providers.standard.operators.trigger_dagrun.TriggerDagRunOperator")
	if err := validateOperatorProviders(spec, &domain.LeoflowConfig{}); err != nil {
		t.Errorf("expected no error for a base-bundled provider, got %v", err)
	}
}

// TestValidateOperatorProvidersUndeclaredIsLoudReject: an operator whose provider
// is declared nowhere fails compile, naming the task and the line to add.
func TestValidateOperatorProvidersUndeclaredIsLoudReject(t *testing.T) {
	spec := opSpec("airflow.providers.snowflake.operators.snowflake.SQLExecuteQueryOperator")
	err := validateOperatorProviders(spec, &domain.LeoflowConfig{})
	if err == nil {
		t.Fatal("expected a compile error for an undeclared provider, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "q") || !strings.Contains(msg, "snowflake") {
		t.Errorf("error must name the task and the provider, got %q", msg)
	}
}
