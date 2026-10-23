package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDAGVersionHashesSymlinksWithoutFollowing(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	err := hashReadError("p/secret.txt", os.ErrPermission)
	if !strings.Contains(err.Error(), "exclude_paths") || !strings.Contains(err.Error(), "--dag-version") || !strings.Contains(err.Error(), "p/secret.txt") {
		t.Errorf("err = %v", err)
	}
}

func TestDefaultDAGVersionDbtShippedInputs(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestDefaultDAGVersionHashesADockerfileTheDockerignoreLists(t *testing.T) {
	t.Parallel()
	dir := versionProject(t, "")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	writeProjectFile(t, dir, ".dockerignore", "Dockerfile\n")
	before := labelOf(t, dir)
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY dag.py /app/\n")
	if got := labelOf(t, dir); got == before {
		t.Errorf("a changed Dockerfile listed in .dockerignore kept label %q", before)
	}
}

func TestDefaultDAGVersionFollowsSymlinkedRoots(t *testing.T) {
	t.Parallel()
	t.Run("dag_source", func(t *testing.T) {
		dir := versionProject(t, "dag_source: dag.py\n")
		writeProjectFile(t, dir, "real.py", "# v1\n")
		if err := os.Remove(filepath.Join(dir, "dag.py")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real.py", filepath.Join(dir, "dag.py")); err != nil {
			t.Fatal(err)
		}
		before := labelOf(t, dir)
		writeProjectFile(t, dir, "real.py", "# v2\n")
		if got := labelOf(t, dir); got == before {
			t.Errorf("an edit behind the dag.py link kept label %q", before)
		}
	})
	t.Run("include_paths", func(t *testing.T) {
		dir := versionProject(t, "include_paths: [lib]\n")
		writeProjectFile(t, dir, "shared/util.py", "X = 1\n")
		if err := os.Symlink("shared", filepath.Join(dir, "lib")); err != nil {
			t.Fatal(err)
		}
		before := labelOf(t, dir)
		writeProjectFile(t, dir, "shared/util.py", "X = 2\n")
		if got := labelOf(t, dir); got == before {
			t.Errorf("an edit behind the lib link kept label %q", before)
		}
	})
	t.Run("a root that leaves the project is refused", func(t *testing.T) {
		dir := versionProject(t, "include_paths: [lib]\n")
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, "lib")); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := defaultDAGVersion(context.Background(), dir, cfg, "Dockerfile", ""); err == nil || !strings.Contains(err.Error(), "outside the project") {
			t.Errorf("err = %v, want an outside-the-project refusal", err)
		}
	})
}

func TestDefaultDAGVersionHashesDockerignoreOnlyWithAProjectDockerfile(t *testing.T) {
	t.Parallel()
	dir := versionProject(t, "")
	writeProjectFile(t, dir, ".dockerignore", "notes\n")
	before := labelOf(t, dir)
	writeProjectFile(t, dir, ".dockerignore", "notes\ntmp/\n")
	if got := labelOf(t, dir); got != before {
		t.Errorf("with the generated Dockerfile a .dockerignore edit moved the label: %q -> %q", before, got)
	}
}

func TestDockerignoreReincludeDescendsOnlyWhereItCan(t *testing.T) {
	t.Parallel()
	m := parseDockerignore([]byte("notes\nbuild\n!notes/keep.md\n"))
	if !m.mayReinclude("notes") {
		t.Error("notes holds a re-included file; the walk must descend")
	}
	if m.mayReinclude("build") {
		t.Error("nothing under build is re-included; the walk must skip it")
	}
	if !parseDockerignore([]byte("data\n!*/keep.txt\n")).mayReinclude("data") {
		t.Error("!*/keep.txt reaches data/keep.txt")
	}
	// A negation naming the directory itself brings back nothing beneath it.
	if parseDockerignore([]byte("data\n!data/keep\n")).mayReinclude("data/keep") {
		t.Error("!data/keep names the directory, not anything under it")
	}
	if parseDockerignore([]byte("data\n!*/keep.txt\n")).mayReinclude("data/sub") {
		t.Error("!*/keep.txt cannot reach under data/sub")
	}
	if !parseDockerignore([]byte("x\n!**/keep\n")).mayReinclude("x") {
		t.Error("a ** negation can match anywhere")
	}
}

func TestDockerignoreClassNeverMatchesASlash(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"a[/]b", "a/b", false},
		{"a[^x]b", "a/b", false},
		{"a[!x]b", "a/b", false},
		{"a[^x]b", "acb", true},
		{"a[bc]d", "abd", true},
	}
	for _, c := range cases {
		if got := parseDockerignore([]byte(c.pattern + "\n")).excluded(c.path); got != c.want {
			t.Errorf("%q excludes %q = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestDefaultDAGVersionHashesADockerfileOutsideTheProject(t *testing.T) {
	t.Parallel()
	t.Run("--dockerfile ../X", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "proj")
		writeProjectFile(t, dir, "dag.py", "# dag\n")
		writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: etl\n")
		writeProjectFile(t, parent, "Dockerfile.shared", "FROM scratch\n")
		cfg, err := loadProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		label := func() string {
			l, lerr := defaultDAGVersion(context.Background(), dir, cfg, "../Dockerfile.shared", "")
			if lerr != nil {
				t.Fatalf("defaultDAGVersion: %v", lerr)
			}
			return l
		}
		before := label()
		writeProjectFile(t, parent, "Dockerfile.shared", "FROM busybox\n")
		if got := label(); got == before {
			t.Errorf("an edited shared Dockerfile kept label %q", before)
		}
	})
	t.Run("a Dockerfile symlinked outside", func(t *testing.T) {
		dir := versionProject(t, "")
		outside := t.TempDir()
		writeProjectFile(t, outside, "Dockerfile", "FROM scratch\n")
		if err := os.Symlink(filepath.Join(outside, "Dockerfile"), filepath.Join(dir, "Dockerfile")); err != nil {
			t.Fatal(err)
		}
		before := labelOf(t, dir)
		writeProjectFile(t, outside, "Dockerfile", "FROM busybox\n")
		if got := labelOf(t, dir); got == before {
			t.Errorf("an edit behind the Dockerfile link kept label %q", before)
		}
	})
}

func TestDefaultDAGVersionSeesAFileAGlobNegationReincludes(t *testing.T) {
	t.Parallel()
	dir := versionProject(t, "")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	writeProjectFile(t, dir, ".dockerignore", "data\n!*/keep.txt\n")
	writeProjectFile(t, dir, "data/keep.txt", "k")
	writeProjectFile(t, dir, "data/other.txt", "o")
	before := labelOf(t, dir)
	writeProjectFile(t, dir, "data/other.txt", "o2")
	if got := labelOf(t, dir); got != before {
		t.Fatalf("an excluded file moved the label: %q -> %q", before, got)
	}
	writeProjectFile(t, dir, "data/keep.txt", "k2")
	if got := labelOf(t, dir); got == before {
		t.Errorf("data/keep.txt ships through !*/keep.txt, but editing it kept label %q", before)
	}
}
