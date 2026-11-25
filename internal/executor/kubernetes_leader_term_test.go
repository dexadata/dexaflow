package executor

import "testing"

// TestBuildPodAnnotatesTheLeaderTerm: a task pod names the lease term of the
// leader that created it (ADR 0106, point 5), so an operator can tell a pod
// a deposed leader created; a request without a term adds no annotation.
func TestBuildPodAnnotatesTheLeaderTerm(t *testing.T) {
	req := Request{TaskInstanceID: "ti", DagID: "d", RunID: "r", TaskID: "t", Image: "i", LeaderTerm: 7}
	if got := BuildPod(req).Annotations[LeaderTermAnnotation]; got != "7" {
		t.Fatalf("annotation %s = %q, want 7", LeaderTermAnnotation, got)
	}
	req.LeaderTerm = 0
	if _, ok := BuildPod(req).Annotations[LeaderTermAnnotation]; ok {
		t.Fatal("a request without a term was annotated")
	}
}
