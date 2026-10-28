package cli

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/version"
)

// ciScriptPath is where `ci init` writes the deploy script, relative to the
// top of the repository. Every pipeline it writes runs this script, so the
// deploy logic lives in one file whatever the CI system.
const ciScriptPath = "ci/dexaflow-deploy.sh"

// ciUVVersion is the uv the script installs when the runner has none, so
// dependency images are built from wheels without a container daemon.
const ciUVVersion = "0.12.24"

// ciUVHashes are the sha256 of the uv wheels for Linux on x86_64 and
// aarch64, glibc and musl, from PyPI: the script installs uv with
// --require-hashes, so a replaced wheel is refused.
var ciUVHashes = []string{
	"e47005957dca320272ad68960b591b854b6bf0c3288368efb096d9ccd06b8404", // manylinux_2_17_x86_64
	"94c9ed22c1b5d6861a72639e23c5707bb07a354b67ec0462e9a0169a9665b5fd", // manylinux_2_17_aarch64, musllinux_1_1_aarch64
	"0fc14fabb78c55d140a01125d5cd038afb4fed317eb0c3868f3fba87b8cefe16", // manylinux_2_28_aarch64
	"b6fe9cd01f5c576a640f17829cb465899fffb5d6d8dfdd576e38d4f1e40dd438", // musllinux_1_1_x86_64
}

// The images the pipelines run on, pinned by digest so a moved tag does
// not change what runs. `ci init --force` after a CLI upgrade picks up the
// digests that CLI pins.
const (
	ciPythonImage   = "python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1"
	ciDockerImage   = "docker:27@sha256:aa3df78ecf320f5fafdce71c659f1629e96e9de0968305fe1de670e0ca9176ce"
	ciDindImage     = "docker:27-dind@sha256:aa3df78ecf320f5fafdce71c659f1629e96e9de0968305fe1de670e0ca9176ce"
	ciCloudSDKImage = "gcr.io/google.com/cloudsdktool/cloud-sdk:slim@sha256:ffd36b9b80bb08b2dea60b58ca2b60dfa3a40a3c00f3e5732d6d113f4e7ffa45"
)

// ciMinCLIVersion is the first release whose install.sh reads every
// variable the script sets (DEXAFLOW_VERSION, DEXAFLOW_NO_SETUP,
// DEXAFLOW_NO_PATH, DEXAFLOW_INSTALL_DIR).
const ciMinCLIVersion = "v0.5.0"

// ciActionMinVersion is the first release that ships the setup-dexaflow
// action the GitHub workflow installs the CLI with.
const ciActionMinVersion = "v0.5.4"

// ciOIDCMinVersion is the first release whose CLI has `dexaflow auth
// ci-token` and whose server serves the CI OIDC exchange (ADR 0092): a
// pipeline that installs an older CLI keeps the deploy token.
const ciOIDCMinVersion = "v0.5.5"

// ciCLIImageRepo is the published image of the CLI, which the GitLab job
// runs in from the release that first ships it.
const ciCLIImageRepo = "ghcr.io/dexadata/dexaflow-cli"

// ciCLIImageMinVersion is the first release that publishes ciCLIImageRepo.
const ciCLIImageMinVersion = "v0.5.4"

// lookupImageDigest asks the registry of image (a tag reference) for the
// digest it names, with the CLI's registry client, which computes it from
// the manifest returned. The dexaflow-cli package is public, so no
// credentials are needed.
func lookupImageDigest(ctx context.Context, image string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pinned, err := build.Registry{}.Resolve(ctx, image)
	if err != nil {
		return "", err
	}
	_, digest, _ := strings.Cut(pinned.Ref, "@")
	return digest, nil
}

// ciCLIImage is the dexaflow-cli image of cliVersion, pinned by its tag
// and its digest, that the GitLab job runs in; "" for a release older than
// the image, whose job installs the CLI on the Python image.
func ciCLIImage(ctx context.Context, cliVersion string) (string, error) {
	if semverLess(cliVersion, ciCLIImageMinVersion) {
		return "", nil
	}
	image := ciCLIImageRepo + ":" + cliVersion
	digest, err := depsFrom(ctx).imageDigest(ctx, image)
	if err != nil {
		return "", fmt.Errorf("finding the digest of %s, to pin the image the GitLab job runs in: %w", image, err)
	}
	if !ciDigestRe.MatchString(digest) {
		return "", fmt.Errorf("finding the digest of %s, to pin the image the GitLab job runs in: %q is not a sha256 digest", image, digest)
	}
	return image + "@" + digest, nil
}

