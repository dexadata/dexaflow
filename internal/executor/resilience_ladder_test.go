package executor

import (
	"strings"
	"testing"
	"time"
)

// defaultLadder is the production ladder as the server wires it: the agent's
// heartbeat interval and token TTL, the default reaper thresholds/graces, the
// reconciler's sweep interval, the scheduler's longest infra re-place delay and
// the default credential-lifetime ceiling.
func defaultLadder() ResilienceLadder {
	cfg := DefaultReaperConfig()
	return ResilienceLadder{
		HeartbeatInterval:            15 * time.Second,
		AgentLostThreshold:           cfg.AgentLostThreshold,
		SettlingGrace:                cfg.SettlingGrace,
		AttemptTokenTTL:              10 * time.Minute,
		ReconcileInterval:            30 * time.Second,
		OrphanThreshold:              cfg.OrphanThreshold,
		InfraReplaceMaxDelay:         190 * time.Second,
		MaxAttemptCredentialLifetime: 24 * time.Hour,
		TaskTerminationGrace:         30 * time.Second,
		InfraConfirmMaxWait:          2 * time.Minute,
	}
}

// TestValidateResilienceLadderDefaultsPass: the shipped defaults satisfy every
// ordering the control-plane-restart recovery depends on.
func TestValidateResilienceLadderDefaultsPass(t *testing.T) {
	if err := ValidateResilienceLadder(defaultLadder()); err != nil {
		t.Fatalf("default ladder must validate, got %v", err)
	}
}

// TestValidateResilienceLadderDisabledCredentialCeilingPasses: a non-positive
// credential lifetime is the operator's documented "no ceiling" setting, under
// which a token renews for as long as the attempt lives — so the TTL-below-
// ceiling rung is trivially satisfied and must not be reported as a violation.
func TestValidateResilienceLadderDisabledCredentialCeilingPasses(t *testing.T) {
	l := defaultLadder()
	l.MaxAttemptCredentialLifetime = 0
	if err := ValidateResilienceLadder(l); err != nil {
		t.Fatalf("a disabled credential ceiling must validate, got %v", err)
	}
}

// TestValidateResilienceLadderRequiresTwoSweepsUnderGrace: at least TWO whole
// maintenance cycles (reconcile sweep, then reap) must fit under the settling
// grace. A maintenance interval just under the grace (179s vs 180s) satisfies a
// plain "interval < grace" ordering yet guarantees zero completed cycles in the
// window, so the validator must reject it.
func TestValidateResilienceLadderRequiresTwoSweepsUnderGrace(t *testing.T) {
	l := defaultLadder()
	l.ReconcileInterval = 179 * time.Second
	l.SettlingGrace = 180 * time.Second
	l.AttemptTokenTTL = 10 * time.Minute
	err := ValidateResilienceLadder(l)
	if err == nil {
		t.Fatal("maintenance interval=179s under grace=180s must be rejected")
	}
	for _, w := range []string{"maintenance interval", "settling grace", "build-time invariant violated"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q must name %q", err, w)
		}
	}
}

// TestValidateResilienceLadderRejectsEachViolation: each single inverted (or
// equal — the relations are strict) rung fails, and the error names BOTH sides
// of the violated relation so an operator can tell which knob moved. Rungs made
// of build-time constants tell the operator to file a bug; the one operator-
// tunable rung names its config key instead.
func TestValidateResilienceLadderRejectsEachViolation(t *testing.T) {
	const bug = "build-time invariant violated"
	const key = "auth.max_attempt_credential_lifetime"
	cases := []struct {
		name    string
		mutate  func(*ResilienceLadder)
		want    []string // substrings the error must carry
		notWant []string // substrings the error must NOT carry
	}{
		{
			name:    "heartbeat interval not below agent-lost threshold",
			mutate:  func(l *ResilienceLadder) { l.HeartbeatInterval = l.AgentLostThreshold },
			want:    []string{"heartbeat interval", "agent-lost threshold", bug},
			notWant: []string{key},
		},
		{
			name:    "agent-lost threshold not below settling grace",
			mutate:  func(l *ResilienceLadder) { l.SettlingGrace = l.AgentLostThreshold },
			want:    []string{"agent-lost threshold", "settling grace", bug},
			notWant: []string{key},
		},
		{
			name:    "settling grace not below attempt token TTL",
			mutate:  func(l *ResilienceLadder) { l.AttemptTokenTTL = l.SettlingGrace },
			want:    []string{"settling grace", "attempt token TTL", bug},
			notWant: []string{key},
		},
		{
			name:    "two maintenance intervals not below settling grace",
			mutate:  func(l *ResilienceLadder) { l.ReconcileInterval = l.SettlingGrace / 2 },
			want:    []string{"maintenance interval", "settling grace", bug},
			notWant: []string{key},
		},
		{
			name:    "infra re-place delay not below orphan threshold",
			mutate:  func(l *ResilienceLadder) { l.InfraReplaceMaxDelay = l.OrphanThreshold },
			want:    []string{"infra re-place", "orphan threshold", bug},
			notWant: []string{key},
		},
		{
			name:    "attempt token TTL not below the credential lifetime ceiling",
			mutate:  func(l *ResilienceLadder) { l.MaxAttemptCredentialLifetime = 5 * time.Minute },
			want:    []string{"attempt token TTL", key},
			notWant: []string{bug, "file a bug"},
		},
		{
			name:   "a non-positive rung",
			mutate: func(l *ResilienceLadder) { l.HeartbeatInterval = 0 },
			want:   []string{"heartbeat interval", "positive"},
		},
		{
			name:   "a non-positive orphan threshold",
			mutate: func(l *ResilienceLadder) { l.OrphanThreshold = 0 },
			want:   []string{"orphan threshold", "positive"},
		},
		{
			name:   "a non-positive infra re-place delay",
			mutate: func(l *ResilienceLadder) { l.InfraReplaceMaxDelay = -time.Second },
			want:   []string{"infra re-place", "positive"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := defaultLadder()
			tc.mutate(&l)
			err := ValidateResilienceLadder(l)
			if err == nil {
				t.Fatalf("ladder %+v must be rejected", l)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q must name %q", err, w)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(err.Error(), nw) {
					t.Errorf("error %q must not carry %q", err, nw)
				}
			}
		})
	}
}

