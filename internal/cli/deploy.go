package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

// deployOptions holds the resolved flags for a deploy run.
type deployOptions struct {
	serverURL  string
	token      string
	builder    string
	dockerfile string
	dagVersion string
	all        bool
	skipBuild  bool
	trigger    bool
	yes        bool
}

// newDeployCommand builds `leoflow deploy [path]`: the pipeline-less promotion of
// a DAG to a Pro control plane (ADR 0041). It reuses the compile/build/push
// primitives, then re-pins the image by digest and registers the dag.json — one
// verb for what is otherwise compile --build --push + push by hand.
func newDeployCommand() *cobra.Command {
	var o deployOptions
	cmd := &cobra.Command{
		Use:   "deploy [path | dag_id]",
		Short: "Build, push, and register a DAG to a control plane (Pro).",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeployTarget(cmd, args, o)
		},
	}
	cmd.Flags().BoolVar(&o.all, "all", false, "deploy every DAG project in the workspace")
	cmd.Flags().BoolVar(&o.skipBuild, "skip-build", false, "reuse the existing image (skip docker build/push) but still recompile dag.json from leoflow.yaml/dag.py")
	cmd.Flags().BoolVar(&o.trigger, "trigger", false, "trigger a run immediately after registering")
	cmd.Flags().BoolVarP(&o.yes, "yes", "y", false, "skip the confirmation prompt (for automation)")
	cmd.Flags().StringVar(&o.serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&o.token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	cmd.Flags().StringVar(&o.builder, "builder", "docker", "image build tool to shell out to (e.g. docker, podman, nerdctl)")
	cmd.Flags().StringVar(&o.dockerfile, "dockerfile", "Dockerfile", "Dockerfile path relative to the DAG directory")
	cmd.Flags().StringVar(&o.dagVersion, "dag-version", "", "DAG version label (default: git describe, else dev)")
	return cmd
}

// runDeployTarget resolves what to deploy from the args and flags: --all (every
// project in the workspace), a directory path, or a dag_id (resolved to its
// subdir in the multi-DAG workspace). A bare invocation deploys the current dir.
func runDeployTarget(cmd *cobra.Command, args []string, o deployOptions) error {
	if o.all {
		if len(args) > 0 {
			return fmt.Errorf("--all deploys the whole workspace; do not also pass a path or dag_id")
		}
		return deployAll(cmd, o)
	}
	target := "."
	if len(args) == 1 {
		target = args[0]
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return runDeploy(cmd, target, o)
	}
	if len(args) == 1 {
		dir, rerr := resolveProjectDir(defaultWorkspace(cmd), target)
		if rerr != nil {
			return rerr
		}
		return runDeploy(cmd, dir, o)
	}
	return runDeploy(cmd, target, o)
}

// resolveProjectDir maps a dag_id to its project directory within the workspace,
// erroring (with the available ids) when no project matches — so a typo is a
// loud, helpful failure rather than a silent wrong-DAG deploy.
func resolveProjectDir(workspaceDir, dagID string) (string, error) {
	ws, err := ResolveWorkspace(workspaceDir)
	if err != nil {
		return "", err
	}
	available := make([]string, 0, len(ws.Projects))
	for _, p := range ws.Projects {
		if p.DagID == dagID {
			return p.Path, nil
		}
		available = append(available, p.DagID)
	}
	return "", fmt.Errorf("no DAG %q in workspace %s; available: %s",
		dagID, workspaceDir, strings.Join(available, ", "))
}

// deployAll deploys every project in the workspace, best-effort: it builds,
// pushes, and registers each, prints a per-DAG summary, and returns a non-zero
// error if any failed (so CI catches it) — partial pushes are not rolled back
// (content-addressed images make a re-deploy idempotent). ADR 0041.
func deployAll(cmd *cobra.Command, o deployOptions) error {
	wsDir := defaultWorkspace(cmd)
	ws, err := ResolveWorkspace(wsDir)
	if err != nil {
		return err
	}
	if len(ws.Projects) == 0 {
		return fmt.Errorf("no DAG projects found in workspace %s", wsDir)
	}
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if serverURL, _, terr := resolveServerToken(cmd, o.serverURL, o.token); terr == nil &&
		shouldConfirm(serverURL, o.yes, cmdInteractive(cmd)) {
		target := fmt.Sprintf("%d DAG(s) from %s", len(ws.Projects), wsDir)
		if !confirmDeploy(cmd.InOrStdin(), out, target, serverURL) {
			return fmt.Errorf("deploy aborted")
		}
	}
	o.yes = true // confirmed once for the whole workspace; no per-project prompts
	var failed []string
	for _, p := range ws.Projects {
		devPrintf(out, "==> deploying %s (%s)\n", p.DagID, p.Path)
		if derr := runDeploy(cmd, p.Path, o); derr != nil {
			failed = append(failed, p.DagID)
			devPrintf(errOut, "    FAILED %s: %v\n", p.DagID, derr)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d deploys failed: %s",
			len(failed), len(ws.Projects), strings.Join(failed, ", "))
	}
	_, err = fmt.Fprintf(out, "Deployed %d DAG(s) from %s\n", len(ws.Projects), wsDir)
	return err
}

