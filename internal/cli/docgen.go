package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// newGenDocsCommand generates the CLI reference as markdown (one file per
// command) for the docs site. Hidden: it is a build/docs tool, not a user
// command. Run via `dexaflow gen-docs --dir docs/cli` (the docs workflow does this).
func newGenDocsCommand() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:    "gen-docs",
		Short:  "Generate the CLI reference markdown.",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return fmt.Errorf("creating docs dir %s: %w", dir, err)
			}
			root := cmd.Root()
			root.DisableAutoGenTag = true // no churny "auto generated on <date>" footer
			if err := doc.GenMarkdownTree(root, dir); err != nil {
				return fmt.Errorf("generating CLI docs: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "docs/cli", "output directory for the generated markdown")
	return cmd
}
