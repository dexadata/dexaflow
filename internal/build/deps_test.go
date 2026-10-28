package build

import (
	"os/exec"
	"strings"
	"testing"
)

// imageBuilderPackages are the import path prefixes that build images: this
// package and the registry client ADR 0073 chooses for it.
var imageBuilderPackages = []string{
	"github.com/dexadata/dexaflow/internal/build",
	"github.com/google/go-containerregistry",
	"github.com/docker/cli",
	"github.com/docker/docker-credential-helpers",
}

// linkedBuilderPackages returns the packages of deps that build images.
func linkedBuilderPackages(deps []string, forbidden []string) []string {
	var out []string
	for _, pkg := range deps {
		for _, f := range forbidden {
			if pkg == f || strings.HasPrefix(pkg, f+"/") {
				out = append(out, pkg)
			}
		}
	}
	return out
}

// Image building, and the registry client and its credentials with it, belong
// to the CLI only (ADR 0073, section 3). The server, the agent and the MCP
// server must never link them; this asserts it without building anything.
func TestOnlyTheCLILinksTheImageBuilder(t *testing.T) {
	for _, bin := range []string{"dexaflow-server", "dexaflow-agent", "dexaflow-mcp"} {
		out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "../../cmd/"+bin).Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", bin, err)
		}
		deps := strings.Split(strings.TrimSpace(string(out)), "\n")
		if bad := linkedBuilderPackages(deps, imageBuilderPackages); len(bad) > 0 {
			t.Errorf("%s links %s; image building and the registry client are CLI only (ADR 0073). "+
				"Run `go list -deps ./cmd/%s` and find the import that pulls them in", bin, strings.Join(bad, ", "), bin)
		}
	}
}

// The check can fail: the CLI itself links this package.
func TestTheLinkCheckSeesTheCLI(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "../../cmd/dexaflow").Output()
	if err != nil {
		t.Fatalf("go list -deps dexaflow: %v", err)
	}
	if bad := linkedBuilderPackages(strings.Split(string(out), "\n"), imageBuilderPackages); len(bad) == 0 {
		t.Error("the CLI does not link internal/build; the check would pass vacuously")
	}
}
