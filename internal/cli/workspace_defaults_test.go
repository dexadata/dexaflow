package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// defaultsWorkspace writes a workspace with shared defaults, a target dev
// with its own registry and a target bare without one, and a project
// dags/orders whose dexaflow.yaml is projectYAML.
func defaultsWorkspace(t *testing.T, projectYAML string) (root, project string) {
	t.Helper()
	root = t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), "targets:\n"+
		"  dev: {server: https://dev.example, registry: ghcr.io/acme/dev, platforms: [linux/arm64]}\n"+
		"  bare: {server: https://bare.example}\n"+
		"defaults:\n  registry: ghcr.io/acme\n  platforms: [linux/amd64, linux/arm64]\n  base_image: ghcr.io/acme/base:1\n")
	project = filepath.Join(root, "dags", "orders")
	writeTree(t, filepath.Join(project, projectConfigFile), projectYAML)
	writeTree(t, filepath.Join(project, "dag.py"), "")
	return root, project
}

func overlayFor(t *testing.T, project string, args ...string) (registry, image, source string, platforms []string, base string) {
	t.Helper()
	root := NewRootCommand()
	cmd, rest, err := root.Find(append([]string{"deploy"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	if perr := cmd.ParseFlags(rest); perr != nil {
		t.Fatal(perr)
	}
	cfg, err := loadProjectConfig(project)
	if err != nil {
		t.Fatal(err)
	}
	prov, err := applyWorkspaceDefaults(cmd, project, cfg, true)
	if err != nil {
		t.Fatalf("applyWorkspaceDefaults: %v", err)
	}
	return cfg.Registry.URL, cfg.Registry.ImageName, prov.registrySource, cfg.Build.Platforms, cfg.BaseImage
}

func TestRegistryPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		project   string
		env       string
		args      []string
		userCfg   string
		want      string
		wantFrom  string
		wantImage string
	}{
		{name: "target", project: "dag_id: Orders_\n", args: []string{"--target", "dev"}, want: "ghcr.io/acme/dev", wantFrom: `target "dev"`, wantImage: "orders"},
		{name: "workspace defaults", project: "dag_id: orders\n", args: []string{"--target", "bare"}, want: "ghcr.io/acme", wantFrom: "defaults in"},
		{name: "project beats target", project: "dag_id: orders\nregistry: {url: quay.io/me, image_name: o}\n", args: []string{"--target", "dev"}, want: "quay.io/me", wantFrom: "dexaflow.yaml", wantImage: "o"},
		{name: "env beats project", project: "dag_id: orders\nregistry: {url: quay.io/me}\n", env: "env.example/r", want: "env.example/r", wantFrom: "DEXAFLOW_REGISTRY"},
		{name: "flag beats env", project: "dag_id: orders\n", env: "env.example/r", args: []string{"--registry", "flag.example/r"}, want: "flag.example/r", wantFrom: "--registry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLIEnv(t)
			if tc.env != "" {
				t.Setenv("DEXAFLOW_REGISTRY", tc.env)
			}
			root, project := defaultsWorkspace(t, tc.project)
			t.Chdir(root)
			args := append([]string{"--config", emptyConfig(t)}, tc.args...)
			got, image, from, _, _ := overlayFor(t, project, args...)
			if got != tc.want {
				t.Errorf("registry = %q, want %q", got, tc.want)
			}
			if !strings.Contains(from, tc.wantFrom) {
				t.Errorf("source = %q, want it to mention %q", from, tc.wantFrom)
			}
			want := tc.wantImage
			if want == "" {
				want = "orders"
			}
			if image != want {
				t.Errorf("image_name = %q, want %q", image, want)
			}
		})
	}
}

func TestRegistryFromTheUserConfigIsTheLastResort(t *testing.T) {
	isolateCLIEnv(t)
	project := filepath.Join(t.TempDir(), "orders")
	writeTree(t, filepath.Join(project, projectConfigFile), "dag_id: orders\n")
	t.Chdir(t.TempDir())
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "registry: ghcr.io/me\n")
	got, _, from, _, _ := overlayFor(t, project, "--config", cfg)
	if got != "ghcr.io/me" || !strings.Contains(from, cfg) {
		t.Errorf("registry = %q from %q, want the config file's", got, from)
	}
}

