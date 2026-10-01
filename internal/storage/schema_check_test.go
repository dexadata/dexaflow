package storage

import (
	"errors"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// checkSchemaCurrent is the pure boot-time gate: given the DB's schema state and
// the binary's embedded latest version, it fails fast with an actionable message
// rather than letting the server boot and error at the first query that touches a
// missing/old table (ADR 0060 S1). It runs even under role:api (a read-only
// SELECT), which skips migrations by design (ADR 0049).
func TestCheckSchemaCurrent(t *testing.T) {
	const latest = 26

	t.Run("current + clean = ok", func(t *testing.T) {
		if err := checkSchemaCurrent(latest, true, false, latest); err != nil {
			t.Errorf("current schema returned error: %v", err)
		}
	})

	t.Run("table missing = fail fast", func(t *testing.T) {
		err := checkSchemaCurrent(0, false, false, latest)
		if err == nil {
			t.Fatal("missing schema_migrations must fail")
		}
	})

	t.Run("behind = fail with the version gap", func(t *testing.T) {
		err := checkSchemaCurrent(latest-3, true, false, latest)
		if err == nil {
			t.Fatal("a DB behind the binary must fail")
		}
	})

	t.Run("ahead = ok (expand-contract; rollback/rollout must not crash)", func(t *testing.T) {
		// A DB ahead of the binary (older code after a helm rollback, or an old pod
		// restarting mid-rollout) must NOT hard-fail — expand-contract migrations
		// keep older code working. Hard-failing here would CrashLoopBackOff on
		// rollback. The caller logs a warning; boot proceeds.
		if err := checkSchemaCurrent(latest+1, true, false, latest); err != nil {
			t.Errorf("an ahead DB must be accepted (rollback safety), got: %v", err)
		}
	})

	t.Run("dirty = fail (a migration did not complete)", func(t *testing.T) {
		err := checkSchemaCurrent(latest, true, true, latest)
		if err == nil {
			t.Fatal("a dirty schema must fail")
		}
	})

	t.Run("errors are ErrValidation-wrapped for a clean boot message", func(t *testing.T) {
		if err := checkSchemaCurrent(0, false, false, latest); !errors.Is(err, domain.ErrSchemaNotCurrent) {
			t.Errorf("want domain.ErrSchemaNotCurrent, got %v", err)
		}
	})
}

// TestSchemaReadinessDivergesFromBootOnDirty pins the one state where the boot
// gate and the readiness probe MUST disagree, and why sharing one function
// between them was a bug rather than an economy.
//
// The migrate Job is a `pre-upgrade` Helm hook, so on `helm upgrade` it runs
// while the OLD pods are live and in the Service's endpoints. golang-migrate
// commits dirty=true for the whole execution of each migration body. A readiness
// probe that treats dirty as not-ready therefore flips EVERY old replica
// NotReady at the same instant — they all read the same row — for any migration
// longer than failureThreshold × periodSeconds (3 × 10s), dropping the Service
// to zero endpoints. In the "all" role that Service also carries gRPC, so
// running task pods lose the control plane mid-migration.
//
// The two verdicts answer different questions:
//
//   - boot: "may this process start against a half-applied schema?" — no.
//   - readiness: "is a migration in flight right now?" — a pod that was serving
//     correctly a second ago should keep serving.
//
// The discriminator is the version. dirty at a version ABOVE this binary's
// latest is a forward migration past what this binary needs, which is exactly
// the old-pods-during-upgrade case, and expand-contract keeps this binary
// working. dirty at or below its latest is a half-applied schema this binary
// actually depends on.
func TestSchemaReadinessDivergesFromBootOnDirty(t *testing.T) {
	const latest = 26

	t.Run("dirty ahead: boot refuses, readiness stays ready", func(t *testing.T) {
		// An old binary (v26) during an upgrade whose migration Job is applying
		// v27 right now. This is the case that empties the Service.
		if err := checkSchemaCurrent(latest+1, true, true, latest); err == nil {
			t.Error("boot must still refuse to start against a half-applied schema")
		}
		if err := checkSchemaReady(latest+1, true, true, latest); err != nil {
			t.Errorf("a live pod must stay ready while a forward migration is in flight, got: %v", err)
		}
	})

	t.Run("dirty at this binary's own version = not ready", func(t *testing.T) {
		// Not the upgrade case: the migration that is half-applied is the very
		// one this binary requires, so its own schema is unusable.
		if err := checkSchemaReady(latest, true, true, latest); err == nil {
			t.Error("a half-applied migration to this binary's required version must not report ready")
		}
	})

	t.Run("dirty and behind = not ready", func(t *testing.T) {
		// A migration in flight that has not yet reached what this binary needs.
		if err := checkSchemaReady(latest-1, true, true, latest); err == nil {
			t.Error("a dirty schema still behind this binary must not report ready")
		}
	})

	t.Run("absent = not ready (the #1023 state)", func(t *testing.T) {
		if err := checkSchemaReady(0, false, false, latest); err == nil {
			t.Error("an empty database must never report ready")
		}
	})

	t.Run("behind = not ready", func(t *testing.T) {
		if err := checkSchemaReady(latest-3, true, false, latest); err == nil {
			t.Error("a database behind this binary must not report ready")
		}
	})

	t.Run("clean ahead = ready (rollback must be able to converge)", func(t *testing.T) {
		if err := checkSchemaReady(latest+1, true, false, latest); err != nil {
			t.Errorf("an ahead schema must be ready, got: %v", err)
		}
	})

	t.Run("current = ready", func(t *testing.T) {
		if err := checkSchemaReady(latest, true, false, latest); err != nil {
			t.Errorf("a current schema must be ready, got: %v", err)
		}
	})

	t.Run("every not-ready verdict is the schema sentinel, not a transient error", func(t *testing.T) {
		// The probe handler branches on this to decide whether to report
		// "schema not current" or "unavailable"; a verdict that does not carry
		// the sentinel would be reported as a connection problem.
		for _, c := range []struct {
			name            string
			version, latest uint
			exists, dirty   bool
		}{
			{"absent", 0, latest, false, false},
			{"behind", latest - 3, latest, true, false},
			{"dirty at latest", latest, latest, true, true},
		} {
			err := checkSchemaReady(c.version, c.exists, c.dirty, c.latest)
			if !errors.Is(err, domain.ErrSchemaNotCurrent) {
				t.Errorf("%s: want domain.ErrSchemaNotCurrent, got %v", c.name, err)
			}
		}
	})
}

// TestDirtyAheadIsTheOnlyReadyDirtyState guards the discriminator itself: the
// readiness exemption is scoped to a forward migration past this binary, and
// must not widen into "dirty is fine".
func TestDirtyAheadIsTheOnlyReadyDirtyState(t *testing.T) {
	const latest = 26
	cases := []struct {
		name          string
		version       uint
		exists, dirty bool
		want          bool
	}{
		{"dirty ahead", latest + 1, true, true, true},
		{"dirty at latest", latest, true, true, false},
		{"dirty behind", latest - 1, true, true, false},
		{"clean ahead", latest + 1, true, false, false},
		{"absent", 0, false, true, false},
	}
	for _, c := range cases {
		if got := dirtyAhead(c.version, c.exists, c.dirty, latest); got != c.want {
			t.Errorf("%s: dirtyAhead = %v, want %v", c.name, got, c.want)
		}
	}
}
