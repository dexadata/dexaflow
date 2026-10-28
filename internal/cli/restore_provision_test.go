package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// freshRestoreHome turns the fixture home into what `dexaflow setup` leaves on
// a new machine: a config and setup.json, and no datastore.
func freshRestoreHome(t *testing.T, dexaHome string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dexaHome, "setup.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// #154: on a fresh install (setup run, Lite never started) restore provisions
// the managed Postgres itself, without --force, replays into it, stops what it
// started, and logs each phase in order.
func TestRestoreOnAFreshInstallProvisionsTheManagedPostgres(t *testing.T) {
	d := testDeps()
	dexaHome, archive := restoreFixture(t, d)
	freshRestoreHome(t, dexaHome)
	f := fakeRestoreDatastore(t, d, nil)
	d.psqlRestore = func(context.Context, string, []byte) error {
		f.events = append(f.events, "replay")
		return nil
	}

	cmd, out := restoreCmd(d)
	if err := runRestore(cmd, archive, false); err != nil {
		t.Fatalf("restore on a fresh install: %v\n%s", err, out.String())
	}
	if got := strings.Join(f.events, ","); got != "provision,replay,stop" {
		t.Errorf("datastore events = %s, want provision,replay,stop", got)
	}
	log := out.String()
	last := -1
	for _, phase := range []string{
		"▸ decoding manifest",
		"▸ provisioning the managed Postgres",
		"▸ applying the datastore dump",
		"▸ restoring workspace",
		"✓ restore complete; run `dexaflow lite` to start.",
	} {
		i := strings.Index(log, phase)
		if i < 0 {
			t.Errorf("phase %q not logged:\n%s", phase, log)
			continue
		}
		if i < last {
			t.Errorf("phase %q logged out of order:\n%s", phase, log)
		}
		last = i
	}
}

// A failed provisioning stops the restore before anything is replayed or
// written.
func TestRestoreStopsWhenProvisioningFails(t *testing.T) {
	d := testDeps()
	dexaHome, archive := restoreFixture(t, d)
	fakeRestoreDatastore(t, d, errors.New("no libicu"))
	before, _ := os.ReadFile(filepath.Join(dexaHome, "config.yaml"))
	replayed := false
	d.psqlRestore = func(context.Context, string, []byte) error { replayed = true; return nil }

	cmd, _ := restoreCmd(d)
	err := runRestore(cmd, archive, false)
	if err == nil || !strings.Contains(err.Error(), "no libicu") {
		t.Fatalf("restore with a failed provisioning: %v, want the provisioning error", err)
	}
	if replayed {
		t.Error("the dump was replayed without a datastore")
	}
	after, _ := os.ReadFile(filepath.Join(dexaHome, "config.yaml"))
	if !bytes.Equal(before, after) {
		t.Error("config.yaml replaced although provisioning failed")
	}
}

// The cluster a restore started is stopped when the replay fails too.
func TestRestoreStopsThePostgresItStartedWhenTheReplayFails(t *testing.T) {
	d := testDeps()
	_, archive := restoreFixture(t, d)
	f := fakeRestoreDatastore(t, d, nil)
	d.psqlRestore = func(context.Context, string, []byte) error { return errors.New("disk full") }

	cmd, _ := restoreCmd(d)
	if err := runRestore(cmd, archive, false); err == nil {
		t.Fatal("a failed replay must fail the restore")
	}
	if got := strings.Join(f.events, ","); got != "provision,stop" {
		t.Errorf("datastore events = %s, want provision,stop", got)
	}
}

// A home that holds a datastore, managed or Docker, is still refused without
// --force, before anything is provisioned.
func TestRestoreRefusesAHomeWithADatastoreWithoutForce(t *testing.T) {
	for name, rel := range map[string]string{
		"managed": filepath.Join("pgdata", "PG_VERSION"),
		"docker":  filepath.Join("dev", "db-port"),
	} {
		t.Run(name, func(t *testing.T) {
			d := testDeps()
			dexaHome, archive := restoreFixture(t, d)
			p := filepath.Join(dexaHome, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("5433\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			f := fakeRestoreDatastore(t, d, nil)
			cmd, _ := restoreCmd(d)
			err := runRestore(cmd, archive, false)
			if err == nil || !strings.Contains(err.Error(), "would overwrite an existing install") {
				t.Fatalf("restore over a %s datastore without --force: %v", name, err)
			}
			if len(f.events) != 0 {
				t.Errorf("refused restore touched the datastore: %v", f.events)
			}
		})
	}
}

// The archive holds a managed datastore. The restore pins the install to it,
// so a plain `dexaflow lite` boots the restored data on a host with Docker too
// (ADR 0094, #1997), and the closing line needs no flag.
func TestRestorePinsTheManagedDatastore(t *testing.T) {
	d := testDeps()
	dexaHome, archive := restoreFixture(t, d)
	fakeRestoreDatastore(t, d, nil)
	d.dockerUsable = func(context.Context) bool { return true }
	d.psqlRestore = func(context.Context, string, []byte) error { return nil }

	cmd, out := restoreCmd(d)
	if err := runRestore(cmd, archive, false); err != nil {
		t.Fatal(err)
	}
	if got := readDatastorePin(filepath.Join(dexaHome, "dev")); got != datastoreManaged {
		t.Errorf("datastore pin after a restore = %q, want %q", got, datastoreManaged)
	}
	if !strings.Contains(out.String(), "run `dexaflow lite` to start") || strings.Contains(out.String(), "--postgres managed") {
		t.Errorf("closing line must name a plain `dexaflow lite`:\n%s", out.String())
	}
}
