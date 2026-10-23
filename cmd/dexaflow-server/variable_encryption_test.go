package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// #507: asking for encrypted (or decrypted) Variables without a usable key is a
// configuration error caught at boot, never a silent plaintext store. The
// default needs no key, exactly as before.
func TestConfigureSecretsVariableEncryptionNeedsAKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		mode    string
		wantErr bool
	}{
		{"", false}, {config.VariableEncryptionOff, false},
		{config.VariableEncryptionOn, true}, {config.VariableEncryptionDecrypt, true},
	} {
		cfg := &config.ServerConfig{VariableEncryption: tc.mode}
		_, _, err := secretOptions(cfg, logger)
		if (err != nil) != tc.wantErr {
			t.Errorf("variable_encryption %q without a key: err = %v, wantErr %v", tc.mode, err, tc.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "DEXAFLOW_SECRET_KEY") {
			t.Errorf("the error must name the missing setting: %v", err)
		}
	}
}

func TestVariableSweepOutcome(t *testing.T) {
	for _, tc := range []struct {
		name  string
		res   storage.VariableSweepResult
		err   error
		level slog.Level
		quiet bool
	}{
		{name: "nothing to do", quiet: true},
		{name: "sealed", res: storage.VariableSweepResult{Migrated: 3}, level: slog.LevelInfo},
		{name: "skipped", res: storage.VariableSweepResult{Migrated: 1, Skipped: 1}, level: slog.LevelWarn},
		{name: "unreadable", res: storage.VariableSweepResult{Unreadable: 1}, err: errors.New("x"), level: slog.LevelError},
	} {
		msg, level := variableSweepOutcome(storage.VariableEncryptionOn, tc.res, tc.err)
		if tc.quiet != (msg == "") || (!tc.quiet && level != tc.level) {
			t.Errorf("%s: %q at %v", tc.name, msg, level)
		}
	}
}
