package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The agent is a thin static binary that runs as PID 1 inside every task pod
// (ADR 0004). It talks to the control plane over gRPC and never to the
// Kubernetes API, so it must not link the Kubernetes client libraries — their
// transitive graph is large enough to dominate a binary this size.
//
// CI already caps the built binary at 20 MB, but that gate is a poor guard for
// this: it needs a build, it only fires once the bloat crosses the cap (a
// partial import that fits under it slips through), and when it does fire it
// reports a number rather than a cause. This asserts the property directly and
// names it, in milliseconds and without building anything.
//
// The realistic way this breaks is indirect: someone adds a k8s import to a
// package the agent already depends on — internal/domain is the near miss,
// which gained k8s.io/apimachinery for resource-quantity validation and is not
// imported by the agent today. Nothing enforces that it stays that way except
// this test.
func TestAgentLinksNoKubernetesPackages(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "./").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	var offenders []string
	for _, pkg := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(pkg, "k8s.io/") {
			offenders = append(offenders, pkg)
		}
	}
	if len(offenders) == 0 {
		return
	}
	t.Fatalf("the agent now links %d Kubernetes package(s), which ADR 0004 forbids — "+
		"it speaks gRPC to the control plane and never touches the Kubernetes API.\n"+
		"First few: %s\n"+
		"This is almost always indirect: a shared package gained a k8s import. "+
		"Run `go list -deps ./cmd/dexaflow-agent | grep k8s.io` and then "+
		"`go mod why -m k8s.io/apimachinery` to find the path in, and move the "+
		"dependency behind an interface the agent does not import.",
		len(offenders), strings.Join(offenders[:min(5, len(offenders))], ", "))
}
