package cli

import (
	"context"
	"net/url"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/version"
)

// collectBuildInfo is where a register of the project at dir comes from
// (#1702): the commit the project's git work tree is on, its repository (the
// origin remote, else the CI's own variable), the CI pipeline run read from
// the well-known variables of GitHub Actions, GitLab CI, Jenkins, Buildkite
// and CircleCI, and this CLI's version. getenv is os.Getenv outside tests.
// What cannot be read is left empty, and a value the server would refuse is
// dropped (domain.BuildInfo.Sanitized), so build info never fails a deploy.
// The builder and the base image are added by the build.
func collectBuildInfo(ctx context.Context, dir string, getenv func(string) string) domain.BuildInfo {
	b := domain.BuildInfo{
		Commit:      gitOutput(ctx, dir, "rev-parse", "HEAD"),
		Repo:        browsableRemote(gitOutput(ctx, dir, "remote", "get-url", "origin")),
		PipelineURL: withoutQuery(ciPipelineURL(getenv)),
		CLIVersion:  version.Get().Version,
	}
	if b.Repo == "" {
		b.Repo = withoutQuery(ciRepoURL(getenv))
	}
	return b.Sanitized()
}

// ciRepoURL is the repository URL the CI names, "" outside a known CI.
func ciRepoURL(getenv func(string) string) string {
	if server, repo := getenv("GITHUB_SERVER_URL"), getenv("GITHUB_REPOSITORY"); server != "" && repo != "" {
		return strings.TrimRight(server, "/") + "/" + repo
	}
	return getenv("CI_PROJECT_URL")
}

// ciPipelineURL is the URL of the CI run, "" outside a known CI: GitHub
// Actions (the workflow run), GitLab CI (CI_PIPELINE_URL), Jenkins
// (BUILD_URL), Buildkite (BUILDKITE_BUILD_URL) and CircleCI
// (CIRCLE_BUILD_URL), in that order.
func ciPipelineURL(getenv func(string) string) string {
	if repo := ciRepoURL(getenv); repo != "" && getenv("GITHUB_RUN_ID") != "" {
		return repo + "/actions/runs/" + getenv("GITHUB_RUN_ID")
	}
	for _, name := range []string{"CI_PIPELINE_URL", "BUILD_URL", "BUILDKITE_BUILD_URL", "CIRCLE_BUILD_URL"} {
		if v := getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// withoutQuery is v without its query string and fragment: a CI variable can
// carry one (a token in a Jenkins URL), which the server refuses and which
// does not name the run or the repository.
func withoutQuery(v string) string {
	if i := strings.IndexAny(v, "?#"); i >= 0 {
		return v[:i]
	}
	return v
}

// browsableRemote turns a git remote URL into the http or https URL of the
// repository: credentials and a trailing .git are dropped, and an ssh remote
// (git@host:path or ssh://[user@]host[:port]/path) becomes https://host/path.
// A local path, a file URL or anything else it cannot map is "".
func browsableRemote(remote string) string {
	if remote == "" || strings.ContainsAny(remote, " \t\r\n") {
		return ""
	}
	if user, rest, ok := strings.Cut(remote, "@"); ok && !strings.Contains(user, "/") && !strings.Contains(remote, "://") {
		host, path, found := strings.Cut(rest, ":")
		if !found || host == "" || path == "" {
			return ""
		}
		return "https://" + host + "/" + strings.TrimSuffix(strings.TrimPrefix(path, "/"), ".git")
	}
	u, err := url.Parse(remote)
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "http", "https":
	case "ssh", "git+ssh":
		u.Scheme, u.Host = "https", u.Hostname()
	default:
		return ""
	}
	u.User = nil
	u.Path = strings.TrimSuffix(u.Path, ".git")
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// addBuildInfo sets the build_* query parameters of a register from b, one
// per field that is set.
func addBuildInfo(q url.Values, b *domain.BuildInfo) {
	if b == nil {
		return
	}
	for name, v := range map[string]string{
		"build_commit": b.Commit, "build_repo": b.Repo, "build_pipeline_url": b.PipelineURL,
		"build_builder": b.Builder, "build_base_image": b.BaseImage, "build_cli_version": b.CLIVersion,
	} {
		if v != "" {
			q.Set(name, v)
		}
	}
}
