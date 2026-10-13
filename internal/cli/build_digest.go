package cli

import (
	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/build"
)

// imageDigest resolves the pinned repo digest of an image the builder tool
// already pushed, for `deploy --skip-build`, which builds nothing and so has
// no builder result to read it from (ADR 0015: shelled out, no Docker SDK).
func imageDigest(cmd *cobra.Command, builder, image string) (string, error) {
	return build.LocalDaemon{Tool: builder}.Digest(cmdContext(cmd), image, cmd.ErrOrStderr())
}

// deployedDigest returns the pinned reference deploy registers: the one the
// builder's result carries when this deploy built and pushed the image, else
// the digest of the image already pushed.
func deployedDigest(cmd *cobra.Command, builder, image string, built build.Result) (string, error) {
	if built.Ref != "" {
		return built.Ref, nil
	}
	return imageDigest(cmd, builder, image)
}