// ciDigestRe matches a sha256 image digest.
var ciDigestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ciReleaseRepo is the repository the releases, and the action, come from.
const ciReleaseRepo = "https://github.com/dexadata/dexaflow.git"

// ciCommitRe matches a full commit SHA.
var ciCommitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// lookupTagCommit finds the commit release tag points to, for pinning the
// setup-dexaflow action by commit. It asks the repository with git
// ls-remote: the peeled entry of an annotated tag, else the tag itself.
func lookupTagCommit(ctx context.Context, tag string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	//nolint:gosec // G204: fixed git binary; tag matched ciReleaseRe.
	c := exec.CommandContext(ctx, "git", "ls-remote", "--tags", ciReleaseRepo, "refs/tags/"+tag, "refs/tags/"+tag+"^{}")
	c.Env = gitEnv(os.Environ())
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s: %w", ciReleaseRepo, err)
	}
	return tagCommitIn(string(out), tag)
}

// tagCommitIn reads the commit of tag from git ls-remote output: the peeled
// entry (tag^{}) of an annotated tag, else the entry of a lightweight one.
func tagCommitIn(out, tag string) (string, error) {
	var plain, peeled string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		sha, ref, ok := strings.Cut(line, "\t")
		switch {
		case !ok || !ciCommitRe.MatchString(sha):
		case ref == "refs/tags/"+tag+"^{}":
			peeled = sha
		case ref == "refs/tags/"+tag:
			plain = sha
		}
	}
	if peeled != "" {
		return peeled, nil
	}
	if plain == "" {
		return "", fmt.Errorf("%s has no tag %s", ciReleaseRepo, tag)
	}
	return plain, nil
}

// ciActionCommit is the commit the workflow pins the setup-dexaflow action
// to: this CLI's own commit when it is the release being pinned, else the
// commit the release tag points to.
func ciActionCommit(ctx context.Context, cliVersion string) (string, error) {
	if semverLess(cliVersion, ciActionMinVersion) {
		return "", fmt.Errorf("the GitHub workflow installs the CLI with the setup-dexaflow action, which ships from %s; "+
			"pass --cli-version %s or later (%s is older)", ciActionMinVersion, ciActionMinVersion, cliVersion)
	}
	info := version.Get()
	if "v"+strings.TrimPrefix(info.Version, "v") == cliVersion && ciCommitRe.MatchString(info.GitCommit) {
		return info.GitCommit, nil
	}
	sha, err := depsFrom(ctx).tagCommit(ctx, cliVersion)
	if err != nil {
		return "", fmt.Errorf("finding the commit of %s, to pin the setup-dexaflow action by: %w", cliVersion, err)
	}
	return sha, nil
}

// ciPythonVersion is the Python the pipelines put on the runner for the
// parser (3.11 to 3.13).
const ciPythonVersion = "3.12"

// ciSystems lists the systems `ci init` writes a pipeline for, in the order
// the help names them.
var ciSystems = []string{"github", "gitlab", "cloudbuild", "generic"}

// ciPipelineFile is the file each system reads its pipeline from; generic
// has none, it is the script alone.
var ciPipelineFile = map[string]string{
	"github":     ".github/workflows/dexaflow-deploy.yaml",
	"gitlab":     ".gitlab-ci.yml",
	"cloudbuild": "cloudbuild.yaml",
}

// ciTemplates are the pipeline templates. They use [[ ]] as delimiters,
// since GitHub expressions are written ${{ }}.
//
//go:embed ci_templates/*.tmpl
var ciTemplates embed.FS

// ciReleaseRe matches a release tag the install script can download.
var ciReleaseRe = regexp.MustCompile(`^v\d+\.\d+\.\d+(-rc\.\d+)?$`)

// ciBranchRe is what a branch name may look like in a generated pipeline,
// which writes it into YAML and shell without quoting it again.
var ciBranchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// ciEnvironmentRe is a GitHub environment name the workflow can write
// without quoting.
var ciEnvironmentRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

