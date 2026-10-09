package executor

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
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

// The pod informer keeps no copy of a task's source (ADR 0067 §3): its cache
// holds every managed pod for the life of the process, and the source can be
// 128 KiB per pod. The transform drops only the source annotation.
func TestDropSourceAnnotation(t *testing.T) {
	pod := BuildPod(func() Request { r := sampleReq(); r.Source = "print(1)\n"; r.SourceMode = true; return r }())
	out, err := dropSourceAnnotation(pod)
	if err != nil {
		t.Fatalf("dropSourceAnnotation: %v", err)
	}
	got := out.(*corev1.Pod)
	if _, ok := got.Annotations[SourceAnnotation]; ok {
		t.Errorf("source annotation kept in the cache copy")
	}
	if got.Annotations["leoflow.io/task-instance-id"] != "ti-1" {
		t.Errorf("other annotations lost: %v", got.Annotations)
	}
	// Not a pod (a tombstone, say): passed through untouched.
	other := "not a pod"
	if o, err := dropSourceAnnotation(other); err != nil || o != other {
		t.Errorf("dropSourceAnnotation(non-pod) = %v, %v; want it unchanged", o, err)
	}
}

func TestPodInformerCacheDropsSourceAnnotation(t *testing.T) {
	p := informerPod("p-src", "run-a", "extract", corev1.PodRunning)
	p.Annotations = map[string]string{SourceAnnotation: "print(1)\n", "keep": "me"}
	cs := fake.NewClientset(p)
	pi := NewPodInformer(cs, "leoflow")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pi.Start(ctx)
	if !pi.WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	cached, err := pi.lister.Pods("leoflow").Get("p-src")
	if err != nil {
		t.Fatalf("cached pod: %v", err)
	}
	if _, ok := cached.Annotations[SourceAnnotation]; ok {
		t.Errorf("informer cache holds the source annotation")
	}
	if cached.Annotations["keep"] != "me" {
		t.Errorf("informer cache lost other annotations: %v", cached.Annotations)
	}
}
