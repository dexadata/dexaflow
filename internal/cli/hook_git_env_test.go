package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tests of this package run git in temp dirs, directly and through the
// CLI, and the shell scripts they run do too. None of them may follow a
// GIT_DIR, GIT_INDEX_FILE or other repository override inherited from a git
// hook to the repository being committed to, so the process drops them
// before any test starts.
func init() {
	for _, kv := range gitRepoOverrides {
		_ = os.Unsetenv(strings.TrimSuffix(kv, "=")) //nolint:errcheck // fails only on an invalid name
	}
}

// TestGitTestsLeaveTheHookRepositoryAlone runs this package's git tests the
// way the pre-commit hook does: `git commit` exports GIT_DIR (in a worktree)
// and GIT_INDEX_FILE to the hook, and `go test ./...` inherits them. Every
// `git init` and `git commit` those tests run in a temp dir then lands in the
// repository being committed to; a contributor's checkout once ended up with
// core.bare = true. A scratch repository stands in for that checkout here
// and must come out unchanged.
func TestGitTestsLeaveTheHookRepositoryAlone(t *testing.T) {
	t.Parallel()
	requireGit(t)
	scratch := t.TempDir()
	gitIn(t, scratch, "init", "-q")
	writeProjectFile(t, scratch, "README.md", "# scratch\n")
	gitIn(t, scratch, "add", "-A")
	gitIn(t, scratch, "commit", "-q", "-m", "scratch")
	before := scratchState(t, scratch)

	gitDir := filepath.Join(scratch, ".git")
	child := exec.CommandContext(t.Context(), os.Args[0], //nolint:gosec // re-runs this test binary
		"-test.count=1", "-test.run=^(TestGitRootOf|TestDefaultDAGVersionDirtyTree|TestCIScriptDeploysWhatChanged|TestDeployByDagIDUsesTheGitRootWithoutAWorkspaceFile)$")
	child.Dir = t.TempDir()
	// The two variables a hook in a worktree receives (see githooks(5)).
	child.Env = append(os.Environ(), "GIT_DIR="+gitDir, "GIT_INDEX_FILE="+filepath.Join(gitDir, "index"))
	out, err := child.CombinedOutput()
	if after := scratchState(t, scratch); after != before {
		t.Errorf("the hook's repository changed:\nbefore: %s\nafter:  %s", before, after)
	}
	if err != nil {
		t.Errorf("the git tests failed under the hook's environment: %v\n%s", err, out)
	}
}

// scratchState is what a test run must not change in a repository: whether
// it is bare, its commits and its index.
func scratchState(t *testing.T, dir string) string {
	t.Helper()
	c := exec.CommandContext(t.Context(), "git", "-C", dir, "config", "core.bare")
	c.Env = gitEnv(os.Environ())
	bare, _ := c.Output() //nolint:errcheck // empty when unset
	return strings.Join([]string{
		"bare=" + strings.TrimSpace(string(bare)),
		"commits=" + gitIn(t, dir, "rev-list", "--all"),
		"index=" + gitIn(t, dir, "ls-files", "--stage"),
	}, " ")
}
