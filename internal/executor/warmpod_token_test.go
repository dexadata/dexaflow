package executor

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// TestBuildWarmPodAgentTokenReachesOnlyTheAgent pins the parts of the warm pod
// spec that keep the agent's bootstrap credential away from the task process it
// starts, in the hardened configuration (exchange transport, non-root, read-only
// root):
//
//   - the credential is a projected ServiceAccount token, never a Secret, and no
//     plaintext token rides in the container env;
//   - its volume is mounted read-only, only at the agent token directory, and
//     only into the warm container (no other container or path can see it);
//   - the projection grants no read to other users;
//   - no Kubernetes API token is mounted at all.
//
// The agent and the task share one container and one UID, so these do not by
// themselves make the file unreadable to the task: the agent also strips
// LEOFLOW_AGENT_TOKEN_PATH from the task env and is non-dumpable, and the
// control plane refuses a second registration of a live worker identity. A
// regression in any of these properties fails here.
func TestBuildWarmPodAgentTokenReachesOnlyTheAgent(t *testing.T) {
	spec := baseWarmSpec()
	spec.BootstrapToken = ""
	spec.AgentTokenTransport = agentTransportExchange
	spec.PodSecurity = PodSecurity{RunAsNonRoot: true}
	spec.ReadOnlyRootFilesystem = true
	pod := BuildWarmPod(spec)

	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("the warm pod must not automount a Kubernetes API token")
	}
	if _, ok := warmEnvVar(pod, "LEOFLOW_AGENT_TOKEN"); ok {
		t.Error("no agent token may ride in the container env under the exchange transport")
	}

	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == agentTokenVolumeName {
			vol = &pod.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.Projected == nil {
		t.Fatalf("the agent token must be a projected volume, got %+v", vol)
	}
	if vol.Secret != nil || vol.ConfigMap != nil {
		t.Fatalf("the agent token volume must not be backed by a Secret or ConfigMap: %+v", vol.VolumeSource)
	}
	if len(vol.Projected.Sources) != 1 || vol.Projected.Sources[0].ServiceAccountToken == nil {
		t.Fatalf("the projection must carry exactly the ServiceAccount token: %+v", vol.Projected.Sources)
	}
	if src := vol.Projected.Sources[0]; src.Secret != nil || src.ConfigMap != nil || src.DownwardAPI != nil {
		t.Fatalf("the projection must carry nothing besides the token: %+v", src)
	}
	if m := vol.Projected.DefaultMode; m != nil && *m&0o007 != 0 {
		t.Errorf("the token projection must not be readable by other users, defaultMode = %#o", *m)
	}

	if n := len(pod.Spec.InitContainers) + len(pod.Spec.EphemeralContainers); n != 0 {
		t.Fatalf("the warm pod must run no other container that could mount the token, found %d", n)
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("the warm pod must have exactly the warm container, got %d", len(pod.Spec.Containers))
	}
	var mounts []corev1.VolumeMount
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.Name == agentTokenVolumeName {
			mounts = append(mounts, m)
		}
	}
	if len(mounts) != 1 {
		t.Fatalf("the agent token must be mounted exactly once, got %+v", mounts)
	}
	if m := mounts[0]; m.MountPath != agentTokenMountDir || !m.ReadOnly || m.SubPath != "" {
		t.Errorf("token mount = %+v, want read-only at %s with no subPath", m, agentTokenMountDir)
	}
	if env, _ := warmEnvVar(pod, "LEOFLOW_AGENT_TOKEN_PATH"); env.Value != agentTokenMountDir+"/"+agentTokenFile {
		t.Errorf("LEOFLOW_AGENT_TOKEN_PATH = %q, want the agent token file", env.Value)
	}
}
