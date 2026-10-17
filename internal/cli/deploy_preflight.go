package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dexadata/dexaflow/internal/build"
)

// The preflight of a deploy (#1705) asks, in seconds and before anything is
// built, whether the deploy would be refused: the control plane and the
// token before the compile, the registry push before the build, and the
// server's dry run of the register once the spec is compiled. A refusal
// stops the deploy and names what to fix; what cannot be checked is a note
// and the deploy goes on, as it did before the preflight.

// dryRunHeader is the header a server sets on every answer of the register
// dry run route past its permission check, refusals included (internal/api).
const dryRunHeader = "Dexaflow-Dry-Run"

// deployViaHeader is the header of a deploy the server refuses because it
// takes deploys only through its trusted issuer (#1773), with the value
// deployViaIssuer.
const (
	deployViaHeader = "Dexaflow-Deploy-Via"
	deployViaIssuer = "trusted-issuer"
)

// deployViaIssuerError explains a 403 that says the control plane takes
// deploys only through its trusted issuer (auth.trusted_issuer.deploy_audience,
// #1773): no role, scope or API token of this CLI can deploy there. It returns
// nil for any other answer.
func deployViaIssuerError(serverURL string, status int, header http.Header, body []byte) error {
	if status != http.StatusForbidden || header.Get(deployViaHeader) != deployViaIssuer {
		return nil
	}
	return fmt.Errorf("the control plane at %s takes deploys only through its trusted issuer: %w\n"+
		"  A session, a password login or an API token cannot deploy there, whatever its roles and scopes.\n"+
		"  Deploy through the platform that owns deployments, or pass (--token or DEXAFLOW_TOKEN) a bearer token\n"+
		"  the issuer minted for the server's deploy audience with the dexaflow:deploy scope", serverURL, apiStatusError(status, body))
}

// preflightTimeout bounds each preflight check of a deploy.
const preflightTimeout = 30 * time.Second

// placeholderDigest stands in for the digest of an image not built yet, so
// the dry run sees the image in the shape the register will:
// <repository>@sha256:<digest>.
var placeholderDigest = "sha256:" + strings.Repeat("0", 64)

// serverAccess is what the access check found out about the control plane.
type serverAccess struct {
	// dryRun is true when the server serves the register dry run.
	dryRun bool
}

// pausedScopeHint is what a refusal adds when the deploy sends is_paused.
const pausedScopeHint = "\n  --paused and --active set is_paused, which also needs write on dags and, for an API token, the dexaflow:run scope besides deploy:\n" +
	"      dexaflow auth create-token --scope deploy,run --name <name>"

// checkServerAccess asks the control plane whether this token may register
// a version of dagID, with is_paused as the deploy will send it, by sending
// an empty spec to the register dry run route: the permission and scope
// gates answer 401 or 403 first, and the handler refuses the empty spec with
// 400 and dryRunHeader. A server that predates the route answers 404 and
// registers nothing; the deploy goes on without a dry run. warn receives the
// notes on what could not be checked.
func checkServerAccess(ctx context.Context, warn io.Writer, serverURL, token, dagID string, paused *bool) (serverAccess, error) {
	status, header, body, err := postVersion(ctx, versionPost{serverURL: serverURL, token: token, dagID: dagID,
		spec: []byte("{}"), paused: paused, dryRun: true})
	if err != nil {
		return serverAccess{}, fmt.Errorf("preflight: cannot reach the control plane at %s: %w\n"+
			"  Check the server (--server, --target or DEXAFLOW_SERVER_URL) and that it is up", serverURL, err)
	}
	hint := ""
	if paused != nil {
		hint = pausedScopeHint
	}
	if err := deployViaIssuerError(serverURL, status, header, body); err != nil {
		return serverAccess{}, fmt.Errorf("preflight: %w", err)
	}
	switch {
	case status == http.StatusUnauthorized:
		return serverAccess{}, fmt.Errorf("preflight: the control plane refused the token: %w\n"+
			"  Sign in again (%s --server %s) or pass a valid --token", apiStatusError(status, body), loginCommandPath, serverURL)
	case status == http.StatusForbidden && header.Get(dryRunHeader) == "":
		return serverAccess{}, fmt.Errorf("preflight: the token may not deploy to %s: %w\n"+
			"  A deploy needs the deployer role (or deploy or write on dags), and an API token the deploy scope:\n"+
			"      dexaflow auth create-token --scope deploy --name <name>%s", serverURL, apiStatusError(status, body), hint)
	case status == http.StatusForbidden:
		// The handler's own refusal: is_paused without write on dags or the
		// run scope.
		return serverAccess{}, fmt.Errorf("preflight: the server would refuse this register: %w%s", apiStatusError(status, body), hint)
	case status == http.StatusBadRequest && header.Get(dryRunHeader) == "true":
		return serverAccess{dryRun: true}, nil
	case status == http.StatusNotFound:
		//nolint:errcheck // advisory: the deploy proceeds
		fmt.Fprintf(warn, "note: %s offers no dry run of the register (an older server); its policy checks run when it registers, after the build\n", serverURL)
		return serverAccess{}, nil
	}
	//nolint:errcheck // advisory: the deploy proceeds
	fmt.Fprintf(warn, "note: could not check the control plane before the build (server returned %d); deploying\n", status)
	return serverAccess{}, nil
}

