package cli

import (
	"strings"
	"testing"
)

func TestComposeImageRef(t *testing.T) {
	t.Parallel()
	if got := composeImageRef("ghcr.io/org", "etl", "abc"); got != "ghcr.io/org/etl:abc" {
		t.Errorf("ref = %q, want ghcr.io/org/etl:abc", got)
	}
	// A trailing slash on the URL must not double up.
	if got := composeImageRef("ghcr.io/org/", "etl", "abc"); got != "ghcr.io/org/etl:abc" {
		t.Errorf("ref = %q, want a single slash join", got)
	}
}

func TestResolveImageTagByStrategy(t *testing.T) {
	t.Parallel()
	inGit := gitRevision{sha: "sha9", exactTag: "v1.0+rc/1"}
	noGit := gitRevision{}
	untagged := gitRevision{sha: "sha9"}
	cases := []struct {
		strategy, version string
		rev               gitRevision
		want              string
	}{
		{"version", "v1", inGit, "v1"},
		// The label lands in the tag, so it is made a valid tag.
		{"version", "release/v1+b", inGit, "release-v1-b"},
		{"git_sha", "v1", inGit, "sha9"},
		{"git-sha", "v1", inGit, "sha9"},
		// git_sha outside git used to produce `repo:` with an empty tag.
		{"git_sha", "v1", noGit, "v1"},
		{"git_tag", "v1", inGit, "v1.0-rc-1"},
		{"git_tag", "v1", untagged, "v1"},
		{"git_tag", "v1", noGit, "v1"},
		// custom tags with the label the caller chose with --dag-version.
		{"custom", "v1", inGit, "v1"},
		// Values outside the schema enum (a config built before validation, or
		// "sha" in an old file) keep their earlier meaning: the sha, else the
		// version label.
		{"", "v1", inGit, "sha9"},
		{"sha", "v1", inGit, "sha9"},
		{"sha", "v1", noGit, "v1"},
	}
	for _, c := range cases {
		if got := resolveImageTag(c.strategy, c.version, c.rev); got != c.want {
			t.Errorf("resolveImageTag(%q,%q,%+v) = %q, want %q",
				c.strategy, c.version, c.rev, got, c.want)
		}
	}
}

func TestTagStrategyFallbackWarning(t *testing.T) {
	t.Parallel()
	cases := []struct {
		strategy string
		rev      gitRevision
		warns    bool
	}{
		{"git_sha", gitRevision{}, true},
		{"git_sha", gitRevision{sha: "abc"}, false},
		{"git_tag", gitRevision{sha: "abc"}, true},
		{"git_tag", gitRevision{sha: "abc", exactTag: "v1"}, false},
		{"version", gitRevision{}, false},
		{"custom", gitRevision{}, false},
	}
	for _, c := range cases {
		got := tagStrategyFallback(c.strategy, c.rev)
		if (got != "") != c.warns {
			t.Errorf("tagStrategyFallback(%q, %+v) = %q, want warning=%v", c.strategy, c.rev, got, c.warns)
		}
		if c.warns && !strings.Contains(got, c.strategy) {
			t.Errorf("the warning must name the strategy %q: %q", c.strategy, got)
		}
	}
}
