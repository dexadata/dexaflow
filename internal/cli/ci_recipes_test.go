package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// ciRecipeBlockRe finds a recipe of the CI/CD page for a system `ci init`
// does not write (#1887): an HTML comment naming the recipe, then a fenced
// block titled with the file it is saved as.
var ciRecipeBlockRe = regexp.MustCompile("(?s)<!-- ci-recipe: ([a-z-]+) -->\n```(yaml|groovy) title=\"([^\"]+)\"\n(.*?)```")

// ciRecipeWants lists, per recipe, what it must hold to deploy the way the
// page describes: the objects it defines and the commands it runs.
var ciRecipeWants = map[string][]string{
	"argo":    {"kind: WorkflowTemplate", "sh /tmp/repo/ci/dexaflow-deploy.sh"},
	"tekton":  {"kind: Task\n", "kind: TaskRun", "sh repo/ci/dexaflow-deploy.sh"},
	"jenkins": {"kubernetes {", "GIT_PREVIOUS_SUCCESSFUL_COMMIT", "sh ci/dexaflow-deploy.sh"},
	"buildah": {"sh repo/ci/dexaflow-deploy.sh install", "--builder buildah"},
	"kaniko":  {"--digest-file=out/digest", "dexaflow push out/dag.json --image"},
}

// jenkinsPodRe and jenkinsShRe find the pod template and the shell steps of
// a Jenkinsfile, the parts of it another tool reads.
var (
	jenkinsPodRe = regexp.MustCompile("(?s)yaml '''\n(.*?)'''")
	jenkinsShRe  = regexp.MustCompile("(?s)sh '''\n(.*?)'''")
)

// TestCIRecipesParse reads each recipe of the CI/CD page as its system
// would: every Kubernetes document parses and names its kind, and every
// shell script inside it (a Tekton step's script, an Argo container's
// `sh -c` argument, a Jenkins `sh` step) parses as POSIX sh. The docs flag
// test checks the `dexaflow` commands in them.
func TestCIRecipesParse(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile(filepath.Join("..", "..", "website", "content", "operate", "cicd-deploy.md"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, b := range ciRecipeBlockRe.FindAllStringSubmatch(string(page), -1) {
		name, lang, title, body := b[1], b[2], b[3], b[4]
		wants, ok := ciRecipeWants[name]
		if !ok {
			t.Errorf("block %s names an unknown recipe %q", title, name)
			continue
		}
		seen[name] = true
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("the %s recipe (%s) holds no %q", name, title, w)
			}
		}
		scripts, perr := recipeScripts(lang, body)
		if perr != nil {
			t.Errorf("the %s recipe (%s): %v", name, title, perr)
		}
		if len(scripts) == 0 {
			t.Errorf("the %s recipe (%s) runs no shell script", name, title)
		}
		for i, s := range scripts {
			checkRecipeShell(t, fmt.Sprintf("%s script %d", title, i+1), s)
		}
	}
	for name := range ciRecipeWants {
		if !seen[name] {
			t.Errorf("cicd-deploy.md shows no %s recipe", name)
		}
	}
}

// recipeScripts parses a recipe and returns the shell scripts it runs.
func recipeScripts(lang, body string) ([]string, error) {
	if lang == "groovy" {
		pod := jenkinsPodRe.FindStringSubmatch(body)
		if pod == nil {
			return nil, errors.New("the Jenkinsfile has no pod template")
		}
		if _, err := kubeDocuments(pod[1]); err != nil {
			return nil, fmt.Errorf("the pod template: %w", err)
		}
		var scripts []string
		for _, m := range jenkinsShRe.FindAllStringSubmatch(body, -1) {
			scripts = append(scripts, m[1])
		}
		return scripts, nil
	}
	docs, err := kubeDocuments(body)
	if err != nil {
		return nil, err
	}
	var scripts []string
	for _, d := range docs {
		scripts = append(scripts, shellScriptsIn(d)...)
	}
	return scripts, nil
}

// kubeDocuments parses every YAML document of s, each of which must be a
// Kubernetes object with an apiVersion and a kind.
func kubeDocuments(s string) ([]map[string]any, error) {
	dec := yaml.NewDecoder(strings.NewReader(s))
	var docs []map[string]any
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("does not parse: %w", err)
		}
		if doc["apiVersion"] == nil || doc["kind"] == nil {
			return nil, fmt.Errorf("a document has no apiVersion or kind: %v", doc)
		}
		docs = append(docs, doc)
	}
}

// shellScriptsIn walks a parsed document and returns each `script:` string
// and each argument a `command: [sh, -c]` runs.
func shellScriptsIn(v any) []string {
	var out []string
	switch n := v.(type) {
	case map[string]any:
		if s, ok := n["script"].(string); ok {
			out = append(out, s)
		}
		if isShC(n["command"]) {
			if args, ok := n["args"].([]any); ok && len(args) > 0 {
				if s, ok := args[0].(string); ok {
					out = append(out, s)
				}
			}
		}
		for _, child := range n {
			out = append(out, shellScriptsIn(child)...)
		}
	case []any:
		for _, child := range n {
			out = append(out, shellScriptsIn(child)...)
		}
	}
	return out
}

// isShC reports whether a container command is `sh -c`.
func isShC(v any) bool {
	c, ok := v.([]any)
	return ok && len(c) == 2 && c[0] == "sh" && c[1] == "-c"
}

// checkRecipeShell parses script with sh -n, and with shellcheck when it is
// installed.
func checkRecipeShell(t *testing.T, label, script string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "recipe.sh")
	writeTree(t, p, script)
	if sh, err := exec.LookPath("sh"); err == nil {
		if out, rerr := exec.CommandContext(t.Context(), sh, "-n", p).CombinedOutput(); rerr != nil { //nolint:gosec // a test script
			t.Errorf("%s: sh -n: %v\n%s\n%s", label, rerr, out, script)
		}
	}
	if bin, err := exec.LookPath("shellcheck"); err == nil {
		if out, rerr := exec.CommandContext(t.Context(), bin, "-s", "sh", p).CombinedOutput(); rerr != nil { //nolint:gosec // a test binary
			t.Errorf("%s: shellcheck: %v\n%s\n%s", label, rerr, out, script)
		}
	}
}
