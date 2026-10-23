package setup

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestExtractFS(t *testing.T) {
	src := fstest.MapFS{
		"parser/pyproject.toml":             {Data: []byte("[project]\nname='leoflow-parser'\n")},
		"parser/leoflow_parser/__init__.py": {Data: []byte("")},
		"parser/leoflow_parser/cli.py":      {Data: []byte("print('hi')\n")},
		"runtime/python/pyproject.toml":     {Data: []byte("[project]\nname='leoflow-runtime'\n")},
	}
	dest := t.TempDir()
	if err := ExtractFS(src, dest); err != nil {
		t.Fatalf("ExtractFS err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "parser", "leoflow_parser", "cli.py"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != "print('hi')\n" {
		t.Errorf("cli.py = %q, want the source", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "runtime", "python", "pyproject.toml")); err != nil {
		t.Errorf("runtime pyproject not extracted: %v", err)
	}
}
