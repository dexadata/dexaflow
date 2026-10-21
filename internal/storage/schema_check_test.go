package storage

import (
	"errors"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// schemaWaitVerdict is the boot gate of ADR 0090 decision 1: an absent or
// behind schema, or one dirty at a version other than this binary's latest, is
// a reason to wait, not to exit. Only a dirty version equal to the latest stops
// boot, because the migration this binary needs is half applied and waiting
// does not finish it.
func TestSchemaWaitVerdict(t *testing.T) {
	const latest = 26
	cases := []struct {
		name          string
		version       uint
		exists, dirty bool
		class, reason string
		ahead         bool
	}{
		{"current and clean", latest, true, false, "", "", false},
		{"clean ahead (rollback must not crash)", latest + 1, true, false, "", "", true},
		{"absent", 0, false, false, SchemaReasonAbsent, "schema absent", false},
		{"behind", latest - 3, true, false, SchemaReasonBehind, "schema behind v23, requires v26", false},
		{"dirty behind (a migration may be in flight)", latest - 1, true, true, SchemaReasonDirty, "schema dirty at v25", false},
		{"dirty ahead (a newer release is migrating)", latest + 1, true, true, SchemaReasonDirty, "schema dirty at v27", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := schemaWaitVerdict(c.version, c.exists, c.dirty, latest)
			if err != nil {
				t.Fatalf("a state to wait through stopped boot: %v", err)
			}
			if got.Class != c.class || got.Reason != c.reason || got.Ahead != c.ahead {
				t.Errorf("verdict = %+v, want class %q reason %q ahead %v", got, c.class, c.reason, c.ahead)
			}
			if got.Ready() != (c.class == "") {
				t.Errorf("Ready() = %v for class %q", got.Ready(), got.Class)
			}
		})
	}

	t.Run("dirty at this binary's latest stops boot", func(t *testing.T) {
		_, err := schemaWaitVerdict(latest, true, true, latest)
		if !errors.Is(err, domain.ErrSchemaNotCurrent) {
			t.Fatalf("want domain.ErrSchemaNotCurrent, got %v", err)
		}
		if !strings.Contains(err.Error(), "dirty at v26") {
			t.Errorf("the error does not name the dirty version: %v", err)
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

	t.Run("dirty ahead: boot waits, readiness stays ready", func(t *testing.T) {
		// An old binary (v26) during an upgrade whose migration Job is applying
		// v27 right now. This is the case that empties the Service.
		if w, err := schemaWaitVerdict(latest+1, true, true, latest); err != nil || w.Ready() {
			t.Errorf("boot must wait, not start, against a half-applied schema: %+v %v", w, err)
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

// readyClass labels the readiness verdict for dexaflow_schema_ready, so a pod
// that was serving reports why it stopped in the same words boot uses. It must
// be empty exactly when checkSchemaReady passes, dirty ahead included.
func TestReadyClassFollowsTheReadinessVerdict(t *testing.T) {
	const latest = 26
	cases := []struct {
		name          string
		version       uint
		exists, dirty bool
		want          string
	}{
		{"current", latest, true, false, ""},
		{"clean ahead", latest + 1, true, false, ""},
		{"dirty ahead stays ready", latest + 1, true, true, ""},
		{"absent", 0, false, false, SchemaReasonAbsent},
		{"behind", latest - 1, true, false, SchemaReasonBehind},
		{"dirty behind", latest - 1, true, true, SchemaReasonBehind},
		{"dirty at latest", latest, true, true, SchemaReasonDirty},
	}
	for _, c := range cases {
		if got := readyClass(c.version, c.exists, c.dirty, latest); got != c.want {
			t.Errorf("%s: readyClass = %q, want %q", c.name, got, c.want)
		}
		if (checkSchemaReady(c.version, c.exists, c.dirty, latest) == nil) != (c.want == "") {
			t.Errorf("%s: readyClass disagrees with checkSchemaReady", c.name)
		}
	}
}

// replacedReason is ADR 0090 decision 3 on the server's side: with an
// installation id configured, a database without a schema, without an id or
// with another id is not this release's own. Without one (Lite, or a chart
// install whose migration Job does not record it) nothing is checked.
func TestReplacedReason(t *testing.T) {
	cases := []struct {
		name     string
		expected string
		exists   bool
		dbID     string
		want     string
	}{
		{"no id configured, empty database", "", false, "", ""},
		{"no id configured, any id", "", true, "b", ""},
		{"same id", "a", true, "a", ""},
		{"emptied", "a", false, "", "database replaced: no schema"},
		{"a schema without an id", "a", true, "", "database replaced: no installation id"},
		{"another installation", "a", true, "b", "database replaced: another installation id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := replacedReason(c.expected, c.exists, c.dbID); got != c.want {
				t.Errorf("replacedReason = %q, want %q", got, c.want)
			}
		})
	}
}