// ciHostRe is a registry host the pipelines can name without quoting.
var ciHostRe = regexp.MustCompile(`^[A-Za-z0-9.-]+(:\d+)?$`)

// ciECRRe matches an ECR registry host and captures its region.
var ciECRRe = regexp.MustCompile(`^\d+\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com$`)

// ciInitOptions holds the flags of `ci init`.
type ciInitOptions struct {
	force       bool
	stdout      bool
	branch      string
	cliVersion  string
	environment string
	auth        string
}

// ciRegistry is one registry the workspace pushes to.
type ciRegistry struct {
	// Host is the registry host, such as ghcr.io.
	Host string
	// Region is the AWS region of an ECR host.
	Region string
}

// ciPlan is what a pipeline template is rendered from.
type ciPlan struct {
	System     string
	CLIVersion string
	// Branch is the branch whose pushes deploy.
	Branch string
	// BranchSet is set when --branch named it; GitLab otherwise follows
	// the project's default branch.
	BranchSet bool
	// Environment is the GitHub environment the job runs in.
	Environment string
	// OIDC is the issuer entry a tokenless pipeline signs in through, nil
	// for the deploy token path (ADR 0092).
	OIDC *ciOIDC
	// CLICommit is the commit of CLIVersion, which the GitHub workflow pins
	// the setup-dexaflow action to.
	CLICommit string
	// CLIImage is the dexaflow-cli image of CLIVersion, by tag and digest,
	// that the GitLab job runs in when no project needs a daemon; "" for a
	// release without the image.
	CLIImage string
	// MinCLIVersion is ciMinCLIVersion, for the script's help.
	MinCLIVersion string
	// UVHashes are the hashes the script installs uv with.
	UVHashes []string
	// The images the pipelines run on, by digest.
	PythonImage, DockerImage, DindImage, CloudSDKImage string
	ScriptPath                                         string
	UVVersion                                          string
	PythonVersion                                      string
	// WorkspaceDir is the workspace folder relative to the top of the
	// repository, "." when they are the same.
	WorkspaceDir string
	// Target is the workspace target the pipeline deploys to, "" when the
	// pipeline reads DEXAFLOW_SERVER_URL.
	Target string
	// GHCR is set when a project pushes to ghcr.io.
	GHCR bool
	// GCloud is set when the job needs a Google Cloud session: an Artifact
	// Registry or GCR image, or a Cloud Build remote builder.
	GCloud bool
	// Remote is set when a project has a remote builder.
	Remote bool
	// ECR lists the ECR registries, one per region.
	ECR []ciRegistry
	// Others are the registries GitHub logs in to with a stored credential.
	Others []ciRegistry
	// GitLabOthers are the registries other than GitLab's own.
	GitLabOthers []ciRegistry
	// NonGoogle are the registries a Cloud Build service account cannot
	// push to.
	NonGoogle []ciRegistry
	// Unknown is set when no project names a registry.
	Unknown bool
	// Daemon says, per project, why its image needs a container daemon:
	// a real build with no remote builder configured.
	Daemon []string
}

// ciFile is one file `ci init` writes.
type ciFile struct {
	path    string
	content []byte
	mode    os.FileMode
}

// newCICommand builds `dexaflow ci`, the commands that set up a CI system to
// deploy the workspace.
func newCICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Set up a CI pipeline that deploys the workspace.",
	}
	cmd.AddCommand(newCIInitCommand())
	return cmd
}

