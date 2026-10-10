package domain

import (
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// A dexaflow.yaml alerts: block (native on-failure alerting, #424) unmarshals into
// the typed config and validates against the canonical schema.
func TestAlertsConfigUnmarshalsAndValidates(t *testing.T) {
	const y = `
dag_id: sales
alerts:
  on_failure:
    - type: slack
      conn: slack_prod
      message: "{{dag}} failed"
    - type: webhook
      conn: pagerduty
`
	var c LeoflowConfig
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Alerts == nil || len(c.Alerts.OnFailure) != 2 {
		t.Fatalf("alerts.on_failure not parsed: %+v", c.Alerts)
	}
	first := c.Alerts.OnFailure[0]
	if first.Type != "slack" || first.Conn != "slack_prod" || first.Message != "{{dag}} failed" {
		t.Fatalf("first rule mismatch: %+v", first)
	}
	if c.Alerts.OnFailure[1].Type != "webhook" || c.Alerts.OnFailure[1].Conn != "pagerduty" {
		t.Fatalf("second rule mismatch: %+v", c.Alerts.OnFailure[1])
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid alerts config rejected: %v", err)
	}
}

// An unknown alert type is rejected by the schema enum, not silently accepted —
// consistent with how dbt granularity and connector names fail loud.
func TestAlertsConfigRejectsUnknownType(t *testing.T) {
	c := &LeoflowConfig{
		DagID:  "sales",
		Alerts: &AlertsConfig{OnFailure: []AlertRule{{Type: "carrier-pigeon", Conn: "x"}}},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected unknown alert type to be rejected")
	}
}

// An alert rule without a connection is rejected: the endpoint and its secret must
// come from a managed connection, never a literal URL/token in dexaflow.yaml (which
// would then be rendered into dag.json). Aligns with the env-ref secret discipline.
func TestAlertsConfigRequiresConn(t *testing.T) {
	c := &LeoflowConfig{
		DagID:  "sales",
		Alerts: &AlertsConfig{OnFailure: []AlertRule{{Type: "slack"}}},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected alert rule without conn to be rejected")
	}
}
