package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// devDagImageRef returns the local image tag built for a DAG in cluster-mode.
func devDagImageRef(dagID string) string {
	return "leoflow-dev-" + dagID + ":dev"
}

// k3dCreateArgs builds the argv to create the dedicated dev cluster.
func k3dCreateArgs(cluster string) []string {
	return []string{"cluster", "create", cluster, "--wait"}
}

// k3dImportArgs builds the argv to import local images into the dev cluster (so
// task pods can use them without a registry).
func k3dImportArgs(cluster string, images ...string) []string {
	args := make([]string, 0, len(images)+3)
	args = append(args, "image", "import")
	args = append(args, images...)
	return append(args, "--cluster", cluster)
}

// devKubeconfigPath returns the isolated kubeconfig file under the dev home; the
// control plane is pointed here so it only ever targets the dev cluster.
func devKubeconfigPath(home string) string {
	return filepath.Join(home, "kubeconfig")
}

// baseImageBuildArgs builds the docker argv for the task base image.
func baseImageBuildArgs() []string {
	return []string{"build", "-f", filepath.Join("runtime", "Dockerfile"),
		"--build-arg", "PYTHON_VERSION=" + devPyVersion, "-t", devBaseImage, "."}
}

// kubectlNamespaceArgs builds the kubectl argv that creates the task-pod
// namespace in the dev cluster.
func kubectlNamespaceArgs(kubeconfig string) []string {
	return []string{"--kubeconfig", kubeconfig, "create", "namespace", devNamespace}
}

// devDockerfile is the Dockerfile generated for a project that does not ship its
// own: it layers the DAG source onto the task base image so the agent can import
// it (matching runtime/Dockerfile's PYTHONPATH convention).
//
// dbtGroups carries the cleaned, sorted project directories of a hybrid DAG's
// dbt task groups (see dbtGroupProjectDirs). They belong in the image for the
// same reason the DAG source does: a group's tasks run `dbt --project-dir
// <project>` from WORKDIR /home/leoflow (#20).
// It shares compile_build.go's guards deliberately. This generator had the whole
// #1070 class and #1064 alongside it, and it is the worse of the two places to
// have them: ensureProjectDockerfile WRITES the result to <project>/Dockerfile
// and never removes it, and ensureDockerfile honors a project-shipped
// Dockerfile verbatim, so one `dexaflow lite` run would persist a poisoned file
// that every later `compile --build` then used.
func devDockerfile(baseImage, dagSource string, deps []string, dbtGroups []string) (string, error) {
	if err := domain.CheckDockerfileWord("dag_source", dagSource); err != nil {
		return "", err
	}
	base := filepath.Base(dagSource)
	from, ferr := fromOperand("base_image", baseImage)
	if ferr != nil {
		return "", ferr
	}
	var b strings.Builder
	b.WriteString("FROM " + from + "\n")
	// Install the DAG's declared dependencies before COPY so the (rarely-changing)
	// dependency layer is cached across edits to dag.py.
	if len(deps) > 0 {
		args, aerr := shellArgs("dependencies", deps)
		if aerr != nil {
			return "", aerr
		}
		b.WriteString("RUN pip install --no-cache-dir -- " + args + "\n")
	}
	// Every group is checked before the short-circuit: a `.` group made the
	// function return before any other group was looked at.
	for _, project := range dbtGroups {
		if err := domain.CheckDockerfileWord("dbt_groups.*.project", project); err != nil {
			return "", err
		}
	}
	if slices.Contains(dbtGroups, ".") {
		// project: "." means the project is the DAG directory; one COPY covers both.
		b.WriteString("COPY . /home/leoflow/\n")
	} else {
		if err := writeCopy(&b, "dag_source", base); err != nil {
			return "", err
		}
		for _, project := range dbtGroups {
			if err := writeCopy(&b, "dbt_groups.*.project", project); err != nil {
				return "", err
			}
		}
	}
	b.WriteString("ENV PYTHONPATH=/home/leoflow\n")
	return b.String(), nil
}

