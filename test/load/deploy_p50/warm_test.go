package main

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/executor"
)

const (
	newImage = "r/p50@sha256:new"
	oldImage = "r/p50@sha256:old"
)

// warmPod is a warm worker pod of image, Ready or not.
func warmPod(name, image string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "leoflow",
			Labels: map[string]string{executor.WarmWorkerLabel: executor.WarmWorkerLabelValue}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: image}}},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}},
	}
}

// pulled is the kubelet's Pulled event of pod.
func pulled(pod, msg string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: pod + ".pulled", Namespace: "leoflow"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: pod, Namespace: "leoflow"},
		Reason:         "Pulled",
		Message:        msg,
	}
}

// scripted is a warmWatch on a fake cluster whose clock moves one second
// per poll; at[i] runs before poll i reads the cluster.
func scripted(t *testing.T, cs *fake.Clientset, at map[int]func()) (warmWatch, time.Time) {
	t.Helper()
	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	clock := start
	polls := 0
	step := func() {
		if f := at[polls]; f != nil {
			f()
		}
	}
	w := warmWatch{
		client:    cs,
		namespace: "leoflow",
		now:       func() time.Time { return clock },
		sleep: func(ctx context.Context, d time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			clock = clock.Add(d)
			polls++
			step()
			return nil
		},
		poll: time.Second,
	}
	step()
	return w, start
}

// create returns a step that creates or updates objs on cs.
func create(t *testing.T, cs *fake.Clientset, objs ...any) func() {
	return func() {
		for _, o := range objs {
			var err error
			switch v := o.(type) {
			case *corev1.Pod:
				if _, gerr := cs.CoreV1().Pods(v.Namespace).Get(context.Background(), v.Name, metav1.GetOptions{}); gerr == nil {
					_, err = cs.CoreV1().Pods(v.Namespace).Update(context.Background(), v, metav1.UpdateOptions{})
				} else {
					_, err = cs.CoreV1().Pods(v.Namespace).Create(context.Background(), v, metav1.CreateOptions{})
				}
			case *corev1.Event:
				_, err = cs.CoreV1().Events(v.Namespace).Create(context.Background(), v, metav1.CreateOptions{})
			}
			if err != nil {
				t.Error(err)
			}
		}
	}
}

// The watch times when the new version's first warm worker appears and when
// it is Ready, from the register, and reads its pull time; workers of the
// previous version do not count.
func TestWarmWatchTimesTheNewVersionsFirstWorker(t *testing.T) {
	// The previous version's Ready worker is there from the start; its name
	// sorts first, so its Pulled event is listed before the new pod's.
	cs := fake.NewClientset(warmPod("a-old", oldImage, true))
	w, start := scripted(t, cs, map[int]func(){
		3: create(t, cs, warmPod("new-1", newImage, false)),
		7: create(t, cs, warmPod("new-1", newImage, true),
			pulled("new-1", `Successfully pulled image "r/p50@sha256:new" in 850ms (850ms including waiting). Image size: 9 bytes.`),
			pulled("a-old", `Successfully pulled image "r/p50@sha256:old" in 40s (40s including waiting)`)),
	})
	got, err := w.wait(context.Background(), newImage, start)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pod != "new-1" || got.Seen != 3*time.Second || got.Ready != 7*time.Second {
		t.Errorf("reading = %+v, want new-1 seen at 3s and Ready at 7s", got)
	}
	if !got.PullKnown || got.Pull != 850*time.Millisecond {
		t.Errorf("pull = %s (known %v), want the new pod's 850ms", got.Pull, got.PullKnown)
	}
}

// With no worker of the new version, the watch ends with the context and
// says what it saw.
func TestWarmWatchFailsWhenNoNewWorkerIsReady(t *testing.T) {
	cs := fake.NewClientset(warmPod("old-1", oldImage, true), warmPod("new-1", newImage, false))
	w, start := scripted(t, cs, nil)
	ctx, cancel := context.WithCancel(context.Background())
	inner := w.sleep
	n := 0
	w.sleep = func(c context.Context, d time.Duration) error {
		n++
		if n == 5 {
			cancel()
		}
		return inner(c, d)
	}
	_, err := w.wait(ctx, newImage, start)
	if err == nil {
		t.Fatal("no error without a Ready worker of the new version")
	}
	if !strings.Contains(err.Error(), "new-1") || !strings.Contains(err.Error(), newImage) {
		t.Errorf("error does not name the image and the pod seen: %v", err)
	}
}
