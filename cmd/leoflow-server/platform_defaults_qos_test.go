package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestPlatformDefaultsEmitLimits locks the QoS half of #725: when the cluster
// configures executor.defaults.resources_*, the L0 default must carry BOTH
// Requests and Limits. Requests alone yields Burstable (or, when the key never
// bound, BestEffort) QoS; only Requests == Limits reaches Guaranteed, which is
// the whole point of pinning a per-cluster default.
func TestPlatformDefaultsEmitLimits(t *testing.T) {
	pd := platformDefaults(config.PlatformDefaultsSection{
		ResourcesCPU:    "250m",
		ResourcesMemory: "256Mi",
	})
	if pd.Resources == nil {
		t.Fatal("platform default Resources is nil despite configured cpu/memory")
	}
	if pd.Resources.Requests == nil {
		t.Fatal("platform default Requests is nil")
	}
	if pd.Resources.Limits == nil {
		t.Fatal("platform default Limits is nil: task pods relying on the default " +
			"cannot reach Guaranteed QoS")
	}
	if pd.Resources.Limits.CPU != "250m" || pd.Resources.Limits.Memory != "256Mi" {
		t.Errorf("Limits = %+v, want cpu=250m memory=256Mi", *pd.Resources.Limits)
	}
	if pd.Resources.Requests.CPU != "250m" || pd.Resources.Requests.Memory != "256Mi" {
		t.Errorf("Requests = %+v, want cpu=250m memory=256Mi", *pd.Resources.Requests)
	}
}

// TestDefaultResourcesBuildGuaranteedPod chains the real mapping to the executor:
// a task that relies on the platform default (no resources of its own) must build
// a pod whose container carries both requests and limits, i.e. Guaranteed QoS.
func TestDefaultResourcesBuildGuaranteedPod(t *testing.T) {
	pd := platformDefaults(config.PlatformDefaultsSection{
		ResourcesCPU:    "250m",
		ResourcesMemory: "256Mi",
	})
	if pd.Resources == nil {
		t.Fatal("platform default Resources is nil despite configured cpu/memory")
	}
	// The dispatcher assigns the platform default verbatim to a task that declared
	// no resources of its own (see internal/dispatch: req.Resources = *d.defaults.Resources).
	req := executor.Request{
		TaskInstanceID: "ti-1", TenantID: "default", DagID: "etl", RunID: "r1",
		TaskID: "extract", TryNumber: 1, Image: "img:v1", Operator: "python",
		Resources: *pd.Resources,
	}
	pod := executor.BuildPod(req)
	c := pod.Spec.Containers[0]
	if len(c.Resources.Requests) == 0 {
		t.Fatal("pod container has no resource requests")
	}
	if len(c.Resources.Limits) == 0 {
		t.Fatal("pod container has no resource limits: BestEffort/Burstable, not Guaranteed")
	}
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		req := c.Resources.Requests[name]
		lim := c.Resources.Limits[name]
		if req.IsZero() || lim.IsZero() || req.Cmp(lim) != 0 {
			t.Errorf("%s: request=%s limit=%s, want equal and non-zero (Guaranteed)",
				name, req.String(), lim.String())
		}
	}
}

// TestPlatformDefaultsWarnOnPartialPair: a PARTIAL executor.defaults.resources
// pair — one quantity set, the other empty — must produce exactly one boot WARN.
//
// The chart documents the pair as landing a task that declares no resources of
// its own in Guaranteed QoS, and that promise holds only when BOTH are set. With
// one set, platformDefaults still builds a non-nil default, the empty side is
// dropped from the pod spec, and the task gets one dimension pinned and the
// other with no request or limit at all: Burstable, first evicted under node
// pressure, and invisible to the autoscaler on the dimension left out. It is a
// documented setting, not an error, so boot proceeds — the WARN is the only
// operator-visible signal, exactly like the disabled credential ceiling (#802).
//
// The record must carry the keys and the offending value as ATTRIBUTES so a JSON
// log can be alerted on; prose alone is only greppable by a human.
func TestPlatformDefaultsWarnOnPartialPair(t *testing.T) {
	warn := func(newHandler func(*bytes.Buffer) slog.Handler, cpu, mem string) string {
		var buf bytes.Buffer
		cfg := &config.ServerConfig{}
		cfg.Auth.MaxAttemptCredentialLifetime = 24 * time.Hour // silence the ceiling WARN
		cfg.Executor.Defaults.ResourcesCPU = cpu
		cfg.Executor.Defaults.ResourcesMemory = mem
		warnStartup(cfg, slog.New(newHandler(&buf)))
		return buf.String()
	}
	text := func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) }
	jsonh := func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) }

	for _, tc := range []struct{ name, cpu, mem, set, missing, value string }{
		{"cpu without memory", "250m", "", "executor.defaults.resources_cpu", "executor.defaults.resources_memory", "250m"},
		{"memory without cpu", "", "256Mi", "executor.defaults.resources_memory", "executor.defaults.resources_cpu", "256Mi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := warn(text, tc.cpu, tc.mem)
			if strings.Count(out, "level=WARN") != 1 {
				t.Errorf("want exactly one WARN, got %q", out)
			}
			// The sentence must name BOTH keys — the one that is set and the one the
			// operator has to add — and the QoS class they actually get.
			for _, want := range []string{tc.set, tc.missing, "Burstable"} {
				if !strings.Contains(out, want) {
					t.Errorf("WARN must mention %q, got %q", want, out)
				}
			}
			got := warn(jsonh, tc.cpu, tc.mem)
			for _, want := range []string{
				`"config_key":"` + tc.set + `"`,
				`"missing_config_key":"` + tc.missing + `"`,
				`"value":"` + tc.value + `"`,
			} {
				if !strings.Contains(got, want) {
					t.Errorf("the JSON WARN must carry %s, got %s", want, got)
				}
			}
		})
	}

	// Both set is the documented configuration, and neither set is the shipped
	// default: a WARN operators learn to ignore is worse than none.
	if out := warn(text, "250m", "256Mi"); out != "" {
		t.Errorf("a complete pair must log nothing at boot, got %q", out)
	}
	if out := warn(text, "", ""); out != "" {
		t.Errorf("an unset pair must log nothing at boot, got %q", out)
	}

	// The platform default is applied by the dispatcher, which is scheduler-side,
	// so an api-only replica in a split install must not warn about behavior it
	// does not implement — the same gate the credential-ceiling WARN carries.
	var buf bytes.Buffer
	cfg := &config.ServerConfig{}
	cfg.Auth.MaxAttemptCredentialLifetime = 24 * time.Hour
	cfg.Server.Role = "api"
	cfg.Executor.Defaults.ResourcesCPU = "250m"
	warnStartup(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
	if buf.Len() != 0 {
		t.Errorf("an api-only role must not emit the partial-pair WARN, got %q", buf.String())
	}
}
