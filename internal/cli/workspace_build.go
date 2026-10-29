package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// buildTarget is one project the workspace build will compile and build, with
// the image reference derived from that project's own `registry:` block.
type buildTarget struct {
	dagID   string
	dir     string
	image   string
	version string
}

// projectLabel is the version label and git revision one project is built
// with. Each project gets its own: one label for a whole workspace made every
// edit to one DAG conflict with the version the others had registered.
type projectLabel struct {
	version string
	rev     gitRevision
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
func buildTargets(projects []Project, label func(Project) projectLabel) (targets []buildTarget, skipped []string) {
	for _, p := range projects {
		if !hasRegistry(p) {
			skipped = append(skipped, fmt.Sprintf("%s (no registry in its dexaflow.yaml, the workspace or the target; `dexaflow compile %s --image <ref> --build` builds it by hand)", p.DagID, p.Path))
			continue
		}
		l := label(p)
		targets = append(targets, buildTarget{
			dagID:   p.DagID,
			dir:     p.Path,
			image:   deployImageRef(p.Config, l.version, l.rev),
			version: l.version,
		})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].dagID < targets[j].dagID })
	sort.Strings(skipped)
	return targets, skipped
}

// hasRegistry reports whether a project declares where its image goes. It is
// deploy's requireRegistry, so a blank url is no registry for both (#1121).
func hasRegistry(p Project) bool {
	return requireRegistry(p.Config) == nil
}

// resolveProjectLabels labels every buildable project the way `dexaflow
// deploy` would label it alone: --dag-version when given, else the
// content-derived default of that project; the git revision of the project's
// own directory, with --sha overriding the commit. A tag strategy that falls
// back is warned about per project. cluster is the deploy defaults layer the
// build applied (nil when none), so the label hashes the same resolved
// config a deploy hashes.
func resolveProjectLabels(cmd *cobra.Command, projects []Project, dagVersion, sha string, cluster *defaultsLayer) (map[string]projectLabel, error) {
	ctx := cmdContext(cmd)
	labels := make(map[string]projectLabel, len(projects))
	for _, p := range projects {
		if !hasRegistry(p) {
			continue
		}
		rev := readGitRevision(ctx, p.Path)
		if sha != "" {
			rev.sha = sha
		}
		version := dagVersion
		if version == "" {
			cfg, err := loadProjectConfig(p.Path)
			if err != nil {
				return nil, fmt.Errorf("labeling %s: %w", p.DagID, err)
			}
			// Hashed with the same defaults the build applied: with --push
			// that includes the cluster's, as a deploy to the same server
			// applies them, so `build --push` and `deploy --skip-build` agree
			// on the label. Without --push the cluster is not asked, and a
			// cluster that advertises platforms or a base image gives the
			// deploy a different label.
			if _, err = applyDeployDefaults(cmd, p.Path, cfg, cluster); err != nil {
				return nil, fmt.Errorf("labeling %s: %w", p.DagID, err)
			}
			version, err = defaultDAGVersion(ctx, p.Path, cfg, "Dockerfile", filepath.Join(p.Path, "dag.json"))
			if err != nil {
				return nil, fmt.Errorf("labeling %s: %w", p.DagID, err)
			}
		}
		if msg := tagStrategyFallback(p.Config.Registry.TagStrategy, rev); msg != "" {
			//nolint:errcheck // a warning that cannot be delivered must not fail the build
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %s\n", p.DagID, msg)
		}
		labels[p.Path] = projectLabel{version: version, rev: rev}
	}
	return labels, nil
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
		rebuild    bool
		builder    string
		dagVersion string
		sha        string
		dryRun     bool
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
			// The registry, platforms, base image and remote builder deploy
			// would use. Only
			// --push asks the server a deploy would go to for its deploy
			// defaults (buildClusterLayer); a build that stays local never
			// calls a server.
			var cluster *defaultsLayer
			if push {
				cluster = buildClusterLayer(cmd)
			}
			for _, p := range spec {
				if p.Config == nil {
					continue
				}
				if _, aerr := applyDeployDefaults(cmd, p.Path, p.Config, cluster); aerr != nil {
					return aerr
				}
			}
			// Resolve exactly as `dexaflow deploy` does before deriving the
			// reference. Handing the raw flags to deployImageRef means the
			// default tag strategy resolves against two empty strings and emits
			// `<url>/<name>:` — a reference docker refuses, from a command whose
			// entire purpose is to stop references being got wrong by hand.
			labels, lerr := resolveProjectLabels(cmd, spec, dagVersion, sha, cluster)
			if lerr != nil {
				return lerr
			}
			targets, skipped := buildTargets(spec, func(p Project) projectLabel { return labels[p.Path] })
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
			opts := func(t buildTarget) compileOptions {
				return compileOptions{
					cluster: cluster,
					output:  filepath.Join(t.dir, "dag.json"),
					image:   t.image,
					build:   true,
					push:    push,
					rebuild: rebuild,
					// Named explicitly: an empty dockerfile makes
					// ensureDockerfile stat the project directory, find it, and
					// hand docker a directory as -f — so the generated
					// Dockerfile, the default path for almost every project, is
					// never produced.
					dockerfile: "Dockerfile",
					builder:    builder,
					// The label the image reference was derived from, so the
					// dag.json and the tag cannot disagree.
					dagVersion: t.version,
				}
			}
			// Every project is checked before any is built: an invalid
			// dexaflow.yaml in the fifth project must not cost four builds
			// first, which is the half-built workspace stopping on failure
			// exists to avoid (#1121).
			if verr := preflightTargets(cmd, targets, opts); verr != nil {
				return verr
			}
			if dryRun {
				printBuildPlan(out, targets)
				return nil
			}
			for i, t := range targets {
				//nolint:errcheck // informational
				fmt.Fprintf(out, "▸ %s → %s\n", t.dagID, t.image)
				if rerr := runCompile(cmd, t.dir, opts(t)); rerr != nil {
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
			printBuildSummary(out, len(targets), skipped)
			return nil
		},
	}
	cmd.Flags().BoolVar(&push, "push", false, "push each built image to its registry")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "check every project and print the image each would be built as, then stop without building")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "build each dependency image again even when the registry holds one for its key (with --push)")
	cmd.Flags().String("registry", "", registryFlagUsage)
	cmd.Flags().StringVar(&builder, "builder", "docker", "image build tool to shell out to (e.g. docker, podman, nerdctl)")
	cmd.Flags().StringVar(&dagVersion, "dag-version", "", "version recorded in each dag.json and used by the registry tag strategy (default: per project, git describe of its directory, or dev outside git, plus a hash of its content)")
	cmd.Flags().StringVar(&sha, "sha", "", "commit sha for the git_sha tag strategy (default: git rev-parse --short HEAD in each project's directory)")
	addPlatformFlag(cmd)
	return cmd
}

