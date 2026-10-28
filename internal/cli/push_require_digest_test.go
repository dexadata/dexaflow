package cli

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/api/apitest"
	"github.com/dexadata/dexaflow/internal/domain"
)

// `dexaflow push` against a server with deploy.require_digest (#1703), driven
// through the real register endpoint: under warn the register succeeds and
// the server's warning is printed on stderr; under enforce the push fails
// with the server's refusal, which says how to pin.
func TestPushPrintsTheDigestWarningAndRefusal(t *testing.T) {
	// A tag no registry holds: the push cannot pin it, so it registers
	// the tag and the server's setting decides.
	image := missingRegistry(t) + "/team/etl:v1"
	f := writePushSpec(t, image)
	serve := func(mode domain.DigestRequirement) *httptest.Server {
		return httptest.NewServer(apitest.NewServer(api.Dependencies{
			Observability: api.ObservabilityDeps{
				Logger: discardTestLogger(),
			},
			Auth: api.AuthDeps{
				DevNoAuth: true,
			},
			Execution: api.ExecutionDeps{
				RequireDigest: mode,
			},
			Stores: api.StoreDeps{
				Versions: registerRepo{},
			},
		}))
	}

	warn := serve(domain.DigestRequireWarn)
	defer warn.Close()
	out, errOut, err := run(t, "push", f, "--server", warn.URL, "--token", "x")
	if err != nil {
		t.Fatalf("push under warn: %v", err)
	}
	if !strings.Contains(out, "Registered") {
		t.Errorf("stdout = %q, want the register line", out)
	}
	if !strings.Contains(errOut, "warning: the server: ") || !strings.Contains(errOut, "deploy.require_digest=warn") || !strings.Contains(errOut, image) {
		t.Errorf("stderr = %q, want the server's warning naming the setting and the image", errOut)
	}

	enforce := serve(domain.DigestRequireEnforce)
	defer enforce.Close()
	_, _, err = run(t, "push", f, "--server", enforce.URL, "--token", "x")
	if err == nil || !strings.Contains(err.Error(), "deploy.require_digest=enforce") || !strings.Contains(err.Error(), "--image <repository>@sha256:<digest>") {
		t.Errorf("push under enforce = %v, want the refusal naming the setting and how to pin", err)
	}
}
