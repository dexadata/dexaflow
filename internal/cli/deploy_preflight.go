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
// warn. It
// prints what the register would do to out and the server's warnings to
// warn.
func serverDryRun(ctx context.Context, out, warn io.Writer, p versionPost, specPath string, img dryImage, version string, origin labelOrigin) error {
	data, err := os.ReadFile(specPath) //nolint:gosec // the compiled dag.json of the DAG directory
	if err != nil {
		return fmt.Errorf("reading %s: %w", specPath, err)
	}
	if p.spec, err = repinImageInSpec(data, img.ref); err != nil {
		return err
	}
	p.dryRun = true
	status, header, body, err := postVersion(ctx, p)
	note := func(format string, a ...any) error {
		_, werr := fmt.Fprintf(warn, "note: "+format+"\n", a...)
		return werr
	}
	switch {
	case err != nil:
		return note("could not run the dry run of the register (%v); its checks run when it registers", err)
	case header.Get(dryRunHeader) != "true":
		return note("%s answered the dry run of the register with %d, no dry run (an older replica?); its checks run when it registers", p.serverURL, status)
	case status >= http.StatusInternalServerError || status == http.StatusTooManyRequests:
		return note("%s could not run the dry run of the register (%v); its checks run when it registers", p.serverURL, apiStatusError(status, body))
	case status == http.StatusConflict && !img.decides:
		return note("the server would refuse this register: %v\n  The image built now decides whether it is the content already registered under %s, so the deploy goes on",
			apiStatusError(status, body), version)
	case refusesThePlaceholder(status, body, img):
		return note("the server would refuse this register: %v\n  The image built now decides whether the server accepts it (the dry run had a placeholder digest), so the deploy goes on",
			apiStatusError(status, body))
	case status >= http.StatusMultipleChoices:
		return fmt.Errorf("preflight: the server would refuse this register: %w", versionConflictHint(apiStatusError(status, body), version, origin))
	}
	var answer dryRunAnswer
	if jerr := json.Unmarshal(body, &answer); jerr != nil {
		return fmt.Errorf("preflight: decoding the dry run answer: %w", jerr)
	}
	for _, w := range answer.Warnings {
		//nolint:errcheck // advisory
		fmt.Fprintf(warn, "warning: the server: %s\n", w)
	}
	_, werr := fmt.Fprintf(out, "preflight:  the server would register %s %s as %s\n", p.dagID, version, dryRunOutcome(answer))
	return werr
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
// credentials may push to its repository, before the build. A refusal stops
// the deploy with the login command; a check that cannot decide is a note.
func checkRegistryPush(ctx context.Context, out, warn io.Writer, image, builder string) error {
	res, err := registryPushCheck(ctx, image, builder)
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
// doctor. The check reads the docker config and its credential helpers
// (build.Registry.CheckPush), which is what the docker builder and the
// daemonless paths push with, so a refusal there is an error naming
// `docker login`. With no credential found, or with another builder, which
// keeps credentials of its own (podman's auth.json, for one), a refusal
// says nothing certain about the push: it is a note naming
// `<builder> login`.
func registryPushCheck(ctx context.Context, image, builder string) (checkResult, error) {
	access, err := build.Registry{}.CheckPush(ctx, image)
	tool := filepath.Base(builder)
	switch {
	case err == nil:
		return checkResult{ok: access.Repository + " accepts a push"}, nil
	case errors.Is(err, build.ErrPushDenied) && access.Anonymous:
		return checkResult{note: fmt.Sprintf("the registry %s refused a push to %s, and the docker config holds no credential for it; "+
			"if the push fails, sign in once:  %s login %s  (%v)", access.Registry, access.Repository, tool, access.Registry, err)}, nil
	case errors.Is(err, build.ErrPushDenied) && tool != "docker":
		return checkResult{note: fmt.Sprintf("the registry %s refused a push to %s with the credential of the docker config; "+
			"%s may push with credentials of its own; if the push fails, sign in:  %s login %s  (%v)", access.Registry, access.Repository, tool, tool, access.Registry, err)}, nil
	case errors.Is(err, build.ErrPushDenied):
		return checkResult{}, fmt.Errorf("preflight: the registry %s refused a push to %s with the credential in the docker config.\n"+
			"  Sign in with an account that may push there:  docker login %s\n  (%w)",
			access.Registry, access.Repository, access.Registry, err)
	}
	target := access.Repository
	if target == "" {
		target = image
	}
	return checkResult{note: fmt.Sprintf("could not check push access to %s (%v); the push will tell", target, err)}, nil
}

// registryHolds reports whether the registry already holds image, so a
// deploy that may reuse it pushes nothing and needs no push check.
func registryHolds(ctx context.Context, image string) bool {
	_, err := build.Registry{}.Resolve(ctx, image)
	return err == nil
}