// devClusterSetup ensures the task base image, the dedicated k3d cluster, its
// namespace, and an isolated kubeconfig, then returns the Kubernetes-executor
// server env plus a reload that builds the DAG image, imports it into the
// cluster, and registers: real pod-per-task, fully isolated, at the cost of
// an image build per change.
//
// Multi-DAG workspaces are NOT supported in cluster mode for v1: every save
// would rebuild N images and re-import each into k3d (~15-30s per save with
// BuildKit cache; far worse cold). The function fails loud with a hint to use
// --executor=subprocess. Cluster-mode multi-DAG is tracked as a follow-up
// (the "Stage" mode design conversation).
func devClusterSetup(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, o devOptions, home string) (env []string, makeReload func(func() string) func() error, err error) {
	if len(ws.Projects) != 1 {
		paths := make([]string, 0, len(ws.Projects))
		for _, p := range ws.Projects {
			paths = append(paths, p.Path)
		}
		return nil, nil, fmt.Errorf("cluster mode (--executor=k8s) does not support multi-DAG workspaces in v1 (got %d projects: %v)\n"+
			"  - use --executor=subprocess for multi-DAG dev (the default, no Docker needed)\n"+
			"  - or point lite at a single project: `dexaflow lite <project-dir>`",
			len(ws.Projects), paths)
	}
	project := ws.Projects[0]
	dir := project.Path
	cfg := project.Config
	if berr := ensureBaseImage(ctx, cmd); berr != nil {
		return nil, nil, berr
	}
	if cerr := ensureDevCluster(ctx, cmd); cerr != nil {
		return nil, nil, cerr
	}
	kubeconfig := devKubeconfigPath(home)
	if kerr := writeDevKubeconfig(ctx, cmd, kubeconfig); kerr != nil {
		return nil, nil, kerr
	}
	if nerr := ensureDevNamespace(ctx, cmd, kubeconfig); nerr != nil {
		return nil, nil, nerr
	}
	if derr := ensureProjectDockerfile(cmd, dir, cfg); derr != nil {
		return nil, nil, derr
	}
	image := devDagImageRef(cfg.DagID)
	makeReload = func(mintToken func() string) func() error {
		return func() error {
			token := mintToken()
			base := func() error {
				opts := compileOptions{image: image, build: true, builder: "docker", dockerfile: "Dockerfile"}
				return devCompileAndRegister(ctx, cmd, dir, opts, token, func() error {
					return k3dImport(ctx, cmd, image)
				}, devURL(o.port))
			}
			return devReportingReload(ctx, base, devURL(o.port), token, dagSourcePath(dir, cfg))()
		}
	}
	env = clusterServerEnv(o.liteEnv(), kubeconfig)
	if wd, aerr := filepath.Abs(dir); aerr == nil {
		env = append(env, liteEditorEnv(wd, filepath.Dir(home))...)
	}
	return env, makeReload, nil
}

// runDevTool runs an external dev tool (k3d/docker/kubectl), streaming to the
// command's output. Commands reach it as cliDeps.devRun, which tests stub.
func runDevTool(ctx context.Context, cmd *cobra.Command, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...) //nolint:gosec // dev tool invoking fixed external binaries
	c.Stdout, c.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	return c.Run()
}

// devToolOutput runs an external dev tool and captures its combined output for
// inspection. Commands reach it as cliDeps.devOutput, which tests stub.
func devToolOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // dev tool invoking fixed external binaries
}

// ensureBaseImage builds the task base image (runtime/Dockerfile) if it is not
// already present, so DAG images can layer onto it. Requires the leoflow source
// tree (it builds from runtime/Dockerfile with the repo as context).
func ensureBaseImage(ctx context.Context, cmd *cobra.Command) error {
	if _, err := depsFrom(ctx).devOutput(ctx, "docker", "image", "inspect", devBaseImage); err == nil {
		return nil
	}
	// The base image is built from runtime/Dockerfile with the repo as context;
	// a binary install (curl|sh) has no source tree, so fail clearly and point at
	// the local run mode instead of the cryptic "lstat runtime: no such file".
	if _, err := os.Stat(filepath.Join("runtime", "Dockerfile")); err != nil {
		return fmt.Errorf("cluster run mode needs the Dexaflow source tree to build the task base image " +
			"(runtime/Dockerfile), which a binary install does not have.\n" +
			"  Use the 'local' run mode: re-run `dexaflow setup` and choose 1 (local), " +
			"or set `lite_executor: subprocess` in ~/.dexaflow/config.yaml.\n" +
			"  (Cluster mode works when you run `dexaflow lite` from a Dexaflow source checkout.)")
	}
	devPrintln(cmd.OutOrStdout(), "▸ building task base image "+devBaseImage+" (first run) …")
	if err := depsFrom(ctx).devRun(ctx, cmd, "docker", baseImageBuildArgs()...); err != nil {
		return fmt.Errorf("building base image (run from the dexaflow source tree): %w", err)
	}
	return nil
}

