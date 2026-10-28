package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shadowWarning is the start of the warning a project Dockerfile earns when
// dexaflow.yaml declares what only the generated Dockerfile applies.
const shadowWarning = "warning: the project builds from its own Dockerfile"

// A project Dockerfile is used as-is (ADR 0003), so the dexaflow.yaml fields
// only the generated Dockerfile applies do not reach the image. The build
// says so and names each field the project declares.
func TestCompileWarnsWhenAProjectDockerfileShadowsDexaflowYAML(t *testing.T) {
	dir := platformProject(t, "base_image: python:3.12-slim\ndependencies: [pandas==2.2.3]\nconnectors: [postgres]\n"+
		"system_packages: [libpq5]\ninclude_paths: [lib]\n")
	writeProjectFile(t, dir, "lib/a.py", "x = 1\n")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
	builder, _ := argvBuilder(t)
	_, errOut, err := run(t, "compile", dir, "--build", "--builder", builder)
	if err != nil {
		t.Fatalf("compile --build: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, shadowWarning) {
		t.Fatalf("no shadow warning:\n%s", errOut)
	}
	for _, want := range []string{"base_image", "dependencies", "connectors", "system_packages", "include_paths", "ADR 0003"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the warning does not name %s:\n%s", want, errOut)
		}
	}
}

// No warning without a project Dockerfile, nor when dexaflow.yaml declares
// nothing a Dockerfile would shadow.
func TestCompileDoesNotWarnWithoutShadowedFields(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml       string
		dockerfile bool
	}{
		"generated Dockerfile":          {"dependencies: [pandas==2.2.3]\n", false},
		"own Dockerfile, nothing set":   {"", true},
		"own Dockerfile, default paths": {"include_paths: [.]\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := platformProject(t, tc.yaml)
			if tc.dockerfile {
				writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
			}
			builder, _ := argvBuilder(t)
			_, errOut, err := run(t, "compile", dir, "--build", "--builder", builder)
			if err != nil {
				t.Fatalf("compile --build: %v\n%s", err, errOut)
			}
			if strings.Contains(errOut, shadowWarning) {
				t.Errorf("unexpected shadow warning:\n%s", errOut)
			}
		})
	}
}

// deploy builds through the same path and warns too.
func TestDeployWarnsWhenAProjectDockerfileShadowsDexaflowYAML(t *testing.T) {
	dir := platformProject(t, "dependencies: [pandas==2.2.3]\n")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
	builder, _ := argvBuilder(t)
	_, errOut, _ := deployDir(t, &deployServer{}, dir, builder, "--dag-version", "v1")
	if !strings.Contains(errOut, shadowWarning) || !strings.Contains(errOut, "dependencies") {
		t.Errorf("deploy printed no shadow warning naming dependencies:\n%s", errOut)
	}
}

// build.context and build.labels are accepted and do nothing: the build
// always uses the DAG directory and sets no label. validate and compile say
// so when a project sets them; the default context "." says nothing.
func TestIgnoredBuildFieldsWarn(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml string
		want string
	}{
		"context": {"build:\n  context: ..\n", "warning: build.context is ignored"},
		"labels":  {"build:\n  labels:\n    team: data\n", "warning: build.labels is ignored"},
		"default": {"build:\n  context: .\n", ""},
	} {
		for _, command := range []string{"validate", "compile"} {
			t.Run(name+"/"+command, func(t *testing.T) {
				dir := platformProject(t, tc.yaml)
				_, errOut, err := run(t, command, dir)
				if err != nil {
					t.Fatalf("%s: %v\n%s", command, err, errOut)
				}
				if tc.want == "" {
					if strings.Contains(errOut, "is ignored") {
						t.Errorf("unexpected warning:\n%s", errOut)
					}
					return
				}
				if !strings.Contains(errOut, tc.want) {
					t.Errorf("%s does not warn %q:\n%s", command, tc.want, errOut)
				}
			})
		}
	}
}

// A base_image the workspace, a target or the cluster fills in is not the
// project's own: a project Dockerfile shadows nothing the author wrote, so
// the warning does not name it.
func TestShadowWarningSkipsABaseImageFromTheWorkspace(t *testing.T) {
	isolateCLIEnv(t)
	root, project := defaultsWorkspace(t, "dag_id: orders\ndependencies: [pandas==2.2.3]\n")
	t.Chdir(root)
	writeProjectFile(t, project, "Dockerfile", "FROM scratch\n")
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
	builder, _ := argvBuilder(t)
	_, errOut, err := run(t, "compile", project, "--build", "--builder", builder, "--image", "reg.example/orders:1", "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("compile --build: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, shadowWarning) {
		t.Fatalf("no shadow warning for dependencies:\n%s", errOut)
	}
	if strings.Contains(errOut, "base_image") {
		t.Errorf("the warning names a base_image the project does not set:\n%s", errOut)
	}
}

// One field takes the singular, and a legacy project is named by its own
// file.
func TestShadowWarningGrammarAndConfigFile(t *testing.T) {
	dir := platformProject(t, "dependencies: [pandas==2.2.3]\n")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
	if err := os.Rename(filepath.Join(dir, "dexaflow.yaml"), filepath.Join(dir, "leoflow.yaml")); err != nil {
		t.Fatal(err)
	}
	builder, _ := argvBuilder(t)
	_, errOut, err := run(t, "compile", dir, "--build", "--builder", builder)
	if err != nil {
		t.Fatalf("compile --build: %v\n%s", err, errOut)
	}
	for _, want := range []string{"so dependencies in leoflow.yaml is ignored", "only the generated Dockerfile applies it", "Install it in your Dockerfile"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the warning does not say %q:\n%s", want, errOut)
		}
	}
}

// The build field warnings name a legacy project's own file too.
func TestIgnoredBuildFieldsNameTheConfigFile(t *testing.T) {
	dir := platformProject(t, "build:\n  context: sub\n")
	if err := os.Rename(filepath.Join(dir, "dexaflow.yaml"), filepath.Join(dir, "leoflow.yaml")); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := run(t, "validate", dir)
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "the directory holding leoflow.yaml") {
		t.Errorf("the build.context warning does not name leoflow.yaml:\n%s", errOut)
	}
}
