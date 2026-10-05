package executor

import (
	"encoding/json"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// podEnvMap flattens a container's plain-valued env vars into a lookup.
func podEnvMap(c corev1.Container) map[string]string {
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	return env
}

// envVar returns the named EnvVar (by reference) or nil.
func envVar(c corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

// TestBuildPodEnvVarTransportUnchanged: the default (and empty) transport sets
// the plaintext LEOFLOW_AGENT_TOKEN env var exactly as today — no projected
// volume, no token-path/transport env vars, no identity annotation. This is the
// safe default: a deploy that does not opt in is byte-identical to before.
func TestBuildPodEnvVarTransportUnchanged(t *testing.T) {
	for _, transport := range []string{"", "envvar"} {
		req := sampleReq()
		req.AgentTokenTransport = transport
		pod := BuildPod(req)
		c := pod.Spec.Containers[0]
		env := podEnvMap(c)

		if env["LEOFLOW_AGENT_TOKEN"] != "tok" {
			t.Errorf("transport %q: LEOFLOW_AGENT_TOKEN = %q, want plaintext %q", transport, env["LEOFLOW_AGENT_TOKEN"], "tok")
		}
		if _, ok := env["LEOFLOW_AGENT_TOKEN_PATH"]; ok {
			t.Errorf("transport %q: LEOFLOW_AGENT_TOKEN_PATH must not be set on the env-var path", transport)
		}
		if _, ok := env["LEOFLOW_AGENT_TOKEN_TRANSPORT"]; ok {
			t.Errorf("transport %q: LEOFLOW_AGENT_TOKEN_TRANSPORT must not be set on the env-var path", transport)
		}
		for _, v := range pod.Spec.Volumes {
			if v.Name == agentTokenVolumeName {
				t.Errorf("transport %q: projected token volume must not be mounted on the env-var path", transport)
			}
		}
		if _, ok := pod.Annotations[AgentIdentityAnnotation]; ok {
			t.Errorf("transport %q: identity annotation must not be set on the env-var path", transport)
		}
	}
}

// TestBuildPodExchangeTransportProjectsToken: under exchange the plaintext token
// is GONE from the pod object — replaced by a projected ServiceAccount token
// volume (bound to the control-plane audience, short-lived) the agent reads from
// a file and exchanges. The identity annotation lets the control plane resolve
// pod → task instance after TokenReview.
func TestBuildPodExchangeTransportProjectsToken(t *testing.T) {
	req := sampleReq()
	req.AgentTokenTransport = "exchange"
	req.AgentTokenAudience = "leoflow-control-plane"
	req.AgentTokenExpirationSeconds = 900
	pod := BuildPod(req)
	c := pod.Spec.Containers[0]
	env := podEnvMap(c)

	// The invariant: NO bearer credential as a plaintext field on the Pod object.
	if _, ok := env["LEOFLOW_AGENT_TOKEN"]; ok {
		t.Error("exchange: plaintext LEOFLOW_AGENT_TOKEN must NOT be present on the pod spec")
	}
	if env["LEOFLOW_AGENT_TOKEN_TRANSPORT"] != "exchange" {
		t.Errorf("exchange: LEOFLOW_AGENT_TOKEN_TRANSPORT = %q, want exchange", env["LEOFLOW_AGENT_TOKEN_TRANSPORT"])
	}
	if env["LEOFLOW_AGENT_TOKEN_PATH"] != agentTokenMountDir+"/"+agentTokenFile {
		t.Errorf("exchange: LEOFLOW_AGENT_TOKEN_PATH = %q, want %q", env["LEOFLOW_AGENT_TOKEN_PATH"], agentTokenMountDir+"/"+agentTokenFile)
	}

	// The projected volume must exist, source the pod's SA token for the given
	// audience, and be mounted read-only.
	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == agentTokenVolumeName {
			vol = &pod.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.Projected == nil || len(vol.Projected.Sources) != 1 || vol.Projected.Sources[0].ServiceAccountToken == nil {
		t.Fatalf("exchange: projected SA token volume not mounted: %+v", pod.Spec.Volumes)
	}
	sat := vol.Projected.Sources[0].ServiceAccountToken
	if sat.Audience != "leoflow-control-plane" {
		t.Errorf("projected token audience = %q, want leoflow-control-plane", sat.Audience)
	}
	if sat.ExpirationSeconds == nil || *sat.ExpirationSeconds != 900 {
		t.Errorf("projected token expiration = %v, want 900", sat.ExpirationSeconds)
	}
	if sat.Path != agentTokenFile {
		t.Errorf("projected token path = %q, want %q", sat.Path, agentTokenFile)
	}
	var mounted bool
	for _, m := range c.VolumeMounts {
		if m.Name == agentTokenVolumeName {
			mounted = true
			if !m.ReadOnly || m.MountPath != agentTokenMountDir {
				t.Errorf("projected token mount = %+v, want readonly at %q", m, agentTokenMountDir)
			}
		}
	}
	if !mounted {
		t.Error("exchange: projected token volume is not mounted into the task container")
	}

	// The identity annotation must round-trip the exact (unsanitized) identity the
	// resolver needs — pod labels are sanitized and lossy, so the resolver reads this.
	raw, ok := pod.Annotations[AgentIdentityAnnotation]
	if !ok {
		t.Fatal("exchange: identity annotation not set")
	}
	var got PodIdentity
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("identity annotation is not valid JSON: %v", err)
	}
	epoch := 0
	want := PodIdentity{TaskInstanceID: "ti-1", TenantID: "default", DagID: "etl", RunID: "r1", TaskID: "extract", TryNumber: 1, AttemptEpoch: &epoch}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("identity annotation = %+v, want %+v", got, want)
	}
}

// TestBuildPodExchangeExpirationFloor: an unset or too-small expiration is
// floored to the minimum so a very short task's bootstrap token has not already
// expired at Register (ADR 0055 "Verify at implementation").
func TestBuildPodExchangeExpirationFloor(t *testing.T) {
	for _, secs := range []int64{0, 60} {
		req := sampleReq()
		req.AgentTokenTransport = "exchange"
		req.AgentTokenExpirationSeconds = secs
		pod := BuildPod(req)
		for _, v := range pod.Spec.Volumes {
			if v.Name == agentTokenVolumeName {
				exp := v.Projected.Sources[0].ServiceAccountToken.ExpirationSeconds
				if exp == nil || *exp != minProjectedTokenExpirationSeconds {
					t.Errorf("expiration %d floored to %v, want %d", secs, exp, minProjectedTokenExpirationSeconds)
				}
			}
		}
	}
}

// TestBuildPodExchangeSecretKeyRefFallback: for a cluster that cannot project an
// SA token, the SecretKeyRef fallback sources LEOFLOW_AGENT_TOKEN from a
// Kubernetes Secret (ValueFrom, not a plaintext Value) and mounts no projected
// volume — still keeping the credential off the plaintext pod spec.
func TestBuildPodExchangeSecretKeyRefFallback(t *testing.T) {
	req := sampleReq()
	req.AgentTokenTransport = "exchange"
	req.AgentTokenSecretName = "leoflow-agent-token-ti-1"
	req.AgentTokenSecretKey = "token"
	pod := BuildPod(req)
	c := pod.Spec.Containers[0]

	ev := envVar(c, "LEOFLOW_AGENT_TOKEN")
	if ev == nil || ev.Value != "" || ev.ValueFrom == nil || ev.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("fallback: LEOFLOW_AGENT_TOKEN must be a SecretKeyRef with no plaintext value, got %+v", ev)
	}
	if ev.ValueFrom.SecretKeyRef.Name != "leoflow-agent-token-ti-1" || ev.ValueFrom.SecretKeyRef.Key != "token" {
		t.Errorf("fallback SecretKeyRef = %+v", ev.ValueFrom.SecretKeyRef)
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name == agentTokenVolumeName {
			t.Error("fallback: projected token volume must not be mounted when SecretKeyRef fallback is used")
		}
	}
}
