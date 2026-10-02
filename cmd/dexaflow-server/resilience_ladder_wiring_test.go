package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

// ceilingEnv is the env var viper's AutomaticEnv binds to
// auth.max_attempt_credential_lifetime, the one ladder rung an operator can
// move. It is the only non-hermetic input to the wired ladder's VALUES: every
// other rung is a build-time constant, and LoadServer("", nil) reads no config
// file. It is not the only environment this test touches — LoadServer itself
// still surfaces a malformed LEOFLOW_* duration — but that fails with its own
// message naming the key, which is the opposite of the misleading failure this
// unset exists to prevent. The unset is explicit rather than an empty string
// because viper only treats an empty env var as unset while AllowEmptyEnv is
// false, and this should not depend on that flag.
const ceilingEnv = "LEOFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME"

// TestResilienceLadderWiringValidates pins that the ladder the server actually
// boots with — agent heartbeat/TTL, default reaper config, reconcile interval,
// the scheduler's infra re-place ceiling and the SHIPPED default
// credential-lifetime ceiling — satisfies every ordering the restart recovery
// depends on. The ceiling is read from the config defaults rather than
// hardcoded, so a change to the shipped default that breaks the order fails
// this test too, not only a change to a build-time constant.
//
// LoadServer resolves that ceiling through viper's AutomaticEnv, so the test
// unsets ceilingEnv for its own duration to reach the config package's own
// defaults map and nothing else (#924). Without that, a developer with a short
// ceiling exported — exactly the developer most likely to be exercising this
// knob — saw this test fail complaining about the SHIPPED defaults, which are
// not what it was reading. A too-short ceiling has its own test below, where it
// is set explicitly rather than inherited from the shell.
func TestResilienceLadderWiringValidates(t *testing.T) {
	// t.Setenv registers the restore and bars t.Parallel; there is no
	// t.Unsetenv, so remove the variable by hand afterwards.
	t.Setenv(ceilingEnv, "")
	if err := os.Unsetenv(ceilingEnv); err != nil {
		t.Fatalf("unsetting %s: %v", ceilingEnv, err)
	}

	cfg, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer with shipped defaults: %v", err)
	}
	if cfg.Auth.MaxAttemptCredentialLifetime <= 0 {
		t.Fatalf("the shipped default credential ceiling must be set, got %v", cfg.Auth.MaxAttemptCredentialLifetime)
	}
	l := resilienceLadder(cfg)
	if err := executor.ValidateResilienceLadder(l); err != nil {
		t.Fatalf("server ladder %+v must validate: %v", l, err)
	}
	if l.ReconcileInterval != reconcileInterval || l.AttemptTokenTTL != attemptTokenTTL {
		t.Errorf("ladder must carry the wired values: %+v", l)
	}
	if l.InfraReplaceMaxDelay != scheduler.InfraReplaceMaxDelay() {
		t.Errorf("ladder must carry the scheduler's infra re-place ceiling %v, got %v", scheduler.InfraReplaceMaxDelay(), l.InfraReplaceMaxDelay)
	}
	if l.OrphanThreshold != executor.DefaultReaperConfig().OrphanThreshold {
		t.Errorf("ladder must carry the orphan threshold, got %v", l.OrphanThreshold)
	}
	if l.MaxAttemptCredentialLifetime != cfg.Auth.MaxAttemptCredentialLifetime {
		t.Errorf("ladder must carry the shipped default credential ceiling %v, got %v", cfg.Auth.MaxAttemptCredentialLifetime, l.MaxAttemptCredentialLifetime)
	}
}

