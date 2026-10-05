package storage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// encryptingWritePaths maps every repository function that seals a value with
// the secret cipher to the columns it writes. A key migration can only drop a
// predecessor safely if it moved EVERY column a predecessor could have sealed,
// so a new encrypted field (Variables, #507) that is not in the registry would
// be left under a key the migration then deletes (ADR 0065 section 7).
//
// Adding an encrypting write path fails TestEveryEncryptedColumnIsInTheRegistry
// until it is listed here AND its columns are in encryptedColumns.
var encryptingWritePaths = map[string][]string{
	"SetConnection":      {"connections.password", "connections.extra"},
	"SetConnectionPatch": {"connections.password", "connections.extra"},
}

// keyMachinery are functions that encrypt only to move existing ciphertext
// between keys, over the registry itself.
var keyMachinery = map[string]bool{
	"encOrEmpty": true, "encPatch": true, // helpers of the write paths above
	"reencryptOne": true, "ReencryptSecrets": true, // the Pro boot sweep
	"MigrateSecretValues": true, "moveValue": true, // the Lite key migration
}

func TestEveryEncryptedColumnIsInTheRegistry(t *testing.T) {
	callers := encryptCallers(t)
	registry := map[string]bool{}
	for _, c := range EncryptedColumns() {
		registry[c.String()] = true
	}
	for _, fn := range callers {
		if keyMachinery[fn] {
			continue
		}
		cols, ok := encryptingWritePaths[fn]
		if !ok {
			t.Errorf("%s seals a value with the secret cipher but is not in encryptingWritePaths: "+
				"add the columns it writes there and to encryptedColumns, or a key migration will drop the key they need", fn)
			continue
		}
		for _, c := range cols {
			if !registry[c] {
				t.Errorf("%s writes %s, which is missing from encryptedColumns", fn, c)
			}
		}
	}
	for fn := range encryptingWritePaths {
		if !contains(callers, fn) {
			t.Errorf("encryptingWritePaths lists %s, which no longer encrypts: keep the list honest", fn)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// encryptCallers returns every function in this package (tests excluded) that
// calls an Encrypt method, directly or through another function of the
// package that does.
func encryptCallers(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]map[string]bool{} // func -> names it calls
	direct := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatal(perr)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := fd.Name.Name
			calls[fn] = map[string]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				ce, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := ce.Fun.(type) {
				case *ast.SelectorExpr:
					if f.Sel.Name == "Encrypt" {
						direct[fn] = true
					}
					calls[fn][f.Sel.Name] = true
				case *ast.Ident:
					calls[fn][f.Name] = true
				}
				return true
			})
		}
	}
	reach := map[string]bool{}
	for fn := range direct {
		reach[fn] = true
	}
	for changed := true; changed; {
		changed = false
		for fn, callees := range calls {
			if reach[fn] {
				continue
			}
			for c := range callees {
				if reach[c] {
					reach[fn], changed = true, true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(reach))
	for fn := range reach {
		out = append(out, fn)
	}
	sort.Strings(out)
	return out
}
