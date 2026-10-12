package main

import "testing"

// TestRunControlFromEnv: the usual boolean spellings turn run control on or
// off under either variable name; anything else is refused rather than
// silently leaving it off.
func TestRunControlFromEnv(t *testing.T) {
	cases := []struct {
		env     map[string]string
		want    bool
		wantErr bool
	}{
		{map[string]string{}, false, false},
		{map[string]string{"DEXAFLOW_MCP_RUN_CONTROL": "true"}, true, false},
		{map[string]string{"DEXAFLOW_MCP_RUN_CONTROL": "1"}, true, false},
		{map[string]string{"DEXAFLOW_MCP_RUN_CONTROL": "TRUE"}, true, false},
		{map[string]string{"DEXAFLOW_MCP_RUN_CONTROL": "false"}, false, false},
		{map[string]string{"DEXAFLOW_MCP_RUN_CONTROL": "0"}, false, false},
		{map[string]string{"LEOFLOW_MCP_RUN_CONTROL": "true"}, true, false},
		{map[string]string{"LEOFLOW_MCP_RUN_CONTROL": "t"}, true, false},
		{map[string]string{"DEXAFLOW_MCP_RUN_CONTROL": "false", "LEOFLOW_MCP_RUN_CONTROL": "true"}, false, false},
		{map[string]string{"DEXAFLOW_MCP_RUN_CONTROL": "yes"}, false, true},
		{map[string]string{"LEOFLOW_MCP_RUN_CONTROL": "on"}, false, true},
	}
	for _, tc := range cases {
		got, err := runControlFromEnv(func(k string) string { return tc.env[k] })
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("runControlFromEnv(%v) = %v, %v; want %v, error %v", tc.env, got, err, tc.want, tc.wantErr)
		}
	}
}
