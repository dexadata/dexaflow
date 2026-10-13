package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDAGVersionHashesSymlinksWithoutFollowing(t *testing.T) {
	dir := versionProject(t, "")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	writeProjectFile(t, dir, "data/a.txt", "a\n")
	// A link to a directory and a dangling link both aborted every compile.
	if err := os.Symlink("data", filepath.Join(dir, "data-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	before := labelOf(t, dir)
	// The build copies the link, not its target: what it points at counts.
	if err := os.Remove(filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if after := labelOf(t, dir); after == before {
		t.Errorf("retargeting a symlink kept label %q", before)
	}
}

func TestDefaultDAGVersionNamesTheWayOutOfAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	dir := versionProject(t, "")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
	writeProjectFile(t, dir, "secret.txt", "x")
	if err := os.Chmod(filepath.Join(dir, "secret.txt"), 0); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = defaultDAGVersion(context.Background(), dir, cfg, "Dockerfile", "")
	if err == nil || !strings.Contains(err.Error(), "exclude_paths") || !strings.Contains(err.Error(), "--dag-version") {
		t.Errorf("err = %v, want the exclude_paths / --dag-version way out", err)
	}
}

func TestReadErrorHintNamesTheWayOut(t *testing.T) {
	err := hashReadError("p/secret.txt", os.ErrPermission)
	if !strings.Contains(err.Error(), "exclude_paths") || !strings.Contains(err.Error(), "--dag-version") || !strings.Contains(err.Error(), "p/secret.txt") {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultDAGVersionDbtShippedInputs(t *testing.T) {
	cases := []struct {
		name, yaml, prefix string
	}{
		{"project at the root", "dag_id: sales\ndbt:\n  project: \".\"\n  manifest: target/manifest.json\n", ""},
		{"project in a directory", "dag_id: sales\ndbt:\n  project: transform\n  manifest: target/manifest.json\n", "transform/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeProjectFile(t, dir, "dexaflow.yaml", c.yaml)
			writeProjectFile(t, dir, c.prefix+"dbt_project.yml", "name: sales\n")
			writeProjectFile(t, dir, c.prefix+"target/manifest.json", `{"v":1}`)
			before := labelOf(t, dir)
			// Rewritten by every `dbt parse`, so it must not move the label.
			writeProjectFile(t, dir, c.prefix+"target/partial_parse.msgpack", "x")
			writeProjectFile(t, dir, c.prefix+"logs/dbt.log", "x")
			if got := labelOf(t, dir); got != before {
				t.Fatalf("parse output moved the label: %q -> %q", before, got)
			}
			// The configured manifest is the DAG: it must count.
			writeProjectFile(t, dir, c.prefix+"target/manifest.json", `{"v":2}`)
			afterManifest := labelOf(t, dir)
			if afterManifest == before {
				t.Fatalf("a new dbt.manifest kept label %q", before)
			}
			// Anything else under target/ ships in the image.
			writeProjectFile(t, dir, c.prefix+"target/compiled/a.sql", "select 1")
			if got := labelOf(t, dir); got == afterManifest {
				t.Errorf("a shipped target/ file kept label %q", got)
			}
		})
	}
}

func TestDefaultDAGVersionAppliesDockerignore(t *testing.T) {
	dir := versionProject(t, "")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	writeProjectFile(t, dir, ".dockerignore", "notes\n**/*.log\n!notes/keep.md\n")
	writeProjectFile(t, dir, "notes/a.md", "a")
	writeProjectFile(t, dir, "notes/keep.md", "k")
	writeProjectFile(t, dir, "sub/x.log", "l")
	before := labelOf(t, dir)
	writeProjectFile(t, dir, "notes/a.md", "a2")
	writeProjectFile(t, dir, "sub/x.log", "l2")
	if got := labelOf(t, dir); got != before {
		t.Fatalf("files the .dockerignore keeps out moved the label: %q -> %q", before, got)
	}
	writeProjectFile(t, dir, "notes/keep.md", "k2")
	afterKeep := labelOf(t, dir)
	if afterKeep == before {
		t.Fatalf("a file re-included with ! kept label %q", before)
	}
	// A line that excludes nothing present still changes the file the build reads.
	writeProjectFile(t, dir, ".dockerignore", "notes\n**/*.log\n!notes/keep.md\ntmp/\n")
	if got := labelOf(t, dir); got == afterKeep {
		t.Errorf("changing .dockerignore kept label %q", got)
	}
}

func TestDefaultDAGVersionHashesAManifestOutsideTheProject(t *testing.T) {
	// The dbt compile reads dbt.manifest wherever it points, including outside
	// every directory the image copies.
	dir := t.TempDir()
	writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: sales\ndbt:\n  project: transform\n  manifest: ../build/manifest.json\n")
	writeProjectFile(t, dir, "transform/dbt_project.yml", "name: sales\n")
	writeProjectFile(t, dir, "build/manifest.json", `{"v":1}`)
	before := labelOf(t, dir)
	writeProjectFile(t, dir, "build/manifest.json", `{"v":2}`)
	if got := labelOf(t, dir); got == before {
		t.Errorf("a new manifest outside the project dir kept label %q", before)
	}
}

func TestDockerignoreMatcher(t *testing.T) {
	m := parseDockerignore([]byte("# comment\n\n/build\n*.tmp\n**/cache\ndocs/*.md\n!docs/README.md\n"))
	cases := map[string]bool{
		"build":          true,
		"build/out/x":    true,
		"a.tmp":          true,
		"sub/a.tmp":      false,
		"x/y/cache/z":    true,
		"cache":          true,
		"docs/a.md":      true,
		"docs/README.md": false,
		"docs/sub/a.md":  false,
		"src/main.py":    false,
	}
	for p, want := range cases {
		if got := m.excluded(p); got != want {
			t.Errorf("excluded(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestDefaultDAGVersionIgnoresARelativeOutputPath(t *testing.T) {
	dir := versionProject(t, "")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	t.Chdir(dir)
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	label := func() string {
		l, lerr := defaultDAGVersion(context.Background(), dir, cfg, "Dockerfile", "out.json")
		if lerr != nil {
			t.Fatal(lerr)
		}
		return l
	}
	before := label()
	writeProjectFile(t, dir, "out.json", "{}")
	if got := label(); got != before {
		t.Errorf("the -o output given relative to cwd moved the label: %q -> %q", before, got)
	}
}

func TestDefaultDAGVersionIgnoresInheritedGitEnv(t *testing.T) {
	other := t.TempDir()
	writeProjectFile(t, other, "f", "x")
	gitInitTagged(t, other, "v9.9.9")
	dir := versionProject(t, "")
	gitInitTagged(t, dir, "v1.0.0")
	// A git hook or CI step exports these; they would point every git call at
	// another repository whatever cmd.Dir says.
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	if label := labelOf(t, dir); !strings.HasPrefix(label, "v1.0.0-") {
		t.Errorf("label = %q, want the project's own v1.0.0", label)
	}
}