// newCIInitCommand builds `dexaflow ci init <system>`.
func newCIInitCommand() *cobra.Command {
	var o ciInitOptions
	cmd := &cobra.Command{
		Use:   "init <" + strings.Join(ciSystems, "|") + ">",
		Short: "Write a pipeline that deploys the DAGs a push changed.",
		Long: "Writes a deploy pipeline for a CI system at the top of the repository:\n" +
			"  github      .github/workflows/dexaflow-deploy.yaml\n" +
			"  gitlab      .gitlab-ci.yml\n" +
			"  cloudbuild  cloudbuild.yaml\n" +
			"  generic     only " + ciScriptPath + "\n" +
			"and " + ciScriptPath + ", which every pipeline runs. The pipeline installs this\n" +
			"version of the CLI (on GitLab it runs in the dexaflow-cli image of that\n" +
			"release, pinned by digest), logs in to the workspace's registry the way the\n" +
			"CI system does natively, and runs `dexaflow deploy --all --changed-since <before>` on\n" +
			"each push to --branch. It reads the control plane from DEXAFLOW_SERVER_URL,\n" +
			"or from the workspace target (--target, else default_target), and a deploy\n" +
			"token from the CI secret DEXAFLOW_TOKEN:\n" +
			"  dexaflow auth create-token --scope deploy --name ci-<repo> --ttl 90d\n" +
			"When that control plane offers a CI OIDC issuer for the system (auth.ci_oidc),\n" +
			"the pipeline instead exchanges the job's OIDC token for a short-lived session\n" +
			"(`dexaflow auth ci-token`), and no token is stored; --auth picks the path.\n" +
			"An existing file is never overwritten without --force.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCIInit(cmd, args[0], o)
		},
	}
	cmd.Flags().BoolVar(&o.force, "force", false, "overwrite the files that already exist")
	cmd.Flags().BoolVar(&o.stdout, "stdout", false, "print the files instead of writing them")
	cmd.Flags().StringVar(&o.branch, "branch", "main", "the branch whose pushes deploy")
	cmd.Flags().StringVar(&o.cliVersion, "cli-version", "", "the CLI release the pipeline installs, such as v0.6.0, "+ciMinCLIVersion+" or later (default: this CLI's version)")
	cmd.Flags().StringVar(&o.environment, "environment", "production", "github: the environment the deploy job runs in, whose secrets and protection rules apply")
	cmd.Flags().StringVar(&o.auth, "auth", ciAuthAuto, "how the pipeline signs in: auto (the job's OIDC token when the server offers an issuer for the system, else a deploy token), oidc or token")
	return cmd
}

// runCIInit plans the pipeline for system, renders it and writes it (or
// prints it with --stdout).
func runCIInit(cmd *cobra.Command, system string, o ciInitOptions) error {
	if !slices.Contains(ciSystems, system) {
		return fmt.Errorf("unknown CI system %q; ci init knows %s", system, strings.Join(ciSystems, ", "))
	}
	cliVersion, err := ciCLIVersion(o.cliVersion)
	if err != nil {
		return err
	}
	if !ciBranchRe.MatchString(o.branch) || strings.Contains(o.branch, "..") {
		return fmt.Errorf("--branch %q is not a branch name a pipeline can use (letters, digits, '.', '_', '-' and '/')", o.branch)
	}
	if !ciEnvironmentRe.MatchString(o.environment) {
		return fmt.Errorf("--environment %q is not an environment name a workflow can use (letters, digits, '.', '_' and '-')", o.environment)
	}
	ws, err := findWorkspaceFromCwd()
	if err != nil {
		return err
	}
	if ws == nil {
		return fmt.Errorf("ci init deploys a workspace, and there is no %s here or above; create one with `dexaflow workspace init`", workspaceFileName)
	}
	top := gitRootOf(cmdContext(cmd), ws.Root)
	if top == "" {
		top = ws.Root
	}
	plan, err := planCI(cmd, ws, top, system, cliVersion, o.branch)
	if err != nil {
		return err
	}
	plan.BranchSet = cmd.Flags().Changed("branch")
	plan.Environment = o.environment
	if plan.OIDC, err = chooseCIAuth(cmd, system, cliVersion, o.auth); err != nil {
		return err
	}
	if pinErr := plan.pinInstall(cmdContext(cmd)); pinErr != nil {
		return pinErr
	}
	files, err := renderCI(plan)
	if err != nil {
		return err
	}
	if o.stdout {
		return printCIFiles(cmd.OutOrStdout(), files)
	}
	return writeCIFiles(cmd.OutOrStdout(), top, files, plan, o.force)
}

// pinInstall pins how the pipeline gets the CLI where that is pinned by
// what a remote holds: the commit of the setup-dexaflow action on GitHub,
// the digest of the dexaflow-cli image on GitLab when the job needs no
// daemon.
func (p *ciPlan) pinInstall(ctx context.Context) error {
	var err error
	switch {
	case p.System == "github":
		p.CLICommit, err = ciActionCommit(ctx, p.CLIVersion)
	case p.System == "gitlab" && len(p.Daemon) == 0:
		p.CLIImage, err = ciCLIImage(ctx, p.CLIVersion)
	}
	return err
}