// preflightTargets loads each project's config the way its compile will and
// reports the first that would be refused, naming the project.
func preflightTargets(cmd *cobra.Command, targets []buildTarget, opts func(buildTarget) compileOptions) error {
	for _, t := range targets {
		cfg, _, err := loadCompileConfig(cmd, t.dir, opts(t))
		if err == nil {
			err = validateProject(t.dir, cfg)
		}
		if err != nil {
			return fmt.Errorf("checking %s before building: %w", t.dagID, err)
		}
	}
	return nil
}

// printBuildSummary closes a workspace build. The skipped list is repeated on
// purpose: it was printed before minutes of builder output, and a closing
// "built N" alone reads as "the workspace is built".
func printBuildSummary(out io.Writer, built int, skipped []string) {
	if len(skipped) > 0 {
		//nolint:errcheck // informational
		fmt.Fprintf(out, "built %d image(s), skipped %d: %s\n", built, len(skipped), strings.Join(skipped, "; "))
		return
	}
	//nolint:errcheck // informational
	fmt.Fprintf(out, "built %d image(s)\n", built)
}

// printBuildPlan is --dry-run's output: each dag id with its image.
func printBuildPlan(out io.Writer, targets []buildTarget) {
	for _, t := range targets {
		//nolint:errcheck // informational
		fmt.Fprintf(out, "%s → %s\n", t.dagID, t.image)
	}
	//nolint:errcheck // informational
	fmt.Fprintln(out, "dry run: nothing built")
}

// builtIDs lists the dag ids of targets already built, for the failure summary.
func builtIDs(done []buildTarget) []string {
	ids := make([]string, 0, len(done))
	for _, t := range done {
		ids = append(ids, t.dagID)
	}
	return ids
}
