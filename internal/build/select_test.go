package build

import (
	"context"
	"strings"
	"testing"
)

// Until a daemonless path exists, every build resolves to the local daemon
// fallback, and the reason says so.
func TestSelectFallsBackToTheLocalDaemon(t *testing.T) {
	b, reason := Select(context.Background(), Options{Tool: "podman"}, "r:1", nil)
	ld, ok := b.(LocalDaemon)
	if !ok || ld.Tool != "podman" {
		t.Fatalf("Select = %#v, want LocalDaemon with the configured tool", b)
	}
	if !strings.Contains(reason, "fallback") {
		t.Errorf("reason %q does not say this is the fallback", reason)
	}
}

func TestSelectDefaultsTheToolToDocker(t *testing.T) {
	b, _ := Select(context.Background(), Options{}, "r:1", nil)
	if b.Describe() != "local-daemon (docker)" {
		t.Errorf("Describe = %q", b.Describe())
	}
}

func TestResolutionOrderIsTheADROrder(t *testing.T) {
	want := []Path{PathSkip, PathCodeLayer, PathDependencyLayer, PathRemote, PathLocalDaemon}
	if got := ResolutionOrder(); strings.Join(pathStrings(got), ",") != strings.Join(pathStrings(want), ",") {
		t.Errorf("ResolutionOrder = %v, want %v", got, want)
	}
}

func pathStrings(ps []Path) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}
