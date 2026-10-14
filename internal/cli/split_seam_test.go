package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/dexadata/dexaflow/internal/build"
)

// The tests of this package never ask a public registry for a base image:
// one off the loopback is refused, so a build splits (ADR 0077) only when a
// test serves the base image itself.
// uv is not there unless a test installs a fake one (fakeUV), so the
// host's uv decides nothing.
func init() {
	baseRegistry = loopbackRegistry{}
	lookUV = func() (string, error) { return "", errors.New("uv is not installed in the tests") }
}

// loopbackRegistry resolves images on 127.0.0.1 and localhost only.
type loopbackRegistry struct{}

func (loopbackRegistry) Resolve(ctx context.Context, image string) (build.Pinned, error) {
	host, _, _ := strings.Cut(image, "/")
	if !strings.HasPrefix(host, "127.0.0.1:") && !strings.HasPrefix(host, "localhost") {
		return build.Pinned{}, errors.New("unit tests read no public registry")
	}
	return build.Registry{}.Resolve(ctx, image)
}

func (loopbackRegistry) Base(ctx context.Context, base build.Pinned, platform string) (build.BaseImage, error) {
	host, _, _ := strings.Cut(base.Ref, "/")
	if !strings.HasPrefix(host, "127.0.0.1:") && !strings.HasPrefix(host, "localhost") {
		return build.BaseImage{}, errors.New("unit tests read no public registry")
	}
	return build.Registry{}.Base(ctx, base, platform)
}