// TestResilienceLadderWarningsDisabledCredentialCeiling: a non-positive
// credential ceiling is a documented setting the validator tolerates, but it
// silently removes TWO backstops at once — heartbeat renewal becomes unbounded
// and a task pod with no declared execution timeout gets no ActiveDeadlineSeconds
// floor. The operator's only signal is the boot WARN, so the warnings must name
// the key and both consequences for zero and for negative values, and must be
// empty when the ceiling is set. The key and the offending value are carried as
// FIELDS, not only inside the prose: a pre-formatted string logs a JSON record
// with nothing to alert on (#924).
func TestResilienceLadderWarningsDisabledCredentialCeiling(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		l := defaultLadder()
		l.MaxAttemptCredentialLifetime = d
		got := ResilienceLadderWarnings(l)
		if len(got) != 1 {
			t.Fatalf("ceiling %v: want exactly one warning, got %+v", d, got)
		}
		w := got[0]
		if w.Key != maxAttemptCredentialLifetimeKey {
			t.Errorf("ceiling %v: warning key = %q, want %q — an alert rule matches on the field, not the prose", d, w.Key, maxAttemptCredentialLifetimeKey)
		}
		if w.Value != d {
			t.Errorf("ceiling %v: warning value = %v, want the offending value", d, w.Value)
		}
		for _, want := range []string{"auth.max_attempt_credential_lifetime", "disabled", "renewal", "activeDeadlineSeconds", "watchdog", "wedged"} {
			if !strings.Contains(w.Msg, want) {
				t.Errorf("ceiling %v: warning %q must mention %q", d, w.Msg, want)
			}
		}
	}
	if got := ResilienceLadderWarnings(defaultLadder()); len(got) != 0 {
		t.Errorf("a set ceiling must produce no warning, got %+v", got)
	}
}

// TestValidateResilienceLadderInfraConfirmValve: the confirmation valve (ADR
// 0052 amendment) must outlast a stopped pod's termination grace plus two
// reconcile cycles, or the planner re-places before the reconciler could read
// the record; and it must stay below the orphan threshold, or a run whose only
// live task is a provisional mark is reaped as orphaned.
func TestValidateResilienceLadderInfraConfirmValve(t *testing.T) {
	short := defaultLadder()
	short.InfraConfirmMaxWait = short.TaskTerminationGrace + 2*short.ReconcileInterval
	err := ValidateResilienceLadder(short)
	if err == nil || !strings.Contains(err.Error(), "infra confirmation valve") {
		t.Fatalf("a valve at termination grace + 2 intervals must be rejected, got %v", err)
	}
	long := defaultLadder()
	long.InfraConfirmMaxWait = long.OrphanThreshold
	err = ValidateResilienceLadder(long)
	if err == nil || !strings.Contains(err.Error(), "orphan threshold") {
		t.Fatalf("a valve at the orphan threshold must be rejected, got %v", err)
	}
	zero := defaultLadder()
	zero.TaskTerminationGrace = 0
	if err := ValidateResilienceLadder(zero); err == nil {
		t.Fatal("a zero task termination grace must be rejected")
	}
}