// requireRegistry enforces ADR 0041's hard prerequisite: a Pro deploy pushes the
// DAG image to a registry the cluster can pull from, so a configured registry is
// mandatory. It returns a loud, actionable error (never a silent attempt) when
// the registry URL or image name is missing — Lite runs locally and needs none,
// but deploy does, full stop.
func requireRegistry(cfg *domain.LeoflowConfig) error {
	if cfg != nil && cfg.Registry != nil &&
		strings.TrimSpace(cfg.Registry.URL) != "" && strings.TrimSpace(cfg.Registry.ImageName) != "" {
		return nil
	}
	return fmt.Errorf(`deploy requires a container registry, but none is configured.
  A Pro deploy pushes the DAG image to a registry your cluster can pull from
  (Lite runs locally and needs none). Add to leoflow.yaml:

      registry:
        url: ghcr.io/<your-org>     # or ECR / Artifact Registry / ACR / private
        image_name: <name>

  Then authenticate your builder once:  docker login ghcr.io`)
}

// repinImageInSpec rewrites the compiled dag.json's image field to the
// digest-pinned reference, preserving every other field, so Pro pulls exactly
// the bytes that were built (ADR 0003 + 0041). It round-trips through a generic
// map so unknown/future fields survive untouched.
func repinImageInSpec(data []byte, digestRef string) ([]byte, error) {
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing compiled dag.json: %w", err)
	}
	spec["image"] = digestRef
	out, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encoding re-pinned dag.json: %w", err)
	}
	return out, nil
}

// deployImageRef composes the pushed image reference for a project from its
// registry config, the resolved version, and the git sha, applying the tag
// strategy (so tag_strategy: git_sha yields a genuine commit tag, not the
// version label).
func deployImageRef(cfg *domain.LeoflowConfig, version, sha string) string {
	tag := resolveImageTag(cfg.Registry.TagStrategy, version, sha)
	return composeImageRef(cfg.Registry.URL, cfg.Registry.ImageName, tag)
}

// resolveServerToken applies the deploy auth precedence: --server/--token, then
// LEOFLOW_* env (via the token flag default), then the persisted config written
// by `leoflow auth login`.
//
// When the token comes from that persisted session file (not a --token flag or
// LEOFLOW_TOKEN env, which are caller- or CI-managed), it is transparently
// refreshed if near expiry and the file is rewritten with the fresh token (aresta
// #5), so a long dev session never re-logs in every hour. The refresh is
// best-effort: on any failure the current token is returned unchanged.
func resolveServerToken(cmd *cobra.Command, serverFlag, tokenFlag string) (serverURL, token string, err error) {
	serverURL, token = serverFlag, tokenFlag
	tokenFromSession := false
	configuredServer := ""
	if serverURL == "" || token == "" {
		cfg, cerr := config.Load(configFilePath(cmd), cmd.Flags())
		if cerr != nil {
			return "", "", cerr
		}
		configuredServer = cfg.ServerURL
		if serverURL == "" {
			serverURL = cfg.ServerURL
		}
		if token == "" {
			token = cfg.Token
			tokenFromSession = true
		}
	}
	// Only auto-refresh a file-persisted session token: a --token flag or a
	// LEOFLOW_TOKEN env var (which config.Load also surfaces as cfg.Token) is
	// externally managed and must never be silently rewritten.
	if tokenFromSession && os.Getenv("LEOFLOW_TOKEN") == "" {
		token = autoRefreshToken(cmdContext(cmd), configFilePath(cmd), serverURL, token)
	}
	// Record what was decided so a 401 can say whether the token belongs to the
	// server being called (#1102). LEOFLOW_TOKEN is externally supplied, so it is
	// not "from config" even though config.Load surfaces it as cfg.Token.
	resolvedTarget.server = serverURL
	resolvedTarget.configured = configuredServer
	resolvedTarget.fromConfig = tokenFromSession && os.Getenv("LEOFLOW_TOKEN") == ""
	return serverURL, token, nil
}

