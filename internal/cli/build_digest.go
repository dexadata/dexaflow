package cli

import (
	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/build"
)

// imageDigest reads the pinned reference of an image already in its
// registry, for `deploy --skip-build`, which builds nothing and so has no
// builder result to read it from. It asks the registry, not a local image
// store, so an image any tool pushed can be deployed (ADR 0075).
func imageDigest(cmd *cobra.Command, reg build.Registry, image string) (string, error) {
	pinned, err := reg.Resolve(cmdContext(cmd), image)
	if err != nil {
		return "", err
	}
	return pinned.Ref, nil
}

// deployedDigest returns the pinned reference deploy registers: the one the
// builder's result carries when this deploy built and pushed the image, else
// the digest of the image already in the registry.
func deployedDigest(cmd *cobra.Command, reg build.Registry, image string, built build.Result) (string, error) {
	if built.Ref != "" {
		return built.Ref, nil
	}
	return imageDigest(cmd, reg, image)
}
