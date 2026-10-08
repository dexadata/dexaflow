package dispatch

import (
	"context"
	"testing"
)

// ADR 0067 §3: with source mode on, a version on the runtime image that carries
// a source dispatches in source mode, on a cold pod. Every other version
// dispatches as before.

const runtimeImage = "ghcr.io/dexadata/runtime@sha256:abc"

func TestDispatchSourceMode(t *testing.T) {
	cases := map[string]struct {
		mode, image, source string
		wantSourceMode      bool
	}{
		"on, runtime image with a source": {runtimeImage, runtimeImage, "print(1)\n", true},
		"on, runtime image, no source":    {runtimeImage, runtimeImage, "", false},
		"on, own image with a source":     {runtimeImage, "etl:v1", "print(1)\n", false},
		"off, runtime image with source":  {"", runtimeImage, "print(1)\n", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := &fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1", TenantID: "acme", Image: tc.image, Source: tc.source}}
			exec := &fakeExecutor{}
			placer := &fakePlacer{ok: false} // a miss, so every case reaches the executor
			d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
			d.SetWarmPlacer(placer)
			d.SetSourceModeImage(tc.mode)

			if _, err := d.Dispatch(context.Background(), "run-1", "etl", "ver-1", pythonTask()); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if exec.req.SourceMode != tc.wantSourceMode {
				t.Errorf("SourceMode = %v, want %v", exec.req.SourceMode, tc.wantSourceMode)
			}
			if exec.req.Source != tc.source {
				t.Errorf("Source = %q, want %q passed through", exec.req.Source, tc.source)
			}
			wantOffers := 1
			if tc.wantSourceMode {
				wantOffers = 0
			}
			if placer.calls != wantOffers {
				t.Errorf("warm placer offered %d times, want %d", placer.calls, wantOffers)
			}
		})
	}
}

// A warm worker that would accept is still never offered a source-mode
// attempt: its pod was built before the task was known and has no dag.py.
func TestDispatchSourceModeNeverGoesWarm(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1", Image: runtimeImage, Source: "print(1)\n"}}
	exec := &fakeExecutor{}
	placer := &fakePlacer{ok: true}
	d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
	d.SetWarmPlacer(placer)
	d.SetSourceModeImage(runtimeImage)

	if _, err := d.Dispatch(context.Background(), "run-1", "etl", "ver-1", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 0 || !exec.req.SourceMode {
		t.Errorf("placer calls = %d, SourceMode = %v; want 0 and a cold source-mode pod", placer.calls, exec.req.SourceMode)
	}
}