// TestResilienceLadderWiringFailsOnShortCredentialCeiling: the ONLY
// operator-tunable rung is auth.max_attempt_credential_lifetime. Hardening it
// below the per-attempt token TTL would silently disable heartbeat renewal — and
// with it the whole restart recovery — so boot must refuse, naming the key.
func TestResilienceLadderWiringFailsOnShortCredentialCeiling(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.MaxAttemptCredentialLifetime = 5 * time.Minute
	err := executor.ValidateResilienceLadder(resilienceLadder(cfg))
	if err == nil {
		t.Fatal("a 5-minute credential ceiling under a 10-minute token TTL must fail validation")
	}
	if !strings.Contains(err.Error(), "auth.max_attempt_credential_lifetime") {
		t.Errorf("error %q must name the config key the operator has to move", err)
	}
}

// TestResilienceLadderWiringWarnsWhenCredentialCeilingDisabled: a non-positive
// auth.max_attempt_credential_lifetime passes validation (it is the documented
// "no ceiling" setting) yet silently removes two backstops — unbounded heartbeat
// renewal and no activeDeadlineSeconds floor on task pods without a declared
// execution_timeout. The boot WARN is the operator's only signal, so the boot
// path must emit exactly one WARN naming the key when the ceiling is disabled,
// and none when it is set. The record must carry the key and the offending
// value as ATTRIBUTES so a JSON log can be alerted on (#924); a pre-formatted
// message alone is only greppable by a human.
func TestResilienceLadderWiringWarnsWhenCredentialCeilingDisabled(t *testing.T) {
	warn := func(d time.Duration) string {
		var buf bytes.Buffer
		cfg := &config.ServerConfig{}
		cfg.Auth.MaxAttemptCredentialLifetime = d
		warnStartup(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
		return buf.String()
	}
	for _, d := range []time.Duration{0, -time.Minute} {
		out := warn(d)
		if strings.Count(out, "level=WARN") != 1 {
			t.Errorf("ceiling %v: want exactly one WARN, got %q", d, out)
		}
		if !strings.Contains(out, "auth.max_attempt_credential_lifetime") || !strings.Contains(out, "activeDeadlineSeconds") {
			t.Errorf("ceiling %v: WARN must name the key and the lost pod deadline floor, got %q", d, out)
		}
		if !strings.Contains(out, `config_key=auth.max_attempt_credential_lifetime`) {
			t.Errorf("ceiling %v: WARN must carry the config key as an attribute, got %q", d, out)
		}
		if !strings.Contains(out, "value="+d.String()) {
			t.Errorf("ceiling %v: WARN must carry the offending value as an attribute, got %q", d, out)
		}
		// The text handler renders a duration the same whether the attribute is
		// the time.Duration or its String(), so it cannot see the difference
		// that matters to whoever writes the alert: under the JSON handler the
		// production one, per observability.log_format's default — a duration
		// is a number of nanoseconds, while a string would be "0s". The godoc
		// on LadderWarning.Value promises the numeric shape, so pin it here or
		// a one-token change flips it invisibly.
		var jbuf bytes.Buffer
		cfgJSON := &config.ServerConfig{}
		cfgJSON.Auth.MaxAttemptCredentialLifetime = d
		warnStartup(cfgJSON, slog.New(slog.NewJSONHandler(&jbuf, nil)))
		if got := jbuf.String(); !strings.Contains(got, `"config_key":"auth.max_attempt_credential_lifetime"`) ||
			!strings.Contains(got, fmt.Sprintf(`"value":%d`, d.Nanoseconds())) {
			t.Errorf("ceiling %v: the JSON WARN must carry config_key and a numeric nanosecond value, got %s", d, got)
		}
	}
	if out := warn(24 * time.Hour); out != "" {
		t.Errorf("a set ceiling must log nothing at boot, got %q", out)
	}
	// Both guarantees live on the scheduler side (token renewal in the agent
	// gRPC server, the pod deadline floor in the dispatcher), so an api-only
	// process in a split install must not warn about behavior it does not
	// implement — a WARN operators learn to ignore is worse than none.
	var buf bytes.Buffer
	cfg := &config.ServerConfig{}
	cfg.Server.Role = "api"
	warnStartup(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
	if buf.Len() != 0 {
		t.Errorf("an api-only role must not emit the credential-ceiling WARN, got %q", buf.String())
	}
}
