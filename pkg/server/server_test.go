package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"testing"

	"github.com/dexadata/dexaflow/pkg/xcomstore"

	"github.com/dexadata/dexaflow/pkg/logsink"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/serverapp"
	"github.com/dexadata/dexaflow/pkg/admission"
)

func TestExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil is success", nil, 0},
		{"a replaced database is 3", ErrDatabaseReplaced, 3},
		{"a wrapped replaced database is 3", fmt.Errorf("serving: %w", ErrDatabaseReplaced), 3},
		{"anything else is 1", errors.New("boom"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestRunMirrorsTheEnvironmentBeforeServing(t *testing.T) {
	t.Setenv("LEOFLOW_SERVER_TEST_ONLY_SETTING", "legacy")
	t.Setenv("DEXAFLOW_SERVER_TEST_ONLY_SETTING", "")
	if err := os.Unsetenv("DEXAFLOW_SERVER_TEST_ONLY_SETTING"); err != nil {
		t.Fatal(err)
	}
	orig := runApp
	t.Cleanup(func() { runApp = orig })
	var seen string
	var gotCfg *config.ServerConfig
	sentinel := errors.New("served")
	runApp = func(ctx context.Context, o serverapp.Options) error {
		seen = os.Getenv("DEXAFLOW_SERVER_TEST_ONLY_SETTING")
		gotCfg = o.Config
		return sentinel
	}
	if err := Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("Run() = %v, want the server's own error", err)
	}
	if seen != "legacy" {
		t.Errorf("the server saw DEXAFLOW_SERVER_TEST_ONLY_SETTING=%q, want the mirrored LEOFLOW_ value", seen)
	}
	if gotCfg != nil {
		t.Error("Run passed a configuration; without options the server loads its own, as the binary does")
	}
}

func TestWithAdmissionRegistersHooksInOrder(t *testing.T) {
	orig := runApp
	t.Cleanup(func() { runApp = orig })
	var got []admission.Hook
	runApp = func(ctx context.Context, o serverapp.Options) error {
		got = o.AdmissionHooks
		return nil
	}
	first := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		return admission.Admit(), nil
	})
	second := admission.HookFunc(func(context.Context, *admission.Request) (admission.Decision, error) {
		return admission.Refuse("second"), nil
	})
	if err := Run(context.Background(), WithAdmission(first), WithAdmission(nil), WithAdmission(second)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("the server got %d hooks, want the two non-nil ones", len(got))
	}
	if d, _ := got[1].Admit(context.Background(), &admission.Request{}); func() bool { _, r := d.Refused(); return !r }() {
		t.Error("hooks were not passed in the order they were registered")
	}
}

type nopSink struct{ name string }

func (nopSink) Open(logsink.Ref) (logsink.LogWriter, error)              { return nil, fs.ErrPermission }
func (nopSink) Read(context.Context, logsink.Ref) (io.ReadCloser, error) { return nil, fs.ErrNotExist }

func TestWithLogSinkPassesTheLastNonNilSink(t *testing.T) {
	orig := runApp
	t.Cleanup(func() { runApp = orig })
	var got any
	runApp = func(ctx context.Context, o serverapp.Options) error {
		got = o.LogSink
		return nil
	}
	if err := Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("without the option the server got sink %v; want none, so it builds the configured one", got)
	}
	want := nopSink{name: "second"}
	if err := Run(context.Background(), WithLogSink(nopSink{name: "first"}), WithLogSink(want), WithLogSink(nil)); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("the server got sink %v, want %v", got, want)
	}
}

type nopBackend struct {
	xcomstore.Backend
	name string
}

func TestWithXComBackendPassesTheLastNonNilBackend(t *testing.T) {
	orig := runApp
	t.Cleanup(func() { runApp = orig })
	var got any
	runApp = func(ctx context.Context, o serverapp.Options) error {
		got = o.XComBackend
		return nil
	}
	if err := Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("without the option the server got backend %v; want none", got)
	}
	want := nopBackend{name: "second"}
	if err := Run(context.Background(), WithXComBackend(nopBackend{name: "first"}), WithXComBackend(want), WithXComBackend(nil)); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("the server got backend %v, want %v", got, want)
	}
}
