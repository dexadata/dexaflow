package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// buildTarget is one project the workspace build will compile and build, with
// the image reference derived from that project's own `registry:` block.
type buildTarget struct {
	dagID string
	dir   string
	image string
}

// buildTargets decides which projects in a workspace can be built and with what
// image, and reports the ones that cannot (#1115).
//
// Per-project by construction: each project declares its own registry, and
// deployImageRef already derives a reference from it, so nothing about image
// naming is invented here.
//
// A project with no registry is REPORTED rather than skipped quietly. A command
// called "build everything" that builds less than everything, and says nothing,
// is worse than one that refuses — the operator walks away believing images
// exist.
func buildTargets(projects []Project, version, sha string) (targets []buildTarget, skipped []string) {
	for _, p := range projects {
		if p.Config == nil || p.Config.Registry == nil ||
			p.Config.Registry.URL == "" || p.Config.Registry.ImageName == "" {
			skipped = append(skipped, fmt.Sprintf("%s (no registry.url/registry.image_name in its dexaflow.yaml — `dexaflow compile %s --image <ref> --build` builds it by hand)", p.DagID, p.Path))
			continue
		}
		targets = append(targets, buildTarget{
			dagID: p.DagID,
			dir:   p.Path,
			image: deployImageRef(p.Config, version, sha),
		})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].dagID < targets[j].dagID })
	sort.Strings(skipped)
	return targets, skipped
}

// newBuildCommand builds every project in a workspace, each with the image its
// own dexaflow.yaml declares.
//
// `dexaflow compile <dir> --image <ref> --build` has always built ONE project.
// With several DAGs in a workspace that meant running it once per directory with
// the right reference each time, by hand — which is where the reference gets
// wrong. This is the loop, and nothing more: it reuses the same compile path
// rather than a second build implementation, so there is one place where an
// image is produced.
func newBuildCommand() *cobra.Command {
	var (
		push       bool
		builder    string
		dagVersion string
		sha        string
	)
	cmd := &cobra.Command{
		Use:   "build [workspace]",
		Short: "Build the container image of every DAG project in a workspace.",
		Long: "Compiles and builds each project found under the workspace, using the image " +
			"reference that project's own `registry:` block derives.\n\n" +
			"A project that declares no registry cannot be built and is reported by name " +
			"rather than skipped quietly — a build that covers less than the workspace, " +
			"silently, leaves you believing images exist.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws := "."
			if len(args) == 1 {
				ws = args[0]
			}
			spec, derr := DiscoverProjects(ws)
			if derr != nil {
				return derr
			}
			// Resolve exactly as `dexaflow deploy` does before deriving the
			// reference. Handing the raw flags to deployImageRef means the
			// default tag strategy resolves against two empty strings and emits
			// `<url>/<name>:` — a reference docker refuses, from a command whose
			// entire purpose is to stop references being got wrong by hand.
			version := dagVersion
			if version == "" {
				version = gitVersion(cmdContext(cmd))
			}
			commit := sha
			if commit == "" {
				commit = gitSHA(cmdContext(cmd))
			}
			targets, skipped := buildTargets(spec, version, commit)
			out := cmd.OutOrStdout()
			for _, s := range skipped {
				//nolint:errcheck // a warning that cannot be delivered must not fail the build
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: not building %s\n", s)
			}
			if len(targets) == 0 {
				//nolint:errcheck // informational
				fmt.Fprintf(out, "no buildable project found under %s\n", ws)
				return nil
			}
			for i, t := range targets {
				//nolint:errcheck // informational
				fmt.Fprintf(out, "▸ %s → %s\n", t.dagID, t.image)
				o := compileOptions{
					output: filepath.Join(t.dir, "dag.json"),
					image:  t.image,
					build:  true,
					push:   push,
					// Named explicitly: an empty dockerfile makes
					// ensureDockerfile stat the project directory, find it, and
					// hand docker a directory as -f — so the generated
					// Dockerfile, the default path for almost every project, is
					// never produced.
					dockerfile: "Dockerfile",
					builder:    builder,
					dagVersion: version,
				}
				if rerr := runCompile(cmd, t.dir, o); rerr != nil {
					// Named, and stopped: a partial workspace where some images
					// are new and some are stale is worse than a clear failure,
					// because the difference is invisible afterwards. Say which
					// images DID get built, so the operator does not have to
					// scroll back through the builder output to find out.
					if i > 0 {
						//nolint:errcheck // informational
						fmt.Fprintf(out, "built before the failure: %s\n", strings.Join(builtIDs(targets[:i]), ", "))
					}
					return fmt.Errorf("building %s: %w", t.dagID, rerr)
				}
			}
			// The skipped list is repeated here on purpose: it was printed
			// before minutes of builder output, and a closing "built N" alone
			// reads as "the workspace is built".
			if len(skipped) > 0 {
				//nolint:errcheck // informational
				fmt.Fprintf(out, "built %d image(s), skipped %d: %s\n", len(targets), len(skipped), strings.Join(skipped, "; "))
				return nil
			}
			//nolint:errcheck // informational
			fmt.Fprintf(out, "built %d image(s)\n", len(targets))
			return nil
		},
	}
	cmd.Flags().BoolVar(&push, "push", false, "push each built image to its registry")
	cmd.Flags().StringVar(&builder, "builder", "docker", "image build tool to shell out to (e.g. docker, podman, nerdctl)")
	cmd.Flags().StringVar(&dagVersion, "dag-version", "", "version recorded in each dag.json and used by the registry tag strategy (default: git describe, else dev)")
	cmd.Flags().StringVar(&sha, "sha", "", "commit sha for the git_sha tag strategy (default: git rev-parse --short HEAD)")
	return cmd
}

// builtIDs lists the dag ids of targets already built, for the failure summary.
func builtIDs(done []buildTarget) []string {
	ids := make([]string, 0, len(done))
	for _, t := range done {
		ids = append(ids, t.dagID)
	}
	return ids
}
