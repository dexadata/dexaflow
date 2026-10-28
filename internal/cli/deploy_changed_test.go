package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitIn runs git in dir with a fixed identity, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)
	c := exec.CommandContext(t.Context(), "git", full...)
	c.Dir = dir
	c.Env = gitEnv(os.Environ())
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// changedWorkspace is a git repository holding a workspace of two projects,
// committed: dags/a ships dag.py and lib/ (include_paths), and keeps
// notes.md and lib/util.pyc (exclude_paths) out of its image; dags/b ships
// its dag.py alone (the default include_paths). README.md belongs to no
// project.
func changedWorkspace(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateCLIEnv(t)
	root := t.TempDir()
	reg := missingRegistry(t) + "/team"
	writeTree(t, filepath.Join(root, workspaceFileName), "targets: {}\n")
	writeTree(t, filepath.Join(root, "README.md"), "# dags\n")
	writeTree(t, filepath.Join(root, "dags", "a", "dag.py"), "# a\n")
	writeTree(t, filepath.Join(root, "dags", "a", "lib", "util.py"), "X = 1\n")
	writeTree(t, filepath.Join(root, "dags", "a", "notes.md"), "notes\n")
	writeTree(t, filepath.Join(root, "dags", "a", "lib", "util.pyc"), "bytecode\n")
	writeTree(t, filepath.Join(root, "dags", "a", projectConfigFile),
		"dag_id: a\ninclude_paths: [lib]\nregistry:\n  url: "+reg+"\n  image_name: a\n")
	writeTree(t, filepath.Join(root, "dags", "b", "dag.py"), "# b\n")
	writeTree(t, filepath.Join(root, "dags", "b", projectConfigFile),
		"dag_id: b\nregistry:\n  url: "+reg+"\n  image_name: b\n")
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "init")
	t.Chdir(root)
	return root
}

// changedReport is the part of the --output json document these tests read.
type changedReport struct {
	DAGs []struct {
		DagID  string `json:"dag_id"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"dags"`
	Removed []struct {
		Path  string `json:"path"`
		DagID string `json:"dag_id"`
	} `json:"removed"`
	Moved []struct {
		DagID string `json:"dag_id"`
		From  string `json:"from"`
		To    string `json:"to"`
	} `json:"moved"`
}

// planChanged runs deploy --plan --output json with the extra args against a
// fake control plane that has no DAG, and returns the report and stderr.
func planChanged(t *testing.T, args ...string) (changedReport, string, error) {
	t.Helper()
	srv := httptest.NewServer(&deployServer{status: http.StatusNotFound})
	defer srv.Close()
	full := append([]string{"deploy", "--plan", "--output", "json", "--server", srv.URL, "--token", "x",
		"--config", emptyConfig(t)}, args...)
	out, errOut, err := run(t, full...)
	var rep changedReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s\nstderr:\n%s", jerr, out, errOut)
	}
	return rep, errOut, err
}

// statuses maps each DAG of the report to its status.
func (r changedReport) statuses() map[string]string {
	m := map[string]string{}
	for _, d := range r.DAGs {
		m[d.DagID] = d.Status
	}
	return m
}

