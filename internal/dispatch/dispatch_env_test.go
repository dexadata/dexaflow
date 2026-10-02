package dispatch

import "testing"

// stripReservedEnv must drop every LEOFLOW_-prefixed key (case-insensitively) an
// author put in dexaflow.yaml env:, so an author cannot override the agent's own
// control-plane config (#828) — e.g. LEOFLOW_CONTROL_PLANE_ADDR, LEOFLOW_AGENT_
// INSECURE, or (ADR 0060) LEOFLOW_SECRETS_BACKEND. Non-reserved keys pass through.
func TestStripReservedEnv(t *testing.T) {
	in := map[string]string{
		"MY_VAR":                     "ok",
		"LEOFLOW_CONTROL_PLANE_ADDR": "http://evil",
		"LEOFLOW_AGENT_INSECURE":     "true",
		"leoflow_secrets_backend":    "evil.Class", // lowercase must also be dropped
		"AIRFLOW_CONN_DB":            "postgres://x",
	}
	out := stripReservedEnv(in)
	if out["MY_VAR"] != "ok" || out["AIRFLOW_CONN_DB"] != "postgres://x" {
		t.Errorf("non-reserved keys must survive: %v", out)
	}
	for _, k := range []string{"LEOFLOW_CONTROL_PLANE_ADDR", "LEOFLOW_AGENT_INSECURE", "leoflow_secrets_backend"} {
		if _, present := out[k]; present {
			t.Errorf("reserved key %q must be dropped", k)
		}
	}
}

func TestStripReservedEnvNil(t *testing.T) {
	if stripReservedEnv(nil) != nil {
		t.Error("nil env must stay nil")
	}
}

// The DEXAFLOW_ prefix names the same variables since the rename, and the agent
// mirrors it onto LEOFLOW_ at startup. An author's DEXAFLOW_CONTROL_PLANE_ADDR
// would therefore reach the agent exactly like LEOFLOW_CONTROL_PLANE_ADDR, so it
// must be dropped the same way, in any case.
func TestStripReservedEnvCoversTheNewPrefix(t *testing.T) {
	in := map[string]string{
		"MY_VAR":                      "ok",
		"DEXAFLOW_CONTROL_PLANE_ADDR": "http://evil",
		"dexaflow_secrets_backend":    "evil.Class",
		"DEXAFLOWISH":                 "kept", // no underscore: not the prefix
	}
	out := stripReservedEnv(in)
	for _, k := range []string{"DEXAFLOW_CONTROL_PLANE_ADDR", "dexaflow_secrets_backend"} {
		if _, present := out[k]; present {
			t.Errorf("reserved key %q must be dropped", k)
		}
	}
	if out["MY_VAR"] != "ok" || out["DEXAFLOWISH"] != "kept" {
		t.Errorf("non-reserved keys must survive: %v", out)
	}
}
