package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// platformFlag is the flag compile, build and deploy share to choose the
// image platform.
const platformFlag = "platform"

// addPlatformFlag registers --platform on a command that builds an image.
func addPlatformFlag(cmd *cobra.Command) {
	cmd.Flags().StringSlice(platformFlag, nil,
		"image platform to build for, os/arch (repeatable or comma-separated), over build.platforms in dexaflow.yaml, the target and the workspace defaults (default linux/amd64)")
}

// platformsFromFlag returns the --platform values, validated, and whether the
// flag was given. A command without the flag (Lite) never has it set.
func platformsFromFlag(cmd *cobra.Command) (platforms []string, set bool, err error) {
	f := cmd.Flags().Lookup(platformFlag)
	if f == nil || !f.Changed {
		return nil, false, nil
	}
	values, err := cmd.Flags().GetStringSlice(platformFlag)
	if err != nil {
		return nil, false, fmt.Errorf("reading --%s: %w", platformFlag, err)
	}
	platforms, err = build.ParsePlatforms(values)
	if err != nil {
		return nil, false, fmt.Errorf("--%s: %w", platformFlag, err)
	}
	return platforms, true, nil
}

// applyPlatformFlag puts --platform over every other source of
// build.platforms and returns the source to report.
func applyPlatformFlag(cmd *cobra.Command, cfg *domain.LeoflowConfig, source string) (string, error) {
	platforms, set, err := platformsFromFlag(cmd)
	if err != nil || !set {
		return source, err
	}
	if cfg.Build == nil {
		cfg.Build = &domain.BuildConfig{}
	}
	cfg.Build.Platforms = platforms
	return "--" + platformFlag, nil
}

// checkPlatforms validates the resolved build.platforms whatever their
// source, so a malformed value from dexaflow.yaml, a target or the workspace
// defaults is refused, with its source named, before anything is parsed,
// built or sent. An empty list selects the tool's default and is left alone.
func checkPlatforms(cfg *domain.LeoflowConfig, source string) error {
	platforms := buildPlatforms(cfg)
	if len(platforms) == 0 {
		return nil
	}
	if _, err := build.ParsePlatforms(platforms); err != nil {
		return fmt.Errorf("build.platforms (from %s): %w", orDefault(source), err)
	}
	return nil
}

// buildPlatforms returns the platforms a project builds for.
func buildPlatforms(cfg *domain.LeoflowConfig) []string {
	if cfg.Build == nil {
		return nil
	}
	return cfg.Build.Platforms
}

// selectBuilder resolves the builder for a compile that builds image from
// the project in dir, in the order of ADR 0073, and says why. Only deploy
// allows the skip path (compileOptions.reuse); a pushed build of the
// generated Dockerfile may split into a dependency image and a code layer
// (splitFor, ADR 0077); a build no daemonless path covers goes to the
// remote builder the project or its target configured (remoteFor, ADR 0083).
func selectBuilder(ctx context.Context, dir, dockerfile string, o compileOptions, cfg *domain.LeoflowConfig, image string) (b build.Builder, reason string) {
	split, noSplit := splitFor(ctx, dir, dockerfile, o, cfg, image)
	opts := build.Options{Tool: o.builder, Reuse: o.reuse, Split: split, NoSplit: noSplit, Rebuild: o.rebuild,
		Registry: registryFor(cfg), Push: o.push, Remote: remoteFor(cfg), RemoteSource: o.remoteSource}
	return build.Select(ctx, opts, image, buildPlatforms(cfg))
}

// remoteFor is the remote builder build.remote_builder configures, reading
// the pushed digest with the project's registry client, or nil when none is
// configured.
func remoteFor(cfg *domain.LeoflowConfig) build.Remote {
	if cfg.Build == nil || cfg.Build.RemoteBuilder == nil || cfg.Build.RemoteBuilder.CloudBuild == nil {
		return nil
	}
	cb := cfg.Build.RemoteBuilder.CloudBuild
	return build.CloudBuild{Project: cb.Project, Region: cb.Region, WorkerPool: cb.WorkerPool, ServiceAccount: cb.ServiceAccount,
		Registry: registryFor(cfg)}
}

// printBuildSettings says what a build is about to do: the platforms and
// where they came from (unless the caller printed them already, as deploy
// does), and the builder path with the reason it was chosen.
func printBuildSettings(out io.Writer, o compileOptions, cfg *domain.LeoflowConfig, b build.Builder, reason string) error {
	var s strings.Builder
	if !o.settingsShown {
		fmt.Fprintf(&s, "platforms:  %s (from %s)\n", strings.Join(buildPlatforms(cfg), ","), orDefault(o.platformsSource))
	}
	fmt.Fprintf(&s, "builder:    %s, %s\n", b.Describe(), reason)
	_, err := io.WriteString(out, s.String())
	if err != nil {
		return fmt.Errorf("writing the build settings: %w", err)
	}
	return nil
}