// TestDeployChangedSinceSelectsTheProjectsWhoseImageInputsChanged: a
// project is deployed exactly when a file its image or its version label is
// built from differs from the ref, uncommitted and untracked files included;
// every other project is skipped and reported with the reason.
func TestDeployChangedSinceSelectsTheProjectsWhoseImageInputsChanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, root string)
		want   map[string]string
	}{
		{"nothing changed", func(*testing.T, string) {}, map[string]string{"a": "skipped", "b": "skipped"}},
		{"a file of no project", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, "README.md"), "# edited\n")
		}, map[string]string{"a": "skipped", "b": "skipped"}},
		{"the DAG source, uncommitted", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, "dags", "a", "dag.py"), "# a edited\n")
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"a file under include_paths, staged", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, "dags", "a", "lib", "util.py"), "X = 2\n")
			gitIn(t, root, "add", "-A")
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"a new untracked file under include_paths", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, "dags", "a", "lib", "new.py"), "Y = 1\n")
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"a deleted file under include_paths", func(t *testing.T, root string) {
			gitIn(t, root, "rm", "-q", filepath.Join("dags", "a", "lib", "util.py"))
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"a deleted file under include_paths that exclude_paths leaves out", func(t *testing.T, root string) {
			gitIn(t, root, "rm", "-q", filepath.Join("dags", "a", "lib", "util.pyc"))
		}, map[string]string{"a": "skipped", "b": "skipped"}},
		{"an edited file under include_paths that exclude_paths leaves out", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, "dags", "a", "lib", "util.pyc"), "other bytecode\n")
		}, map[string]string{"a": "skipped", "b": "skipped"}},
		{"a project file outside its image", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, "dags", "a", "notes.md"), "more notes\n")
		}, map[string]string{"a": "skipped", "b": "skipped"}},
		{"a file next to the DAG source that the generated image does not copy", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, "dags", "b", "helpers.py"), "Z = 1\n")
		}, map[string]string{"a": "skipped", "b": "skipped"}},
		{"a file of a project whose own Dockerfile copies its whole directory", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, "dags", "b", "Dockerfile"), "FROM scratch\nCOPY . /dag\n")
			gitIn(t, root, "add", "-A")
			gitIn(t, root, "commit", "-q", "-m", "own Dockerfile")
			writeTree(t, filepath.Join(root, "dags", "b", "helpers.py"), "Z = 1\n")
		}, map[string]string{"a": "skipped", "b": "planned"}},
		{"the project config", func(t *testing.T, root string) {
			p := filepath.Join(root, "dags", "a", projectConfigFile)
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			writeTree(t, p, string(data)+"dependencies: [requests==2.32.3]\n")
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"the .dockerignore", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, "dags", "a", dockerignoreName), "lib/skip\n")
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"a project Dockerfile", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, "dags", "a", "Dockerfile"), "FROM scratch\n")
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"a deleted file under a symlinked include root", func(t *testing.T, root string) {
			a := filepath.Join(root, "dags", "a")
			writeTree(t, filepath.Join(a, "vendor", "real", "w.py"), "W = 1\n")
			if err := os.Symlink(filepath.Join("vendor", "real"), filepath.Join(a, "shared")); err != nil {
				t.Fatal(err)
			}
			setIncludePaths(t, a, "[lib, shared]")
			commitAll(t, root, "shared")
			gitIn(t, root, "rm", "-q", filepath.Join("dags", "a", "vendor", "real", "w.py"))
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"an image input that git ignores", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, ".gitignore"), "gen.py\nscratch.txt\n")
			commitAll(t, root, "ignore")
			writeTree(t, filepath.Join(root, "dags", "a", "lib", "gen.py"), "G = 1\n")
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"an ignored file outside every image", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, ".gitignore"), "gen.py\nscratch.txt\n")
			commitAll(t, root, "ignore")
			writeTree(t, filepath.Join(root, "dags", "a", "scratch.txt"), "x\n")
		}, map[string]string{"a": "skipped", "b": "skipped"}},
		{"a model of a dbt group", func(t *testing.T, root string) {
			a := filepath.Join(root, "dags", "a")
			writeTree(t, filepath.Join(a, "dbt_sales", "dbt_project.yml"), "name: sales\n")
			writeTree(t, filepath.Join(a, "dbt_sales", "models", "x.sql"), "select 1\n")
			writeTree(t, filepath.Join(a, "dbt_sales", "target", "manifest.json"), "{}\n")
			appendConfig(t, a, "dbt_groups:\n  sales:\n    project: dbt_sales\n")
			commitAll(t, root, "dbt")
			writeTree(t, filepath.Join(a, "dbt_sales", "models", "x.sql"), "select 2\n")
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"the manifest dbt parse rewrites in a dbt group", func(t *testing.T, root string) {
			a := filepath.Join(root, "dags", "a")
			writeTree(t, filepath.Join(a, "dbt_sales", "dbt_project.yml"), "name: sales\n")
			writeTree(t, filepath.Join(a, "dbt_sales", "target", "manifest.json"), "{}\n")
			appendConfig(t, a, "dbt_groups:\n  sales:\n    project: dbt_sales\n")
			commitAll(t, root, "dbt")
			writeTree(t, filepath.Join(a, "dbt_sales", "target", "manifest.json"), `{"x":1}`+"\n")
		}, map[string]string{"a": "skipped", "b": "skipped"}},
		{"an unchanged submodule under include_paths", func(t *testing.T, root string) {
			addSubmodule(t, root, "dags/a/lib/vendored", nil)
		}, map[string]string{"a": "skipped", "b": "skipped"}},
		{"a file under a submodule that the submodule's .gitignore ignores", func(t *testing.T, root string) {
			addSubmodule(t, root, "dags/a/lib/vendored", map[string]string{".gitignore": "gen.py\n"})
			writeTree(t, filepath.Join(root, "dags", "a", "lib", "vendored", "gen.py"), "G = 1\n")
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"a submodule the repository config hides", func(t *testing.T, root string) {
			addSubmodule(t, root, "dags/a/lib/vendored", nil)
			gitIn(t, root, "config", "diff.ignoreSubmodules", "all")
			writeTree(t, filepath.Join(root, "dags", "a", "lib", "vendored", "v.py"), "V = 2\n")
			commitAll(t, filepath.Join(root, "dags", "a", "lib", "vendored"), "bump")
		}, map[string]string{"a": "planned", "b": "skipped"}},
		{"the workspace file", func(t *testing.T, root string) {
			writeTree(t, filepath.Join(root, workspaceFileName), "targets: {}\ndefaults: {platforms: [linux/amd64]}\n")
		}, map[string]string{"a": "planned", "b": "planned"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := changedWorkspace(t)
			tc.change(t, root)
			rep, errOut, err := planChanged(t, "--all", "--changed-since", "HEAD")
			if err != nil {
				t.Fatalf("deploy: %v\n%s", err, errOut)
			}
			if got := rep.statuses(); !mapsEqual(got, tc.want) {
				t.Errorf("statuses = %v, want %v\n%s", got, tc.want, errOut)
			}
			for _, d := range rep.DAGs {
				if d.Status == "skipped" && !strings.Contains(d.Reason, "unchanged since HEAD") {
					t.Errorf("%s skipped with reason %q, want it to say unchanged since HEAD", d.DagID, d.Reason)
				}
				if d.Status == "skipped" && !strings.Contains(errOut, "skipping "+d.DagID+" (unchanged since HEAD") {
					t.Errorf("the text output does not list %s as skipped:\n%s", d.DagID, errOut)
				}
			}
		})
	}
}

