package config

import "testing"

// #507: Variables stay plaintext unless the operator turns encryption on, so a
// v0.5.4 install stores exactly what v0.5.3 reads (rolling upgrade, rollback).
func TestVariableEncryptionDefaultsOff(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.VariableEncryption != VariableEncryptionOff {
		t.Errorf("variable_encryption default = %q, want %q", c.VariableEncryption, VariableEncryptionOff)
	}
}

func TestVariableEncryptionEnvBinds(t *testing.T) {
	for _, env := range []string{"DEXAFLOW_VARIABLE_ENCRYPTION", "LEOFLOW_VARIABLE_ENCRYPTION"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "on")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if c.VariableEncryption != VariableEncryptionOn {
				t.Errorf("variable_encryption = %q, want on (from %s)", c.VariableEncryption, env)
			}
		})
	}
}

func TestValidateVariableEncryption(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		wantErr bool
	}{
		{"", false}, {VariableEncryptionOff, false}, {VariableEncryptionOn, false},
		{VariableEncryptionDecrypt, false}, {"true", true}, {"encrypt", true},
	} {
		c := &ServerConfig{VariableEncryption: tc.mode}
		if err := c.validateSecretPolicies(); (err != nil) != tc.wantErr {
			t.Errorf("variable_encryption %q: err = %v, wantErr %v", tc.mode, err, tc.wantErr)
		}
	}
}