// dryRunAnswer is the part of a dry run answer the preflight reports.
type dryRunAnswer struct {
	DryRun     bool     `json:"dry_run"`
	Created    bool     `json:"created"`
	DagCreated bool     `json:"dag_created"`
	IsPaused   bool     `json:"is_paused"`
	Warnings   []string `json:"warnings"`
	// TaskPods is what the version's task pods would run as (#1889), nil
	// from an older server.
	TaskPods *taskPodsAnswer `json:"task_pods"`
}

// dryImage is the image a dry run is sent in the spec: ref, pinned by
// digest, and whether it is the image the register will get (decides), so
// that an answer the image decides (a refusal of the image itself, a label
// registered with other content) is final.
type dryImage struct {
	ref     string
	decides bool
}

// serverDryRun sends the compiled spec at specPath, its image replaced by
// img.ref, to the server's register dry run, with is_paused as the deploy
// will send it, and returns the refusal the register would give, labeled
// for version and origin. A refusal of the placeholder image or a conflict
// when img does not decide them, an answer that is no dry run (an older
// replica's 404), and one the server could not give (5xx, 429) are notes on
// warn, and return no answer. It prints what the register would do, with
// the links and checks the answer carries, to out and the server's warnings
// to warn, and returns the answer.
func serverDryRun(ctx context.Context, out, warn io.Writer, p versionPost, specPath string, img dryImage, version string, origin labelOrigin) (*dryRunAnswer, error) {
	data, err := os.ReadFile(specPath) //nolint:gosec // the compiled dag.json of the DAG directory
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", specPath, err)
	}
	if p.spec, err = repinImageInSpec(data, img.ref); err != nil {
		return nil, err
	}
	p.dryRun = true
	status, header, body, err := postVersion(ctx, p)
	note := func(format string, a ...any) error {
		_, werr := fmt.Fprintf(warn, "note: "+format+"\n", a...)
		return werr
	}
	switch {
	case err != nil:
		return nil, note("could not run the dry run of the register (%v); its checks run when it registers", err)
	case header.Get(dryRunHeader) != "true":
		return nil, note("%s answered the dry run of the register with %d, no dry run (an older replica?); its checks run when it registers", p.serverURL, status)
	case status >= http.StatusInternalServerError || status == http.StatusTooManyRequests:
		return nil, note("%s could not run the dry run of the register (%v); its checks run when it registers", p.serverURL, apiStatusError(status, body))
	case status == http.StatusConflict && !img.decides:
		return nil, note("the server would refuse this register: %v\n  The image built now decides whether it is the content already registered under %s, so the deploy goes on",
			apiStatusError(status, body), version)
	case refusesThePlaceholder(status, body, img):
		return nil, note("the server would refuse this register: %v\n  The image built now decides whether the server accepts it (the dry run had a placeholder digest), so the deploy goes on",
			apiStatusError(status, body))
	case status >= http.StatusMultipleChoices:
		return nil, fmt.Errorf("preflight: the server would refuse this register: %w", versionConflictHint(apiStatusError(status, body), version, origin))
	}
	var answer dryRunAnswer
	if jerr := json.Unmarshal(body, &answer); jerr != nil {
		return nil, fmt.Errorf("preflight: decoding the dry run answer: %w", jerr)
	}
	for _, w := range answer.Warnings {
		//nolint:errcheck // advisory
		fmt.Fprintf(warn, "warning: the server: %s\n", w)
	}
	_, werr := fmt.Fprintf(out, "preflight:  the server would register %s %s as %s\n%s", p.dagID, version, dryRunOutcome(answer),
		readServerExtras(body).lines())
	return &answer, werr
}