// runDeploy orchestrates the pipeline-less promotion: validate the project,
// enforce the registry, build+push the image for the target platform, re-pin it
// by digest into dag.json, and register that with the control plane.
func runDeploy(cmd *cobra.Command, dir string, o deployOptions) error {
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		return err
	}
	if verr := cfg.Validate(); verr != nil {
		return fmt.Errorf("invalid %s: %w", projectConfigPath(dir), verr)
	}
	if rerr := requireRegistry(cfg); rerr != nil {
		return rerr
	}

	serverURL, token, serr := resolveServerToken(cmd, o.serverURL, o.token)
	if serr != nil {
		return serr
	}
	if shouldConfirm(serverURL, o.yes, cmdInteractive(cmd)) &&
		!confirmDeploy(cmd.InOrStdin(), cmd.OutOrStdout(), dir, serverURL) {
		return fmt.Errorf("deploy aborted")
	}

	version := o.dagVersion
	if version == "" {
		version = gitVersion(cmdContext(cmd))
	}
	image := deployImageRef(cfg, version, gitSHA(cmdContext(cmd)))

	output := filepath.Join(dir, "dag.json")
	co := compileOptions{
		output:     output,
		image:      image,
		builder:    o.builder,
		dockerfile: o.dockerfile,
		dagVersion: version,
		build:      !o.skipBuild,
		push:       !o.skipBuild,
	}
	if cerr := runCompile(cmd, dir, co); cerr != nil {
		return cerr
	}

	digestRef, derr := imageDigest(cmd, o.builder, image)
	if derr != nil {
		return derr
	}
	ctx, out := cmdContext(cmd), cmd.OutOrStdout()
	dagID, rerr := registerDeployedDAG(ctx, out, serverURL, token, output, digestRef, version)
	if rerr != nil {
		return rerr
	}
	surfaceConnections(out, output)
	if o.trigger {
		return triggerDeployRun(ctx, out, serverURL, token, dagID)
	}
	return nil
}

// registerDeployedDAG re-pins the compiled dag.json to the pushed image's digest
// and registers it with the control plane, then prints a per-phase summary. It
// is split from runDeploy so the post-build flow (which needs no builder) is
// unit-testable against a fake control plane.
func registerDeployedDAG(ctx context.Context, out io.Writer, serverURL, token, output, digestRef, version string) (dagID string, err error) {
	data, rerr := os.ReadFile(output) //nolint:gosec // output path is operator-controlled (the DAG dir).
	if rerr != nil {
		return "", fmt.Errorf("reading %s: %w", output, rerr)
	}
	repinned, perr := repinImageInSpec(data, digestRef)
	if perr != nil {
		return "", perr
	}
	if werr := os.WriteFile(output, repinned, 0o600); werr != nil {
		return "", fmt.Errorf("writing %s: %w", output, werr)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(repinned, &spec); jerr != nil {
		return "", fmt.Errorf("parsing compiled dag.json: %w", jerr)
	}
	status, body, uerr := pushVersion(ctx, serverURL, token, spec.DagID, repinned)
	if uerr != nil {
		return "", uerr
	}
	if status >= http.StatusMultipleChoices {
		return "", apiStatusError(status, []byte(body))
	}
	if _, werr := fmt.Fprintf(out,
		"Deployed %s -> %s\n  image %s\n  registered version %s\n", spec.DagID, serverURL, digestRef, version); werr != nil {
		return "", werr
	}
	return spec.DagID, nil
}

// triggerDeployRun kicks a manual run of the just-registered DAG, the --trigger
// opt-in (deploy registers only by default). It posts to the Airflow-compatible
// trigger endpoint with a unique manual run id.
func triggerDeployRun(ctx context.Context, out io.Writer, serverURL, token, dagID string) error {
	runID := fmt.Sprintf("manual__%d", time.Now().UTC().Unix())
	payload := []byte(fmt.Sprintf(`{"dag_run_id":%q}`, runID))
	url := strings.TrimRight(serverURL, "/") + "/api/v2/dags/" + dagID + "/dagRuns"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building trigger request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("triggering run for %q: %w", dagID, err)
	}
	raw, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return fmt.Errorf("reading trigger response: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing trigger response: %w", closeErr)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("trigger returned %d: %s", resp.StatusCode, string(raw))
	}
	_, err = fmt.Fprintf(out, "  triggered run %s\n", runID)
	return err
}
