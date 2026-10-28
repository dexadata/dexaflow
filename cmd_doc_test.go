package dexaflow

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCmdPackageDocComments asserts that every command entrypoint under cmd/
// carries a package doc comment starting with "Command <dir> " and ending with
// a period (ADR 0012), so pkg.go.dev indexes the canonical dexaflow* names.
func TestCmdPackageDocComments(t *testing.T) {
	entries, err := os.ReadDir("cmd")
	if err != nil {
		t.Fatalf("reading cmd dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("cmd/ has no subdirectories")
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			mainPath := filepath.Join("cmd", name, "main.go")
			f, perr := parser.ParseFile(fset, mainPath, nil, parser.PackageClauseOnly|parser.ParseComments)
			if perr != nil {
				t.Fatalf("parsing %s: %v", mainPath, perr)
			}
			if f.Doc == nil {
				t.Fatalf("%s has no package doc comment", mainPath)
			}
			doc := strings.TrimSpace(f.Doc.Text())
			wantPrefix := "Command " + name + " "
			if !strings.HasPrefix(doc, wantPrefix) {
				t.Errorf("%s package doc = %q; want prefix %q", mainPath, doc, wantPrefix)
			}
			if !strings.HasSuffix(doc, ".") {
				t.Errorf("%s package doc = %q; want trailing period", mainPath, doc)
			}
			if strings.Contains(doc, "\u2014") {
				t.Errorf("%s package doc contains an em dash: %q", mainPath, doc)
			}
		})
	}
}
