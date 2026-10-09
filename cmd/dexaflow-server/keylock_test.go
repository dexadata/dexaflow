package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// A Lite server keeps the shared key-migration lock for its whole life. A
// session lock dies with its connection (a Docker Postgres restart, ADR 0009),
// and a migration may run in the gap, so losing it is fatal rather than
// something to reacquire (ADR 0065 section 3).
func TestWatchKeyLockCancelsOnLoss(t *testing.T) {
	var calls atomic.Int32
	holds := func(context.Context) (bool, error) {
		if calls.Add(1) >= 3 {
			return false, nil
		}
		return true, nil
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go watchKeyLock(ctx, cancel, holds, time.Millisecond)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the server kept running after losing the key-migration lock")
	}
	if !errors.Is(context.Cause(ctx), errKeyLockLost) {
		t.Errorf("cause %v, want errKeyLockLost", context.Cause(ctx))
	}
}

// A failing check is a lost lock too: the session that held it is gone.
func TestWatchKeyLockCancelsOnQueryError(t *testing.T) {
	holds := func(context.Context) (bool, error) { return false, errors.New("conn closed") }
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go watchKeyLock(ctx, cancel, holds, time.Millisecond)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a failed lock check did not stop the server")
	}
	if !errors.Is(context.Cause(ctx), errKeyLockLost) {
		t.Errorf("cause %v, want errKeyLockLost", context.Cause(ctx))
	}
}

// `dexaflow lite` refuses a server binary that does not take the lock, and it
// learns that from the version output, which every server binary answers
// without booting.
func TestVersionOutputAdvertisesTheKeyLock(t *testing.T) {
	out := versionOutput()
	if !strings.Contains(out, "\ncapabilities: ") || !strings.Contains(out, capabilityLiteKeyLock) {
		t.Errorf("version output %q does not advertise %s", out, capabilityLiteKeyLock)
	}
	if first := strings.SplitN(out, "\n", 2)[0]; len(strings.Fields(first)) < 2 {
		t.Errorf("the first line must stay \"<name> <version> ...\" for existing parsers, got %q", first)
	}
}

// The Pro boot sweep may only call a rotation complete when the pass moved
// everything: nothing skipped by the optimistic guard, nothing unreadable.
func TestRotationOutcomeOnlyClaimsCompleteOnACleanPass(t *testing.T) {
	cases := []struct {
		name     string
		res      storage.ReencryptResult
		err      error
		complete bool
	}{
		{"clean", storage.ReencryptResult{Migrated: 2}, nil, true},
		{"nothing to do", storage.ReencryptResult{}, nil, false},
		{"skipped", storage.ReencryptResult{Migrated: 2, Skipped: 1}, nil, false},
		{"unreadable", storage.ReencryptResult{Migrated: 2, Unreadable: 1}, errors.New("x"), false},
	}
	for _, c := range cases {
		msg, _ := rotationOutcome(c.res, c.err)
		if got := strings.Contains(msg, "complete"); got != c.complete {
			t.Errorf("%s: message %q, claims complete=%v, want %v", c.name, msg, got, c.complete)
		}
	}
}

// Lite switches the boot sweep off (ADR 0065 gap 2): its only migration path is
// `dexaflow lite migrate-key`. Pro keeps the ADR 0019 default.
func TestBootSweepDefaultsOnAndCanBeSwitchedOff(t *testing.T) {
	cfg, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SecretKeyReencryptOnBoot {
		t.Error("the boot sweep must stay on by default (Pro, ADR 0019)")
	}
	if cfg.SecretKeyMigrationLock {
		t.Error("the key-migration lock is a Lite setting and must be off by default")
	}
	t.Setenv("DEXAFLOW_SECRET_KEY_REENCRYPT_ON_BOOT", "false")
	t.Setenv("DEXAFLOW_SECRET_KEY_MIGRATION_LOCK", "true")
	cfg, err = config.LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SecretKeyReencryptOnBoot || !cfg.SecretKeyMigrationLock {
		t.Errorf("env did not reach the settings: reencrypt=%v lock=%v", cfg.SecretKeyReencryptOnBoot, cfg.SecretKeyMigrationLock)
	}
}