// ciCLIVersion is the release tag the pipeline installs: --cli-version, or
// this CLI's own version when it is a release.
func ciCLIVersion(flag string) (string, error) {
	if flag != "" {
		if !ciReleaseRe.MatchString(flag) {
			return "", fmt.Errorf("--cli-version %q is not a release tag such as v0.6.0", flag)
		}
		if semverLess(flag, ciMinCLIVersion) {
			return "", fmt.Errorf("--cli-version %s is older than %s, the first release whose install.sh reads the variables the pipeline sets", flag, ciMinCLIVersion)
		}
		return flag, nil
	}
	v := "v" + strings.TrimPrefix(version.Get().Version, "v")
	if !ciReleaseRe.MatchString(v) {
		return "", fmt.Errorf("this CLI is not a release (version %s), so the pipeline cannot install it; "+
			"pass the release to install with --cli-version vX.Y.Z", version.Get().Version)
	}
	return v, nil
}

// semverLess reports whether release tag a is older than b, comparing
// major, minor and patch; a release candidate is older than its release.
func semverLess(a, b string) bool {
	parse := func(v string) (nums [3]int, rc int) {
		core, pre, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-rc.")
		for i, f := range strings.SplitN(core, ".", 3) {
			nums[i], _ = strconv.Atoi(f) //nolint:errcheck // ciReleaseRe matched digits
		}
		rc = math.MaxInt
		if pre != "" {
			rc, _ = strconv.Atoi(pre) //nolint:errcheck // ciReleaseRe matched digits
		}
		return nums, rc
	}
	an, ar := parse(a)
	bn, br := parse(b)
	for i := range an {
		if an[i] != bn[i] {
			return an[i] < bn[i]
		}
	}
	return ar < br
}

// planCI gathers what the templates need from the workspace: where it is in
// the repository, the target the pipeline deploys to, the registries its
// projects push to and the projects that need a container daemon.
func planCI(cmd *cobra.Command, ws *workspaceLocation, top, system, cliVersion, branch string) (*ciPlan, error) {
	rel, err := filepath.Rel(top, ws.Root)
	if err != nil {
		return nil, fmt.Errorf("placing the workspace in the repository: %w", err)
	}
	plan := &ciPlan{System: system, CLIVersion: cliVersion, Branch: branch, ScriptPath: ciScriptPath,
		UVVersion: ciUVVersion, UVHashes: ciUVHashes, MinCLIVersion: ciMinCLIVersion, PythonVersion: ciPythonVersion, WorkspaceDir: filepath.ToSlash(rel),
		PythonImage: ciPythonImage, DockerImage: ciDockerImage, DindImage: ciDindImage, CloudSDKImage: ciCloudSDKImage}
	if !ciBranchRe.MatchString(plan.WorkspaceDir) && plan.WorkspaceDir != "." {
		return nil, fmt.Errorf("the workspace folder %q has characters a pipeline would have to quote", plan.WorkspaceDir)
	}
	if plan.Target, err = ciTarget(cmd); err != nil {
		return nil, err
	}
	spec, err := ResolveWorkspace(ws.Root)
	if err != nil {
		return nil, err
	}
	if len(spec.Projects) == 0 {
		return nil, fmt.Errorf("no DAG projects found in workspace %s; add one with `dexaflow new <dag_id>`", ws.Root)
	}
	hosts, err := plan.addProjects(cmd, spec.Projects)
	if err != nil {
		return nil, err
	}
	plan.addRegistries(cmd.ErrOrStderr(), hosts)
	plan.GCloud = plan.GCloud || plan.Remote
	return plan, nil
}

