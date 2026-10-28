package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/dexadata/dexaflow/internal/build"
)

// testDeps are the dependencies the tests of this package run commands
// with: the production ones, except that no public registry is asked for a
// base image (one off the loopback is refused, so a build splits, ADR 0077,
// only when a test serves the base image itself) and uv is not there unless
// a test installs a fake one (fakeUV), so the host's uv decides nothing.
// Each call returns a new set, which a test changes for itself alone.
func testDeps() *cliDeps {
	d := productionDeps()
	d.baseReader = func(r build.Registry) baseReader { return loopbackRegistry{reg: r} }
	d.lookUV = func() (string, error) { return "", errors.New("uv is not installed in the tests") }
	return d
}

// testCtx is a context carrying d, or testDeps when d is nil.
func testCtx(d *cliDeps) context.Context {
	if d == nil {
		d = testDeps()
	}
	return withDeps(context.Background(), d)
}

// runDeps runs the root command with args under d and returns what it
// printed.
func runDeps(t *testing.T, d *cliDeps, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err = root.ExecuteContext(testCtx(d))
	return out.String(), errb.String(), err
}
