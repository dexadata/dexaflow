package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newTestFS roots an FS at a fresh temp dir seeded with a small tree.
func newTestFS(t *testing.T) (fs *FS, root string) {
	t.Helper()
	root = t.TempDir()
	mustWrite(t, filepath.Join(root, "dag.py"), "print('hi')\n")
	mustWrite(t, filepath.Join(root, "dexaflow.yaml"), "name: demo\n")
	if err := os.MkdirAll(filepath.Join(root, "tasks"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustWrite(t, filepath.Join(root, "tasks", "extract.py"), "x = 1\n")
	var err error
	fs, err = New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return fs, root
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestNewRejectsNonDirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "f.txt")
	mustWrite(t, file, "x")
	if _, err := New(file); err == nil {
		t.Fatal("New on a file should fail")
	}
	if _, err := New(filepath.Join(root, "missing")); err == nil {
		t.Fatal("New on a missing path should fail")
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	fs, _ := newTestFS(t)
	if err := fs.Write("dag.py", []byte("print('bye')\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := fs.Read("dag.py")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "print('bye')\n" {
		t.Fatalf("round-trip = %q", got)
	}
}

func TestWriteCreatesParentDirs(t *testing.T) {
	fs, root := newTestFS(t)
	if err := fs.Write("nested/deep/new.py", []byte("ok")); err != nil {
		t.Fatalf("Write nested: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "nested", "deep", "new.py")); err != nil {
		t.Fatalf("nested file not created: %v", err)
	}
}

func TestTreeListsFilesRelativeAndSorted(t *testing.T) {
	fs, _ := newTestFS(t)
	entries, err := fs.Tree()
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	joined := strings.Join(paths, ",")
	for _, want := range []string{"dag.py", "dexaflow.yaml", "tasks", "tasks/extract.py"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Tree missing %q; got %v", want, paths)
		}
	}
	// Paths must be slash-separated and relative (never absolute, never with "..").
	for _, p := range paths {
		if filepath.IsAbs(p) || strings.Contains(p, "..") || strings.Contains(p, "\\") {
			t.Errorf("Tree returned unsafe path %q", p)
		}
	}
}

func TestTreeSkipsNoiseDirs(t *testing.T) {
	fs, root := newTestFS(t)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	mustWrite(t, filepath.Join(root, ".git", "config"), "x")
	entries, err := fs.Tree()
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Path, ".git") {
			t.Errorf("Tree should skip .git, got %q", e.Path)
		}
	}
}

func TestCreateFileAndDir(t *testing.T) {
	fs, root := newTestFS(t)
	if err := fs.Create("newdir", true); err != nil {
		t.Fatalf("Create dir: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, "newdir")); err != nil || !fi.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
	if err := fs.Create("newdir/file.py", false); err != nil {
		t.Fatalf("Create file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "newdir", "file.py")); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestDeleteRemoves(t *testing.T) {
	fs, root := newTestFS(t)
	if err := fs.Delete("tasks/extract.py"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "extract.py")); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
}

// TestTraversalRejected is the security core: every operation must refuse paths
// that escape the workspace root, whether via "..", an absolute path, or
// embedded traversal segments.
func TestTraversalRejected(t *testing.T) {
	fs, _ := newTestFS(t)
	bad := []string{
		"../escape.py",
		"../../etc/passwd",
		"tasks/../../escape.py",
		"./../../escape.py",
		"a/b/../../../escape.py",
	}
	if runtime.GOOS != "windows" {
		bad = append(bad, "/etc/passwd", "/tmp/abs.py")
	}
	for _, p := range bad {
		if _, err := fs.Read(p); err == nil {
			t.Errorf("Read(%q) should be rejected", p)
		}
		if err := fs.Write(p, []byte("x")); err == nil {
			t.Errorf("Write(%q) should be rejected", p)
		}
		if err := fs.Create(p, false); err == nil {
			t.Errorf("Create(%q) should be rejected", p)
		}
		if err := fs.Delete(p); err == nil {
			t.Errorf("Delete(%q) should be rejected", p)
		}
	}
}

// TestEmptyPathRejected guards against operating on the root itself.
func TestEmptyPathRejected(t *testing.T) {
	fs, _ := newTestFS(t)
	for _, p := range []string{"", ".", "/"} {
		if _, err := fs.Read(p); err == nil {
			t.Errorf("Read(%q) should be rejected", p)
		}
		if err := fs.Delete(p); err == nil {
			t.Errorf("Delete(%q) should be rejected", p)
		}
	}
}

func TestReadMissingFile(t *testing.T) {
	fs, _ := newTestFS(t)
	if _, err := fs.Read("nope.py"); err == nil {
		t.Fatal("Read of a missing file should error")
	}
}

// TestMoveRenamesFiles pins the contract for the IDE's "rename / drag-drop"
// surface: moving a file or directory updates the path, the source disappears,
// the destination matches what was there before, and the operation is confined
// to the workspace root just like Read/Write/Create.
func TestMoveRenamesFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.py"), []byte("print('hi')"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "inner.txt"), []byte("inner"), 0o600); err != nil {
		t.Fatalf("seed inner: %v", err)
	}
	fs, ferr := New(root)
	if ferr != nil {
		t.Fatalf("New: %v", ferr)
	}

	// Rename a file in-place.
	if mErr := fs.Move("old.py", "new.py"); mErr != nil {
		t.Fatalf("rename file: %v", mErr)
	}
	if _, sErr := os.Stat(filepath.Join(root, "old.py")); !os.IsNotExist(sErr) {
		t.Errorf("old.py should be gone, got err=%v", sErr)
	}
	got, err := os.ReadFile(filepath.Join(root, "new.py"))
	if err != nil || string(got) != "print('hi')" {
		t.Errorf("new.py = (%q, %v), want (\"print('hi')\", nil)", got, err)
	}

	// Move a file into a different folder (auto-create parents).
	if err := fs.Move("new.py", "scripts/new.py"); err != nil {
		t.Fatalf("move into dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "new.py")); err != nil {
		t.Errorf("scripts/new.py missing: %v", err)
	}

	// Move a whole directory (file inside follows).
	if err := fs.Move("sub", "moved_sub"); err != nil {
		t.Fatalf("move dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "moved_sub", "inner.txt")); err != nil {
		t.Errorf("moved_sub/inner.txt missing: %v", err)
	}
}

// TestMoveRefusesUnsafePaths confines Move to the workspace, mirroring
// Read/Write/Create. Without this, a drag-drop bug in the UI could overwrite
// arbitrary files on the host.
func TestMoveRefusesUnsafePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fs, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, p := range []string{"../escape.txt", "/etc/passwd", "../../host.txt"} {
		if err := fs.Move("a.txt", p); err == nil {
			t.Errorf("Move to %q must be rejected", p)
		}
		if err := fs.Move(p, "ok.txt"); err == nil {
			t.Errorf("Move from %q must be rejected", p)
		}
	}
}

// TestMoveRefusesOverwriteExisting prevents silent data loss: a drag-drop
// that lands on an existing file/folder must fail loudly rather than
// clobber. The IDE surface should ask the user to confirm/rename instead.
func TestMoveRefusesOverwriteExisting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatalf("seed b: %v", err)
	}
	fs, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := fs.Move("a.txt", "b.txt"); err == nil {
		t.Errorf("Move onto existing path must fail")
	}
}