func TestOverlaySourcesAreReported(t *testing.T) {
	isolateCLIEnv(t)
	root, project := defaultsWorkspace(t, "dag_id: orders\n")
	t.Chdir(root)
	root2 := NewRootCommand()
	cmd, rest, err := root2.Find([]string{"deploy", "--config", emptyConfig(t), "--target", "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if perr := cmd.ParseFlags(rest); perr != nil {
		t.Fatal(perr)
	}
	cfg, _ := loadProjectConfig(project)
	prov, err := applyWorkspaceDefaults(cmd, project, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prov.platformsSource, `target "dev"`) || !strings.Contains(prov.baseImageSource, "defaults in") {
		t.Errorf("sources = %+v", prov)
	}
}

// TestWorkspaceDefaultsDoNotReachAProjectOutsideIt: with a target, the
// workspace is the one around the current folder; its defaults are for its
// own projects only.
func TestWorkspaceDefaultsDoNotReachAProjectOutsideIt(t *testing.T) {
	isolateCLIEnv(t)
	root, _ := defaultsWorkspace(t, "dag_id: orders\n")
	t.Chdir(root)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	writeTree(t, filepath.Join(outside, projectConfigFile), "dag_id: elsewhere\n")
	got, _, _, _, base := overlayFor(t, outside, "--config", emptyConfig(t), "--target", "bare")
	if got != "" || base != "" {
		t.Errorf("registry %q base %q: the workspace's defaults reached a project outside it", got, base)
	}
}

func TestPlatformsAndBaseImageDefaults(t *testing.T) {
	isolateCLIEnv(t)
	root, project := defaultsWorkspace(t, "dag_id: orders\n")
	t.Chdir(root)
	_, _, _, platforms, base := overlayFor(t, project, "--config", emptyConfig(t), "--target", "dev")
	if !reflect.DeepEqual(platforms, []string{"linux/arm64"}) || base != "ghcr.io/acme/base:1" {
		t.Errorf("target dev: platforms %v base %q, want the target's platforms and the workspace base image", platforms, base)
	}
	_, _, _, platforms, _ = overlayFor(t, project, "--config", emptyConfig(t), "--target", "bare")
	if !reflect.DeepEqual(platforms, []string{"linux/amd64", "linux/arm64"}) {
		t.Errorf("target bare: platforms %v, want the workspace defaults", platforms)
	}

	root, project = defaultsWorkspace(t, "dag_id: orders\nbase_image: mine:1\nbuild: {platforms: [linux/s390x]}\n")
	t.Chdir(root)
	_, _, _, platforms, base = overlayFor(t, project, "--config", emptyConfig(t), "--target", "dev")
	if !reflect.DeepEqual(platforms, []string{"linux/s390x"}) || base != "mine:1" {
		t.Errorf("the project's own values must win: platforms %v base %q", platforms, base)
	}
}

func TestOutsideAWorkspaceNothingChanges(t *testing.T) {
	isolateCLIEnv(t)
	project := filepath.Join(t.TempDir(), "orders")
	writeTree(t, filepath.Join(project, projectConfigFile), "dag_id: orders\n")
	t.Chdir(t.TempDir())
	got, image, _, platforms, base := overlayFor(t, project, "--config", emptyConfig(t))
	if got != "" || image != "" || base != "" || !reflect.DeepEqual(platforms, []string{"linux/amd64"}) {
		t.Errorf("got registry %q image %q base %q platforms %v, want the project's own (empty) values", got, image, base, platforms)
	}
}

// TestDeployReportsWhereTheRegistryCameFrom drives the real deploy: the
// workspace supplies the registry, so the guard no longer fires, and the
// output names the source before the build starts.
func TestDeployReportsWhereTheRegistryCameFrom(t *testing.T) {
	isolateCLIEnv(t)
	root, _ := defaultsWorkspace(t, "dag_id: orders\n")
	// dev points at a port nothing listens on: the deploy stops at the build.
	writeTree(t, filepath.Join(root, workspaceFileName), "targets:\n"+
		"  dev: {server: http://127.0.0.1:1, registry: ghcr.io/acme/dev}\n")
	t.Chdir(filepath.Join(root, "dags", "orders"))
	out, _, err := run(t, "deploy", "--config", emptyConfig(t), "--target", "dev", "--builder", "/nonexistent/builder")
	if err != nil && strings.Contains(err.Error(), "requires a container registry") {
		t.Fatalf("the workspace registry was not applied: %v", err)
	}
	if !strings.Contains(out, "ghcr.io/acme/dev/orders") || !strings.Contains(out, `target "dev"`) {
		t.Errorf("deploy must print the registry and its source:\n%s\nerr: %v", out, err)
	}
	for _, want := range []string{
		"server:     http://127.0.0.1:1 (target dev, from --target)",
		"platforms:  linux/amd64 (from the default)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("deploy output lacks %q:\n%s", want, out)
		}
	}
}

func TestCompileAndBuildTakeTheRegistryFlag(t *testing.T) {
	for _, name := range []string{"compile", "build", "deploy"} {
		cmd, _, err := NewRootCommand().Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Flags().Lookup("registry") == nil {
			t.Errorf("%s has no --registry: its image would not match deploy's", name)
		}
	}
}
