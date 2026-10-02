package envcompat

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type fakeEnv map[string]string

func (f fakeEnv) set(k, v string) error { f[k] = v; return nil }

func (f fakeEnv) environ() []string {
	out := make([]string, 0, len(f))
	for k, v := range f {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func TestMirrorNewNameReachesLegacyReaders(t *testing.T) {
	env := fakeEnv{"DEXAFLOW_SECRET_KEY": "k1", "PATH": "/bin"}
	notes := Mirror(env.environ(), env.set).Notes()
	if env["LEOFLOW_SECRET_KEY"] != "k1" {
		t.Fatalf("LEOFLOW_SECRET_KEY = %q, want the DEXAFLOW_ value", env["LEOFLOW_SECRET_KEY"])
	}
	if len(notes) != 0 {
		t.Errorf("new names alone need no note, got %v", notes)
	}
}

func TestMirrorLegacyNameReachesNewReaders(t *testing.T) {
	env := fakeEnv{"LEOFLOW_TOKEN": "t"}
	notes := Mirror(env.environ(), env.set).Notes()
	if env["DEXAFLOW_TOKEN"] != "t" {
		t.Fatalf("DEXAFLOW_TOKEN = %q, want the LEOFLOW_ value", env["DEXAFLOW_TOKEN"])
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "LEOFLOW_TOKEN") || !strings.Contains(notes[0], "DEXAFLOW_TOKEN") {
		t.Errorf("want one note naming both variables, got %v", notes)
	}
	for _, n := range notes {
		for _, word := range []string{"removed", "will stop", "deprecated in"} {
			if strings.Contains(strings.ToLower(n), word) {
				t.Errorf("note %q announces a removal; the old names stay", n)
			}
		}
	}
}

func TestMirrorNewNameWinsOnConflict(t *testing.T) {
	env := fakeEnv{"LEOFLOW_LOG_LEVEL": "debug", "DEXAFLOW_LOG_LEVEL": "warn"}
	notes := Mirror(env.environ(), env.set).Notes()
	if env["LEOFLOW_LOG_LEVEL"] != "warn" || env["DEXAFLOW_LOG_LEVEL"] != "warn" {
		t.Fatalf("got LEOFLOW=%q DEXAFLOW=%q, want both warn", env["LEOFLOW_LOG_LEVEL"], env["DEXAFLOW_LOG_LEVEL"])
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "LEOFLOW_LOG_LEVEL") {
		t.Errorf("a conflict must be reported, got %v", notes)
	}
}

func TestMirrorAgreeingPairIsSilent(t *testing.T) {
	env := fakeEnv{"LEOFLOW_X": "1", "DEXAFLOW_X": "1"}
	if notes := Mirror(env.environ(), env.set).Notes(); len(notes) != 0 {
		t.Errorf("an agreeing pair needs no note, got %v", notes)
	}
}

func TestMirrorIgnoresUnrelatedAndPrefixOnly(t *testing.T) {
	env := fakeEnv{"LEOFLOW_": "x", "DEXAFLOW_": "y", "LEOFLOWX": "z", "HOME": "/h"}
	before := env.environ()
	Mirror(env.environ(), env.set)
	if !reflect.DeepEqual(env.environ(), before) {
		t.Errorf("unrelated variables changed: %v -> %v", before, env.environ())
	}
}

func TestMirrorKeepsEmptyValues(t *testing.T) {
	// An explicitly empty value is a value: LEOFLOW_AUTH_DEV_NO_AUTH= must not be
	// ignored, or a deliberate "off" would silently fall back to a default.
	env := fakeEnv{"DEXAFLOW_AUTH_DEV_NO_AUTH": ""}
	Mirror(env.environ(), env.set)
	if v, ok := env["LEOFLOW_AUTH_DEV_NO_AUTH"]; !ok || v != "" {
		t.Errorf("empty value not mirrored: %q, %v", v, ok)
	}
}

func TestMirrorReportsWriteFailures(t *testing.T) {
	failing := func(string, string) error { return errors.New("read-only") }
	r := Mirror([]string{"DEXAFLOW_A=1"}, failing)
	if len(r.Failed) != 1 || !strings.Contains(r.Failed[0], "LEOFLOW_A") {
		t.Fatalf("a failed write must be reported, got %+v", r)
	}
}
