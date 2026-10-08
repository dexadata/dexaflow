package executor

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// ADR 0067 §3: a source-mode pod carries dag.py in an annotation, projects it
// read-only through a downward API volume and runs from that directory.

func sourceVolume(pod *corev1.Pod) *corev1.Volume {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == sourceVolumeName {
			return &pod.Spec.Volumes[i]
		}
	}
	return nil
}

func TestBuildPodSourceMode(t *testing.T) {
	req := sampleReq()
	req.Source = "from leoflow import dag\n"
	req.SourceMode = true

	pod := BuildPod(req)

	if got := pod.Annotations[SourceAnnotation]; got != req.Source {
		t.Errorf("annotation %s = %q, want the source", SourceAnnotation, got)
	}
	if pod.Annotations["leoflow.io/task-instance-id"] != "ti-1" {
		t.Errorf("source mode dropped the existing annotations: %v", pod.Annotations)
	}
	vol := sourceVolume(pod)
	if vol == nil || vol.DownwardAPI == nil {
		t.Fatalf("no downward API volume %q: %+v", sourceVolumeName, pod.Spec.Volumes)
	}
	items := vol.DownwardAPI.Items
	if len(items) != 1 || items[0].Path != "dag.py" || items[0].FieldRef == nil ||
		items[0].FieldRef.FieldPath != "metadata.annotations['"+SourceAnnotation+"']" {
		t.Errorf("downward API items = %+v, want dag.py from the source annotation", items)
	}
	c := pod.Spec.Containers[0]
	var mount *corev1.VolumeMount
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == sourceVolumeName {
			mount = &c.VolumeMounts[i]
		}
	}
	if mount == nil || mount.MountPath != SourceMountPath || !mount.ReadOnly {
		t.Errorf("source mount = %+v, want read-only at %s", mount, SourceMountPath)
	}
	if c.WorkingDir != SourceMountPath {
		t.Errorf("workingDir = %q, want %s so the runtime imports dag from there", c.WorkingDir, SourceMountPath)
	}
}

// Without source mode the source is ignored, as before: no annotation, no
// volume and the image's own working directory.
func TestBuildPodIgnoresSourceOutsideSourceMode(t *testing.T) {
	req := sampleReq()
	req.Source = "from leoflow import dag\n"

	pod := BuildPod(req)

	if _, ok := pod.Annotations[SourceAnnotation]; ok {
		t.Errorf("annotation %s set outside source mode", SourceAnnotation)
	}
	if sourceVolume(pod) != nil {
		t.Errorf("source volume added outside source mode")
	}
	c := pod.Spec.Containers[0]
	if c.WorkingDir != "" {
		t.Errorf("workingDir = %q, want the image default", c.WorkingDir)
	}
	for _, m := range c.VolumeMounts {
		if m.Name == sourceVolumeName {
			t.Errorf("source mount added outside source mode: %+v", m)
		}
	}
}