// commitAll stages and commits everything in the repository at dir.
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", msg)
}

// addSubmodule adds a repository holding v.py and extra files as a submodule
// at rel, and commits.
func addSubmodule(t *testing.T, root, rel string, extra map[string]string) {
	t.Helper()
	sub := t.TempDir()
	writeTree(t, filepath.Join(sub, "v.py"), "V = 1\n")
	for name, body := range extra {
		writeTree(t, filepath.Join(sub, name), body)
	}
	gitIn(t, sub, "init", "-q")
	commitAll(t, sub, "sub")
	gitIn(t, root, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, rel)
	commitAll(t, root, "submodule")
}

// appendConfig appends YAML to the project's dexaflow.yaml.
func appendConfig(t *testing.T, dir, extra string) {
	t.Helper()
	p := filepath.Join(dir, projectConfigFile)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, p, string(data)+extra)
}

// setIncludePaths replaces the include_paths line of the project's
// dexaflow.yaml.
func setIncludePaths(t *testing.T, dir, list string) {
	t.Helper()
	p := filepath.Join(dir, projectConfigFile)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, p, strings.Replace(string(data), "include_paths: [lib]", "include_paths: "+list, 1))
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestDeployChangedSinceNamesAnIgnoredInput: a project built from a file
// git ignores cannot be compared, so it is deployed, and the reason names
// the file.
func TestDeployChangedSinceNamesAnIgnoredInput(t *testing.T) {
	root := changedWorkspace(t)
	writeTree(t, filepath.Join(root, ".gitignore"), "gen.py\n")
	commitAll(t, root, "ignore")
	writeTree(t, filepath.Join(root, "dags", "a", "lib", "gen.py"), "G = 1\n")
	_, errOut, err := planChanged(t, "--all", "--changed-since", "HEAD")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "a: built from dags/a/lib/gen.py, which git ignores") {
		t.Errorf("the output does not name the ignored input:\n%s", errOut)
	}
}

// TestDeployChangedSinceReportsAMovedProjectAsMoved: a project moved to
// another folder keeps its dag_id and is deployed from there; it is not
// reported as removed, whose advice would deregister a live DAG.
func TestDeployChangedSinceReportsAMovedProjectAsMoved(t *testing.T) {
	root := changedWorkspace(t)
	if err := os.MkdirAll(filepath.Join(root, "dags", "team"), 0o750); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "mv", filepath.Join("dags", "b"), filepath.Join("dags", "team", "b"))
	rep, errOut, err := planChanged(t, "--all", "--changed-since", "HEAD")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, errOut)
	}
	if got, want := rep.statuses(), map[string]string{"a": "skipped", "b": "planned"}; !mapsEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	if len(rep.Removed) != 0 {
		t.Errorf("removed = %+v, want none: b moved", rep.Removed)
	}
	if len(rep.Moved) != 1 || rep.Moved[0].DagID != "b" || rep.Moved[0].From != "dags/b" || rep.Moved[0].To != "dags/team/b" {
		t.Errorf("moved = %+v, want b from dags/b to dags/team/b", rep.Moved)
	}
	if strings.Contains(errOut, "dags delete") || !strings.Contains(errOut, "dags/b moved to dags/team/b") {
		t.Errorf("the output must say b moved, with no delete advice:\n%s", errOut)
	}
}

