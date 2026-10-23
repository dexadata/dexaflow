package cli

import (
	"errors"
	"strings"
	"testing"
)

// TestLitePostgresDefaultIsAuto pins the default datastore selection: `leoflow
// lite` resolves the Postgres backend for the host — the Docker postgres:16 when
// Docker is present (the realistic case, since the k3d executor needs Docker
// too), else the managed relocatable PG (Docker-free), with both forceable.
func TestLitePostgresDefaultIsAuto(t *testing.T) {
	t.Parallel()
	f := newLiteCommand().Flags().Lookup("postgres")
	if f == nil {
		// Return after t.Fatal so staticcheck (SA5011) does not flag the
		// dereference below — older bundled go/analysis in some lint
		// rebuilds did not model testing.TB.Fatal as terminating.
		t.Fatal("--postgres flag not defined")
		return
	}
	if f.DefValue != datastoreAuto {
		t.Errorf("--postgres default = %q, want %q", f.DefValue, datastoreAuto)
	}
}

// TestResolveDatastore: "auto" picks Docker when Docker is available, else the
// managed relocatable PG; an explicit value is returned unchanged.
func TestResolveDatastore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		flag     string
		dockerOK bool
		want     string
	}{
		{datastoreAuto, true, datastoreDocker},
		{datastoreAuto, false, datastoreManaged},
		{datastoreDocker, false, datastoreDocker},
		{datastoreManaged, true, datastoreManaged},
	}
	for _, c := range cases {
		if got := resolveDatastore(c.flag, c.dockerOK); got != c.want {
			t.Errorf("resolveDatastore(%q, dockerOK=%v) = %q, want %q", c.flag, c.dockerOK, got, c.want)
		}
	}
}

// TestDockerResponsive: a present `docker` binary is not enough — the daemon must
// answer. A wedged Docker Desktop (socket returns 500) makes the ping fail, which
// is what lets Lite fall back to a Docker-free Postgres instead of aborting (#403).
func TestDockerResponsive(t *testing.T) {
	t.Parallel()
	d := testDeps()
	ctx := testCtx(d)
	d.dockerPing = func() error { return nil }
	if !dockerResponsive(ctx) {
		t.Error("dockerResponsive() = false, want true when the daemon answers")
	}
	d.dockerPing = func() error { return errors.New("500 Internal Server Error") }
	if dockerResponsive(ctx) {
		t.Error("dockerResponsive() = true, want false when the daemon errors (wedged)")
	}
}

// TestDatastoreNote pins the user-facing line for the resolved Postgres backend,
// distinguishing "no Docker at all" from "Docker present but not responding" so a
// wedged daemon gets an actionable message, not the misleading "no Docker" one (#403).
func TestDatastoreNote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode    string
		host    dockerHost
		wantSub string
	}{
		{datastoreDocker, dockerReady, "Docker detected"},
		{datastoreManaged, dockerUnresponsive, "not responding"},
		{datastoreManaged, dockerNoCompose, "Compose v2 plugin is missing"},
		{datastoreManaged, dockerAbsent, "no Docker detected"},
	}
	for _, c := range cases {
		if got := datastoreNote(c.mode, c.host); !strings.Contains(got, c.wantSub) {
			t.Errorf("datastoreNote(%q, host=%v) = %q, want substring %q", c.mode, c.host, got, c.wantSub)
		}
	}
}
