package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
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

// With execution.source_mode.warm_pools off (the default), a warm worker that
// would accept is still never offered a source-mode attempt (ADR 0067 §3).
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

// With execution.source_mode.warm_pools on (ADR 0095), a source-mode attempt
// is offered to a warm worker with its dag.py and the SHA-256 the worker
// checks it against; on a miss it still takes a cold source-mode pod.
func TestDispatchSourceModeGoesWarmWithItsSource(t *testing.T) {
	const src = "print(1)\n"
	for _, free := range []bool{true, false} {
		res := &fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1", Image: runtimeImage, Source: src}}
		exec := &fakeExecutor{}
		placer := &fakePlacer{ok: free}
		d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
		d.SetWarmPlacer(placer)
		d.SetSourceModeImage(runtimeImage)
		d.SetSourceModeWarm(true)

		if _, err := d.Dispatch(context.Background(), "run-1", "etl", "ver-1", pythonTask()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if placer.calls != 1 {
			t.Fatalf("free=%v: placer calls = %d, want 1", free, placer.calls)
		}
		if got := placer.last; string(got.GetDagSource()) != src ||
			got.GetDagSourceSha256() != sha256Hex(src) {
			t.Errorf("free=%v: assignment source %q (sha %q), want %q with its SHA-256", free, got.GetDagSource(), got.GetDagSourceSha256(), src)
		}
		if gotPod := exec.req.TaskID != ""; gotPod == free || (gotPod && !exec.req.SourceMode) {
			t.Errorf("free=%v: dedicated pod = %v (source mode %v), want one only on a miss, in source mode", free, gotPod, exec.req.SourceMode)
		}
	}
}

// An attempt outside source mode carries no source, even with the mode's warm
// pools on: its image holds the DAG.
func TestDispatchWarmAssignmentCarriesNoSourceOutsideSourceMode(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1", Image: "etl:v1", Source: "print(1)\n"}}
	placer := &fakePlacer{ok: true}
	d := newDispatcher(res, &fakeIssuer{token: "t"}, &fakeExecutor{})
	d.SetWarmPlacer(placer)
	d.SetSourceModeImage(runtimeImage)
	d.SetSourceModeWarm(true)

	if _, err := d.Dispatch(context.Background(), "run-1", "etl", "ver-1", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(placer.last.GetDagSource()) != 0 || placer.last.GetDagSourceSha256() != "" {
		t.Errorf("assignment outside source mode carries a source: %+v", placer.last)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// A version registered on the runtime image before source mode was turned on
// was never cap-checked by register. Dispatch checks the cap again and refuses
// the attempt (no retries) instead of letting the apiserver reject the pod
// (ADR 0067 §3). The pod is never created.
func TestDispatchSourceModeRefusesOversizeSource(t *testing.T) {
	big := strings.Repeat("x", domain.MaxSourceModeBytes+1)
	res := &fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1", Image: runtimeImage, Source: big}}
	exec := &fakeExecutor{}
	d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
	d.SetSourceModeImage(runtimeImage)

	disp, err := d.Dispatch(context.Background(), "run-1", "etl", "ver-1", pythonTask())
	if err == nil || disp != executor.Refused {
		t.Fatalf("Dispatch = %v, %v; want Refused with an error", disp, err)
	}
	if !strings.Contains(err.Error(), "131073 bytes") {
		t.Errorf("error %q should give the source size", err)
	}
	if exec.req.TaskID != "" {
		t.Errorf("executor was called for a refused source-mode attempt: %+v", exec.req)
	}
}

// Outside source mode the size of the source does not matter: the image
// carries the code and Source is not shipped to the pod.
func TestDispatchIgnoresSourceSizeOutsideSourceMode(t *testing.T) {
	big := strings.Repeat("x", domain.MaxSourceModeBytes+1)
	for name, mode := range map[string]string{"mode off": "", "own image": "other@sha256:abc"} {
		t.Run(name, func(t *testing.T) {
			res := &fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1", Image: runtimeImage, Source: big}}
			exec := &fakeExecutor{}
			d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
			d.SetSourceModeImage(mode)
			if _, err := d.Dispatch(context.Background(), "run-1", "etl", "ver-1", pythonTask()); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if exec.req.TaskID == "" || exec.req.SourceMode {
				t.Errorf("req = %+v, want a normal dispatch", exec.req)
			}
		})
	}
}
