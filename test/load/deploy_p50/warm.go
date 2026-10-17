package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dexadata/dexaflow/internal/executor"
)

// warmReading is the warm-up of the pool of one deployed version: when its
// first warm worker appeared and when it became Ready, counted from the end
// of the deploy (the register), and how long the kubelet took to pull its
// image.
type warmReading struct {
	Pod       string
	Seen      time.Duration
	Ready     time.Duration
	Pull      time.Duration
	PullKnown bool
}

// warmWatch polls a cluster for the warm worker pods of one image.
type warmWatch struct {
	client    kubernetes.Interface
	namespace string
	poll      time.Duration
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

// sleepCtx waits for d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// wait polls until a warm worker pod running image is Ready and returns its
// reading, timed from start. Warm workers of other images (the previous
// version's pool, other DAGs) do not count. It fails when ctx ends first.
func (w warmWatch) wait(ctx context.Context, image string, start time.Time) (warmReading, error) {
	var r warmReading
	for {
		pod, ready, err := w.firstPod(ctx, image)
		if err != nil {
			return r, err
		}
		if pod != "" && r.Pod == "" {
			r.Pod, r.Seen = pod, w.now().Sub(start)
		}
		if ready != "" {
			r.Pod, r.Ready = ready, w.now().Sub(start)
			if r.Seen == 0 {
				r.Seen = r.Ready
			}
			r.Pull, r.PullKnown = w.pull(ctx, ready)
			return r, nil
		}
		if serr := w.sleep(ctx, w.poll); serr != nil {
			seen := "no warm worker pod of it appeared"
			if r.Pod != "" {
				seen = fmt.Sprintf("its first warm worker %s appeared %s after the deploy and never became Ready", r.Pod, r.Seen)
			}
			return r, fmt.Errorf("waiting for a Ready warm worker of %s: %s: %w", image, seen, serr)
		}
	}
}

// firstPod lists the warm worker pods and returns the name of the first one
// running image, and of the first such pod that is Ready.
func (w warmWatch) firstPod(ctx context.Context, image string) (pod, ready string, err error) {
	sel := executor.WarmWorkerLabel + "=" + executor.WarmWorkerLabelValue
	pods, err := w.client.CoreV1().Pods(w.namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return "", "", fmt.Errorf("listing warm worker pods in %s: %w", w.namespace, err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if !runsImage(p, image) {
			continue
		}
		if pod == "" {
			pod = p.Name
		}
		if podReady(p) {
			return pod, p.Name, nil
		}
	}
	return pod, "", nil
}

// runsImage reports whether a container of p runs image.
func runsImage(p *corev1.Pod, image string) bool {
	for _, c := range p.Spec.Containers {
		if c.Image == image {
			return true
		}
	}
	return false
}

// podReady reports whether p's Ready condition is true.
func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// pull reads the pull time of pod from its Pulled event.
func (w warmWatch) pull(ctx context.Context, pod string) (time.Duration, bool) {
	evs, err := w.client.CoreV1().Events(w.namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + pod + ",reason=Pulled",
	})
	if err != nil {
		return 0, false
	}
	for _, e := range evs.Items {
		// The fake client and some API servers ignore the field selector.
		if e.InvolvedObject.Name != pod || !strings.EqualFold(e.Reason, "Pulled") {
			continue
		}
		if d, ok := pullTime(e.Message); ok {
			return d, true
		}
	}
	return 0, false
}
