package agentrpc_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// errorish matches the names Go convention gives error values — err, rerr,
// verr, perr, cerr, oerr, werr, hbErr, rl.err — a convention golangci's
// error-naming rule already enforces in this repo. It is deliberately narrow:
// it matches how errors are NAMED, not how they are used.
var errorish = regexp.MustCompile(`(?i)^[a-z0-9_]*err(or)?$`)

// statusConstructors are the ways a gRPC status is built. Every one of them
// puts its message on the wire to the task pod.
var statusConstructors = map[string]bool{"Error": true, "Errorf": true, "New": true, "Newf": true}

// TestNoErrorValueIsInterpolatedIntoAnAgentStatus is the source-level tripwire
// for the reintroduction of #1068: a `status.Errorf(codes.Internal, "doing X:
// %v", err)` anywhere in this package puts a driver error's text — SQLSTATE,
// constraint, table, column, or a connection string — into a message returned
// to the tenant's own pod.
//
// It complements, and does not replace, TestAgentRPCStatusesCarryNoStorageErrorText:
// that one asserts on what a real client receives and so catches a leak through
// any mechanism, including a bare error returned from a handler and rendered by
// grpc-go as Unknown. This one names the offending file and line the moment the
// pattern reappears, and it reaches the two AwaitAssignment sites whose error is
// a transport failure a client cannot force.
//
// internalStatus and peerStatus are the only sanctioned ways to turn an error
// into a status here; their parameter is deliberately named cause, not err,
// because the value it carries is destined for the log and never for the wire.
//
// What this guard cannot see (#1078), so neither it nor the pair is exhaustive:
//   - It is name-based. An error value named e, problem or cause passes it, and
//     a status package imported under another alias evades it entirely.
//   - It cannot see a bare error returned from a handler, which grpc-go renders
//     as codes.Unknown with err.Error() as the message. Only the behavioural
//     sweep catches that.
//   - The sweep in turn covers the collaborator seams it poisons today. A new
//     handler behind a new seam arrives with a fake nobody poisons, so it is
//     caught by neither. Adding a seam means adding a case to
//     TestAgentRPCStatusesCarryNoStorageErrorText.
func TestNoErrorValueIsInterpolatedIntoAnAgentStatus(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	constructors := 0
	files := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		files++
		constructors += inspectStatusCalls(t, fset, file)
	}
	if files == 0 {
		t.Fatal("no non-test sources parsed; this guard would pass vacuously")
	}
	if constructors == 0 {
		t.Fatal("no status constructor found in the package; the guard is not watching anything")
	}
}

// inspectStatusCalls reports every error-named value handed to a gRPC status
// constructor in file, and returns how many constructors it examined so the
// caller can prove the guard was not vacuous.
func inspectStatusCalls(t *testing.T, fset *token.FileSet, file *ast.File) int {
	t.Helper()
	seen := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "status" || !statusConstructors[sel.Sel.Name] {
			return true
		}
		seen++
		for _, arg := range call.Args {
			if name, found := errorishOperand(arg); found {
				t.Errorf("%s: status.%s interpolates the error value %q into a message the task pod receives; route it through internalStatus/peerStatus so the cause stays in the control-plane log (#1068)",
					fset.Position(call.Pos()), sel.Sel.Name, name)
			}
		}
		return true
	})
	return seen
}

// errorishOperand reports the first error-named identifier or field reachable
// from an argument expression, so both `err` and `rl.err` are caught.
func errorishOperand(expr ast.Expr) (string, bool) {
	var name string
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if errorish.MatchString(v.Sel.Name) {
				name, found = v.Sel.Name, true
				return false
			}
		case *ast.Ident:
			if errorish.MatchString(v.Name) {
				name, found = v.Name, true
				return false
			}
		}
		return true
	})
	return name, found
}