// imageAllowlistRefusal is the server's refusal of an image the operator's
// execution allowlist does not cover (domain.ExecutionPolicy.CheckImage),
// which ends with the allowlist in parentheses.
const imageAllowlistRefusal = " is not in executor.defaults.execution_allowlist.images ("

// refusesThePlaceholder reports whether a dry run's refusal is one the image
// built may still turn, so that the register decides it: img is the
// placeholder (img.decides false), and the body's only error is the image
// allowlist refusing img.ref while the allowlist names img's repository at a
// digest (repo@sha256:..., or a prefix of one). The real digest may be that
// one; a placeholder never is. A refusal of a repository the allowlist does
// not cover, or one joined with another refusal (CheckSpec joins them, one per
// line), fails the register whatever the build makes, so it stops.
func refusesThePlaceholder(status int, body []byte, img dryImage) bool {
	if img.decides || status != http.StatusBadRequest {
		return false
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &problem) != nil {
		return false
	}
	return allowlistMayTakeTheDigest(problem.Detail, img.ref)
}

// allowlistMayTakeTheDigest reports whether detail is exactly the image
// allowlist refusal of ref and an entry of that allowlist names ref's
// repository at a digest.
func allowlistMayTakeTheDigest(detail, ref string) bool {
	list, ok := strings.CutPrefix(detail, "image "+strconv.Quote(ref)+imageAllowlistRefusal)
	if !ok || strings.Contains(list, "\n") {
		return false
	}
	list, ok = strings.CutSuffix(list, ")")
	if !ok {
		return false
	}
	repo, _, _ := strings.Cut(ref, "@")
	for _, entry := range strings.Split(list, ", ") {
		if strings.HasPrefix(entry, repo+"@") {
			return true
		}
	}
	return false
}

// dryRunOutcome says what a register would do, in the words of the deploy
// output. Unchanged is reachable only when the dry run had the image the
// register gets (nothing is built): content with a placeholder digest is
// never registered.
func dryRunOutcome(a dryRunAnswer) string {
	state := "active"
	if a.IsPaused {
		state = "paused"
	}
	switch {
	case a.DagCreated:
		return "a new DAG, " + state
	case a.Created:
		return "a new version, " + state
	}
	return "unchanged, " + state
}

// checkRegistryPush asks the registry of image whether this machine's
// credentials, as reg reads them, may push to its repository, before the
// build. A refusal stops the deploy with the login command; a check that
// cannot decide is a note.
func checkRegistryPush(ctx context.Context, out, warn io.Writer, reg build.Registry, image, builder string) error {
	res, err := registryPushCheck(ctx, reg, image, builder)
	if err != nil {
		return err
	}
	if res.note != "" {
		_, werr := fmt.Fprintf(warn, "note: %s\n", res.note)
		return werr
	}
	_, werr := fmt.Fprintf(out, "preflight:  %s\n", res.ok)
	return werr
}

// checkResult is a preflight check that did not refuse: what it found, or
// a note on why it could not tell.
type checkResult struct{ ok, note string }

// registryPushCheck is the push check of checkRegistryPush, for deploy and
// doctor. The check reads the credentials reg reads (build.Registry.CheckPush:
// the docker config and its credential helpers, then the cloud CLI's
// session, by registry.auth_method), which is what the daemonless paths
// push with; pushRefusal words a refusal.
func registryPushCheck(ctx context.Context, reg build.Registry, image, builder string) (checkResult, error) {
	access, err := reg.CheckPush(ctx, image)
	switch {
	case err == nil:
		return checkResult{ok: pushAccepted(access, builder)}, nil
	case errors.Is(err, build.ErrPushDenied):
		return pushRefusal(access, err, builder, reg.Auth)
	}
	target := access.Repository
	if target == "" {
		target = image
	}
	return checkResult{note: fmt.Sprintf("could not check push access to %s (%v); the push will tell", target, err)}, nil
}