// TestProjectOutsideTheRepositoryCountsAsChanged: a project git cannot
// compare (its directory resolves outside the repository) is deployed.
func TestProjectOutsideTheRepositoryCountsAsChanged(t *testing.T) {
	t.Parallel()
	dir := versionProject(t, "")
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	top := t.TempDir()
	cs := &changeSet{ref: "main", top: top, paths: map[string]bool{}}
	got := cs.projectChanged(t.Context(), Project{Path: dir, DagID: "etl", Config: cfg}, "Dockerfile")
	if !got.changed || !strings.Contains(got.reason, "its directory "+dir+" is outside the repository at "+top) {
		t.Errorf("verdict = %+v, want changed, outside the repository at %s", got, top)
	}
}

// TestDeployChangedSinceRefusesAnEmptyRef: --changed-since "" is a typo or
// an unset CI variable, never a request to deploy everything.
func TestDeployChangedSinceRefusesAnEmptyRef(t *testing.T) {
	changedWorkspace(t)
	_, _, err := run(t, "deploy", "--all", "--plan", "--server", "http://127.0.0.1:1", "--config", emptyConfig(t), "--changed-since", "")
	if err == nil || !strings.Contains(err.Error(), "empty value") {
		t.Errorf("err = %v, want an empty --changed-since refused", err)
	}
}

// TestDeployChangedSinceComparesWithTheRef: the ref is a commit in history,
// so a change committed after it counts even when the tree is clean.
func TestDeployChangedSinceComparesWithTheRef(t *testing.T) {
	root := changedWorkspace(t)
	base := gitIn(t, root, "rev-parse", "HEAD")
	writeTree(t, filepath.Join(root, "dags", "b", "dag.py"), "# b edited\n")
	gitIn(t, root, "commit", "-q", "-am", "edit b")
	rep, errOut, err := planChanged(t, "--all", "--changed-since", base)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, errOut)
	}
	if got, want := rep.statuses(), map[string]string{"a": "skipped", "b": "planned"}; !mapsEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
}

// TestDeployChangedSinceReportsARemovedProject: a project deleted since the
// ref is reported, with its dag_id, and nothing is deployed or deregistered
// for it.
func TestDeployChangedSinceReportsARemovedProject(t *testing.T) {
	root := changedWorkspace(t)
	gitIn(t, root, "rm", "-q", "-r", filepath.Join("dags", "b"))
	rep, errOut, err := planChanged(t, "--all", "--changed-since", "HEAD")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, errOut)
	}
	if got, want := rep.statuses(), map[string]string{"a": "skipped"}; !mapsEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	if len(rep.Removed) != 1 || rep.Removed[0].DagID != "b" || rep.Removed[0].Path != "dags/b" {
		t.Errorf("removed = %+v, want dags/b (b)", rep.Removed)
	}
	if !strings.Contains(errOut, "dags/b") || !strings.Contains(errOut, "dexaflow dags delete b --deregister") {
		t.Errorf("the text output does not report the removed project and how to remove its DAG:\n%s", errOut)
	}
}