// ensureDevCluster creates the dedicated k3d cluster if it does not exist.
func ensureDevCluster(ctx context.Context, cmd *cobra.Command) error {
	out, _ := depsFrom(ctx).devOutput(ctx, "k3d", "cluster", "list", "--no-headers") //nolint:errcheck // absence is handled below
	if strings.Contains(string(out), devClusterName) {
		return nil
	}
	devPrintln(cmd.OutOrStdout(), "▸ creating dedicated dev cluster "+devClusterName+" (first run) …")
	if err := depsFrom(ctx).devRun(ctx, cmd, "k3d", k3dCreateArgs(devClusterName)...); err != nil {
		return fmt.Errorf("creating k3d cluster %s (is k3d installed?): %w", devClusterName, err)
	}
	return nil
}

// writeDevKubeconfig writes the dev cluster's kubeconfig to an isolated file so
// the control plane only ever targets leoflow-dev, never the product cluster.
func writeDevKubeconfig(ctx context.Context, _ *cobra.Command, path string) error {
	out, err := depsFrom(ctx).devOutput(ctx, "k3d", "kubeconfig", "get", devClusterName)
	if err != nil {
		return fmt.Errorf("getting kubeconfig for %s: %w", devClusterName, err)
	}
	if werr := os.WriteFile(path, out, 0o600); werr != nil {
		return fmt.Errorf("writing kubeconfig %s: %w", path, werr)
	}
	return nil
}

// ensureDevNamespace creates the task-pod namespace in the dev cluster (idempotent).
func ensureDevNamespace(ctx context.Context, _ *cobra.Command, kubeconfig string) error {
	out, err := depsFrom(ctx).devOutput(ctx, "kubectl", kubectlNamespaceArgs(kubeconfig)...)
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("creating namespace %s: %s: %w", devNamespace, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// k3dImport imports a locally-built image into the dev cluster so task pods can
// use it without a registry.
func k3dImport(ctx context.Context, cmd *cobra.Command, image string) error {
	devPrintln(cmd.OutOrStdout(), "▸ importing "+image+" into "+devClusterName+" …")
	if err := depsFrom(ctx).devRun(ctx, cmd, "k3d", k3dImportArgs(devClusterName, image)...); err != nil {
		return fmt.Errorf("importing %s into %s: %w", image, devClusterName, err)
	}
	return nil
}

// ensureProjectDockerfile generates a default Dockerfile when the project lacks
// one, layering the DAG source onto the task base image.
func ensureProjectDockerfile(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig) error {
	df := filepath.Join(dir, "Dockerfile")
	if _, err := os.Stat(df); err == nil {
		return nil
	}
	src := cfg.DagSource
	if src == "" {
		src = "dag.py"
	}
	deps, derr := cfg.EffectiveDependencies()
	if derr != nil {
		return fmt.Errorf("resolving dependencies: %w", derr)
	}
	// The same check Validate runs (#1268), on the raw values under their own
	// names: devDockerfile only ever sees the CLEANED dbt list, and
	// filepath.Clean hides the defect rather than refusing it.
	if derr := cfg.ValidateDockerfileValues(); derr != nil {
		return derr
	}
	content, gerr := devDockerfile(devBaseImage, src, deps, dbtGroupProjectDirs(cfg))
	if gerr != nil {
		return gerr
	}
	devPrintln(cmd.OutOrStdout(), "▸ generating a default Dockerfile (none found) …")
	if werr := os.WriteFile(df, []byte(content), 0o600); werr != nil {
		return fmt.Errorf("writing Dockerfile: %w", werr)
	}
	return nil
}