// addProjects reads each project with the workspace and target defaults
// applied, notes the ones that need a container daemon or have a remote
// builder, and returns the registry hosts they push to.
func (p *ciPlan) addProjects(cmd *cobra.Command, projects []Project) (map[string]bool, error) {
	hosts := map[string]bool{}
	for _, pr := range projects {
		cfg := pr.Config
		if _, err := applyWorkspaceDefaults(cmd, pr.Path, cfg, true); err != nil {
			return nil, fmt.Errorf("%s: %w", pr.DagID, err)
		}
		if cfg.Registry != nil && cfg.Registry.URL != "" {
			hosts[strings.SplitN(cfg.Registry.URL, "/", 2)[0]] = true
		}
		if cfg.Build != nil && cfg.Build.RemoteBuilder != nil && cfg.Build.RemoteBuilder.CloudBuild != nil {
			p.Remote = true
			continue
		}
		if why := ciDaemonReason(pr.Path, cfg); why != "" {
			p.Daemon = append(p.Daemon, pr.DagID+": "+why)
		}
	}
	return hosts, nil
}

// ciTarget is the target the pipeline deploys to: the one --target,
// DEXAFLOW_TARGET or the workspace selects, when the workspace file defines
// it. A target of the user's config file is not in the repository, so the
// pipeline cannot use it.
func ciTarget(cmd *cobra.Command) (string, error) {
	tc, err := loadTargetContext(cmd)
	if err != nil {
		return "", err
	}
	t, err := tc.selectTarget(cmd)
	if err != nil || t == nil {
		return "", err
	}
	if t.Scope != scopeWorkspace {
		//nolint:errcheck // advisory
		fmt.Fprintf(cmd.ErrOrStderr(), "note: target %s is in your config file, not in %s, so the pipeline reads DEXAFLOW_SERVER_URL instead\n", t.Name, workspaceFileName)
		return "", nil
	}
	return t.Name, nil
}

// ciDaemonReason says why the image of the project at dir needs a real
// build, which only the remote builder or a container daemon does; "" when
// a daemonless path applies.
func ciDaemonReason(dir string, cfg *domain.LeoflowConfig) string {
	dockerfile := "Dockerfile"
	if cfg.Build != nil && cfg.Build.Dockerfile != "" {
		dockerfile = cfg.Build.Dockerfile
	}
	if why := splitBlocker(dir, dockerfile, cfg); why != "" {
		return why
	}
	deps, err := cfg.EffectiveDependencies()
	if err != nil {
		return ""
	}
	platforms := buildPlatforms(cfg)
	if len(platforms) == 0 {
		platforms = []string{"linux/amd64"}
	}
	// The pipeline runs on a Linux runner, whatever this machine is.
	return wheelBlockerOn("linux", cfg, deps, platforms)
}

// addRegistries sorts the registry hosts into the logins each system
// writes, and notes a host the pipelines cannot name.
func (p *ciPlan) addRegistries(w io.Writer, hosts map[string]bool) {
	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}
	slices.Sort(names)
	p.Unknown = len(names) == 0
	regions := map[string]bool{}
	for _, h := range names {
		if !ciHostRe.MatchString(h) {
			//nolint:errcheck // advisory
			fmt.Fprintf(w, "note: the registry host %q is not written into the pipeline; add its login by hand\n", h)
			continue
		}
		google := h == "gcr.io" || strings.HasSuffix(h, ".gcr.io") || strings.HasSuffix(h, "-docker.pkg.dev")
		if !google {
			p.NonGoogle = append(p.NonGoogle, ciRegistry{Host: h})
		}
		if h != "registry.gitlab.com" {
			p.GitLabOthers = append(p.GitLabOthers, ciRegistry{Host: h})
		}
		switch m := ciECRRe.FindStringSubmatch(h); {
		case h == "ghcr.io":
			p.GHCR = true
		case google:
			p.GCloud = true
		case m != nil:
			if !regions[m[1]] {
				regions[m[1]] = true
				p.ECR = append(p.ECR, ciRegistry{Host: h, Region: m[1]})
			}
		default:
			p.Others = append(p.Others, ciRegistry{Host: h})
		}
	}
}

// renderCI renders the files of plan's system: its pipeline file, when it
// has one, and the script.
func renderCI(plan *ciPlan) ([]ciFile, error) {
	var files []ciFile
	if path, ok := ciPipelineFile[plan.System]; ok {
		data, err := renderCITemplate(plan.System+".yaml.tmpl", plan)
		if err != nil {
			return nil, err
		}
		files = append(files, ciFile{path: path, content: data, mode: 0o644})
	}
	script, err := renderCITemplate("deploy.sh.tmpl", plan)
	if err != nil {
		return nil, err
	}
	return append(files, ciFile{path: ciScriptPath, content: script, mode: 0o755}), nil
}