// TestDeployChangedSinceRefusals: what --changed-since cannot answer stops
// the deploy before anything is planned, with the way out.
func TestDeployChangedSinceRefusals(t *testing.T) {
	t.Run("an unknown ref", func(t *testing.T) {
		changedWorkspace(t)
		_, _, err := planChanged(t, "--all", "--changed-since", "no-such-branch")
		if err == nil || !strings.Contains(err.Error(), `"no-such-branch"`) || !strings.Contains(err.Error(), "git fetch") {
			t.Errorf("err = %v, want the unknown ref named and git fetch suggested", err)
		}
	})
	t.Run("a ref missing from a shallow clone", func(t *testing.T) {
		root := changedWorkspace(t)
		writeTree(t, filepath.Join(root, "README.md"), "# two\n")
		gitIn(t, root, "commit", "-q", "-am", "two")
		clone := filepath.Join(t.TempDir(), "clone")
		gitIn(t, root, "clone", "-q", "--depth", "1", "file://"+root, clone)
		t.Chdir(clone)
		_, _, err := planChanged(t, "--all", "--changed-since", "HEAD~1")
		if err == nil || !strings.Contains(err.Error(), "shallow") || !strings.Contains(err.Error(), "fetch-depth: 0") {
			t.Errorf("err = %v, want the shallow clone named and fetch-depth: 0 suggested", err)
		}
	})
	t.Run("no git repository", func(t *testing.T) {
		isolateCLIEnv(t)
		root := t.TempDir()
		writeTree(t, filepath.Join(root, workspaceFileName), "targets: {}\n")
		writeTree(t, filepath.Join(root, "dags", "a", "dag.py"), "# a\n")
		t.Chdir(root)
		_, _, err := planChanged(t, "--all", "--changed-since", "main")
		if err == nil || !strings.Contains(err.Error(), "not in a git repository") {
			t.Errorf("err = %v, want no git repository named", err)
		}
	})
	t.Run("a ref that reads as a flag", func(t *testing.T) {
		changedWorkspace(t)
		_, _, err := planChanged(t, "--all", "--changed-since=--output=x")
		if err == nil || !strings.Contains(err.Error(), "is not a git ref") {
			t.Errorf("err = %v, want the ref refused", err)
		}
	})
}

// TestDeployChangedSinceSelectsAmongTheWorkspace: the flag filters the
// workspace's projects, so it goes with --all (or a bare deploy at the
// workspace root) and refuses a named DAG or path, which it could only
// silently drop.
func TestDeployChangedSinceSelectsAmongTheWorkspace(t *testing.T) {
	for _, args := range [][]string{{"a"}, {filepath.Join("dags", "a")}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			changedWorkspace(t)
			_, _, err := planChanged(t, append([]string{"--changed-since", "HEAD"}, args...)...)
			if err == nil || !strings.Contains(err.Error(), "--all") {
				t.Errorf("err = %v, want --changed-since with %v refused, naming --all", err, args)
			}
		})
	}
	t.Run("inside a project", func(t *testing.T) {
		root := changedWorkspace(t)
		t.Chdir(filepath.Join(root, "dags", "a"))
		_, _, err := planChanged(t, "--changed-since", "HEAD")
		if err == nil || !strings.Contains(err.Error(), "--all") {
			t.Errorf("err = %v, want a bare deploy in a project refused, naming --all", err)
		}
	})
	t.Run("bare at the workspace root", func(t *testing.T) {
		root := changedWorkspace(t)
		writeTree(t, filepath.Join(root, "dags", "a", "dag.py"), "# a edited\n")
		rep, errOut, err := planChanged(t, "--changed-since", "HEAD")
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, errOut)
		}
		if got, want := rep.statuses(), map[string]string{"a": "planned", "b": "skipped"}; !mapsEqual(got, want) {
			t.Errorf("statuses = %v, want %v", got, want)
		}
	})
}

// TestDeployChangedSinceWithNothingChangedSaysSo: no project changed is a
// success that says nothing was deployed.
func TestDeployChangedSinceWithNothingChangedSaysSo(t *testing.T) {
	changedWorkspace(t)
	_, errOut, err := planChanged(t, "--all", "--changed-since", "HEAD")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "No DAG project changed since HEAD") {
		t.Errorf("output does not say nothing changed:\n%s", errOut)
	}
	if slices.ContainsFunc(strings.Split(errOut, "\n"), func(l string) bool { return strings.HasPrefix(l, "plan for ") }) {
		t.Errorf("a project was planned:\n%s", errOut)
	}
}

// TestGitEnvDropsEveryRepositoryOverride: a variable that points git at
// another repository, its objects or its index would make the comparison
// read that repository instead of the workspace's.
func TestGitEnvDropsEveryRepositoryOverride(t *testing.T) {
	t.Parallel()
	in := []string{"PATH=/bin", "GIT_DIR=/x", "GIT_WORK_TREE=/x", "GIT_INDEX_FILE=/x", "GIT_COMMON_DIR=/x",
		"GIT_OBJECT_DIRECTORY=/x", "GIT_ALTERNATE_OBJECT_DIRECTORIES=/x", "GIT_AUTHOR_NAME=a"}
	got := gitEnv(in)
	if want := []string{"PATH=/bin", "GIT_AUTHOR_NAME=a"}; !slices.Equal(got, want) {
		t.Errorf("gitEnv = %q, want %q", got, want)
	}
}
