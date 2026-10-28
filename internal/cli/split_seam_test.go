package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/dexadata/dexaflow/internal/build"
)

// loopbackRegistry resolves images on 127.0.0.1 and localhost only, with
// the project's registry client reg.
type loopbackRegistry struct{ reg build.Registry }

func (l loopbackRegistry) Resolve(ctx context.Context, image string) (build.Pinned, error) {
	host, _, _ := strings.Cut(image, "/")
	if !strings.HasPrefix(host, "127.0.0.1:") && !strings.HasPrefix(host, "localhost") {
		return build.Pinned{}, errors.New("unit tests read no public registry")
	}
	return l.reg.Resolve(ctx, image)
}

func (l loopbackRegistry) Base(ctx context.Context, base build.Pinned, platform string) (build.BaseImage, error) {
	host, _, _ := strings.Cut(base.Ref, "/")
	if !strings.HasPrefix(host, "127.0.0.1:") && !strings.HasPrefix(host, "localhost") {
		return build.BaseImage{}, errors.New("unit tests read no public registry")
	}
	return l.reg.Base(ctx, base, platform)
}