// renderCITemplate executes one embedded template with plan.
func renderCITemplate(name string, plan *ciPlan) ([]byte, error) {
	t, err := template.New(name).Delims("[[", "]]").Option("missingkey=error").ParseFS(ciTemplates, "ci_templates/"+name)
	if err != nil {
		return nil, fmt.Errorf("parsing the %s template: %w", name, err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, plan); err != nil {
		return nil, fmt.Errorf("rendering %s: %w", name, err)
	}
	return b.Bytes(), nil
}

// printCIFiles writes each file to w under a line naming its path.
func printCIFiles(w io.Writer, files []ciFile) error {
	for i, f := range files {
		sep := ""
		if i > 0 {
			sep = "\n"
		}
		if _, err := fmt.Fprintf(w, "%s# file: %s\n%s", sep, f.path, f.content); err != nil {
			return err
		}
	}
	return nil
}

// writeCIFiles writes files under top. Without force it first refuses any
// file that exists with other content, naming it, so a refusal writes
// nothing; a file that already holds what would be written is left alone.
func writeCIFiles(w io.Writer, top string, files []ciFile, plan *ciPlan, force bool) error {
	var pending []ciFile
	for _, f := range files {
		p := filepath.Join(top, filepath.FromSlash(f.path))
		existing, err := os.ReadFile(p) //nolint:gosec // a file of the user's repository, named by ci init
		switch {
		case err == nil && bytes.Equal(existing, f.content):
			continue
		case err == nil && !force:
			return ciExistsError(f.path)
		case err != nil && !os.IsNotExist(err):
			return fmt.Errorf("reading %s: %w", p, err)
		}
		pending = append(pending, f)
	}
	for _, f := range pending {
		p := filepath.Join(top, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(p), err)
		}
		if err := writeFileAtomic(p, f.content); err != nil {
			return err
		}
		if err := os.Chmod(p, f.mode); err != nil { //nolint:gosec // the script must be executable, the pipeline readable
			return fmt.Errorf("setting the mode of %s: %w", p, err)
		}
		//nolint:errcheck // informational
		fmt.Fprintf(w, "wrote %s\n", f.path)
	}
	return printCINext(w, plan)
}

// ciExistsError is the refusal to overwrite path.
func ciExistsError(path string) error {
	if path == ".gitlab-ci.yml" {
		return fmt.Errorf("%s already exists and holds the project's other jobs: merge the dexaflow-deploy job into it by hand "+
			"(print it with `dexaflow ci init gitlab --stdout`), or pass --force to replace the file", path)
	}
	return fmt.Errorf("%s already exists with other content; pass --force to overwrite it, or --stdout to compare", path)
}

// printCINext says what to set up in the CI system before the first push.
func printCINext(w io.Writer, plan *ciPlan) error {
	var b strings.Builder
	b.WriteString("\nNext:\n")
	if o := plan.OIDC; o != nil {
		fmt.Fprintf(&b, "  1. no token to store: the job signs in with its OIDC token (audience %s),\n", o.Audience)
		fmt.Fprintf(&b, "     which %s trusts through its CI OIDC issuer %s\n", o.Server, o.Name)
	} else {
		b.WriteString("  1. create a deploy token, and store it as the CI secret DEXAFLOW_TOKEN:\n")
		b.WriteString("       dexaflow auth create-token --scope deploy --name ci-<repo> --ttl 90d\n")
	}
	if plan.Target != "" {
		fmt.Fprintf(&b, "  2. the pipeline deploys to target %s; commit %s with it\n", plan.Target, workspaceFileName)
	} else {
		b.WriteString("  2. set DEXAFLOW_SERVER_URL in the CI system to the control plane URL\n")
	}
	if len(plan.Daemon) > 0 {
		b.WriteString("  note: these projects need a container daemon, or a remote builder (build.remote_builder):\n")
		for _, d := range plan.Daemon {
			fmt.Fprintf(&b, "    - %s\n", d)
		}
	}
	fmt.Fprintf(&b, "  3. commit the files and push to %s\n", plan.Branch)
	_, err := io.WriteString(w, b.String())
	return err
}