// pushAccepted words an accepted push check: the repository, where the
// credential came from and, for a cloud session, that the local daemon
// pushes with the tool's own login, which the session does not give it.
func pushAccepted(access build.PushAccess, builder string) string {
	ok := access.Repository + " accepts a push"
	switch access.Source {
	case "":
		return ok
	case build.SourceDockerConfig:
		return ok + " (credential from " + access.Source + ")"
	}
	tool := toolName(builder)
	return fmt.Sprintf("%s (credential from %s; a local-daemon build pushes with %s's own login: %s)",
		ok, access.Source, tool, build.LoginCommand(access.Registry, tool))
}

// toolName is the name of the --builder executable, docker when empty.
func toolName(builder string) string {
	tool := filepath.Base(builder)
	if tool == "" || tool == "." {
		return "docker"
	}
	return tool
}

// pushRefusal words the registry's refusal err of a push to access, naming
// the command that signs builder in to that registry (build.LoginCommand),
// or, when the refused credential came from a cloud session, the session to
// sign in to and the role to grant. A refusal of the credential the docker
// builder would push with is an error. With no credential found, or with
// another builder, which keeps credentials of its own (podman's auth.json,
// for one), a refusal says nothing certain about the push: it is a note.
func pushRefusal(access build.PushAccess, err error, builder string, method build.AuthMethod) (checkResult, error) {
	tool := toolName(builder)
	login := build.LoginCommand(access.Registry, tool)
	cloud, isCloud := build.DetectCloudRegistry(access.Registry)
	switch {
	case access.Anonymous:
		return checkResult{note: anonymousRefusal(access, err, login, method)}, nil
	case tool != "docker":
		return checkResult{note: fmt.Sprintf("the registry %s refused a push to %s with the credential from %s; "+
			"%s may push with credentials of its own; if the push fails, sign in:  %s  (%v)", access.Registry, access.Repository, access.Source, tool, login, err)}, nil
	case isCloud && access.Source != build.SourceDockerConfig:
		onlyCloud := ""
		if method == build.AuthCloud {
			onlyCloud = "\n  The docker config is not read with registry.auth_method: cloud; auth_method: auto would try its credential first"
		}
		return checkResult{}, fmt.Errorf("preflight: the registry %s refused a push to %s with the credential from %s.\n"+
			"  Sign in to %s with an account that may push there (%s), and grant it %s%s\n  (%w)",
			access.Registry, access.Repository, access.Source, cloud.Provider, cloud.SessionCommand(), cloud.PushRole(), onlyCloud, err)
	}
	instead := ""
	if isCloud && method != build.AuthCloud {
		instead = fmt.Sprintf("\n  Or sign in to %s (%s) and set registry.auth_method: cloud in dexaflow.yaml to push with that session "+
			"instead of the docker config, whose entry may have expired", cloud.Provider, cloud.SessionCommand())
	}
	return checkResult{}, fmt.Errorf("preflight: the registry %s refused a push to %s with the credential from %s.\n"+
		"  Sign in with an account that may push there:  %s%s\n  (%w)",
		access.Registry, access.Repository, access.Source, login, instead, err)
}

// anonymousRefusal words a refusal of a push made with no credential.
func anonymousRefusal(access build.PushAccess, err error, login string, method build.AuthMethod) string {
	why := ""
	if access.CloudError != nil {
		why = " (" + access.CloudError.Error() + ")"
	}
	session := ""
	if cloud, ok := build.DetectCloudRegistry(access.Registry); ok && method != build.AuthDockerConfig {
		session = fmt.Sprintf("; or sign in to %s (%s), whose session the daemonless builds read", cloud.Provider, cloud.SessionCommand())
	}
	return fmt.Sprintf("the registry %s refused a push to %s, and no credential was found for it in %s%s; "+
		"if the push fails, sign in once:  %s%s  (%v)", access.Registry, access.Repository, credentialPlaces(method), why, login, session, err)
}

// credentialPlaces says where method looks for a registry credential.
func credentialPlaces(method build.AuthMethod) string {
	switch method {
	case build.AuthDockerConfig:
		return "the docker config or its credential helpers"
	case build.AuthCloud:
		return "a cloud CLI session (the docker config, for a registry no cloud owns)"
	case build.AuthAuto:
	}
	return "the docker config, its credential helpers or a cloud CLI session"
}

// registryHolds reports whether the registry already holds image, so a
// deploy that may reuse it pushes nothing and needs no push check.
func registryHolds(ctx context.Context, reg build.Registry, image string) bool {
	_, err := reg.Resolve(ctx, image)
	return err == nil
}
