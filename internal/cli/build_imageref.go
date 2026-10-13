package cli

import (
	"fmt"
	"strings"
)

// composeImageRef builds the pushed image reference `<url>/<image_name>:<tag>`
// from the registry config, tolerating a trailing slash on the URL.
func composeImageRef(url, imageName, tag string) string {
	return fmt.Sprintf("%s/%s:%s", strings.TrimRight(url, "/"), imageName, tag)
}

// resolveImageTag picks the image tag from the registry's tag strategy. The
// result is always a valid image tag, and never empty while version is not.
//
//   - version (the default dexaflow.yaml applies) and an unset value: the DAG
//     version label.
//   - git_sha (or git-sha): the project's short commit sha; outside git, the
//     version label, where an empty sha used to yield `repo:` with no tag.
//   - git_tag: the git tag on the project's HEAD; without one, the version
//     label.
//   - custom: the version label, which is the tag the caller chose with
//     --dag-version.
//   - any other value, which the schema refuses but a config built in code or
//     an old file may still carry (an unset value, "sha"): the sha, else the
//     version label, as every value outside version did before.
//
// tagStrategyFallback says when git_sha or git_tag fell back.
func resolveImageTag(strategy, version string, rev gitRevision) string {
	switch strategy {
	case "version", "custom":
	case "git_tag":
		if tag := sanitizeImageTag(rev.exactTag); tag != "" {
			return tag
		}
	default:
		if rev.sha != "" {
			return sanitizeImageTag(rev.sha)
		}
	}
	return sanitizeImageTag(version)
}

// tagStrategyFallback returns a warning when the tag strategy could not be
// honored for this revision and resolveImageTag used the version label instead,
// or "" when it was honored.
func tagStrategyFallback(strategy string, rev gitRevision) string {
	switch {
	case (strategy == "git_sha" || strategy == "git-sha") && rev.sha == "":
		return "tag_strategy " + strategy + ": the project is not in a git repository; tagging the image with the version label instead"
	case strategy == "git_tag" && rev.exactTag == "":
		return "tag_strategy git_tag: no git tag on the project's HEAD; tagging the image with the version label instead"
	}
	return ""
}
