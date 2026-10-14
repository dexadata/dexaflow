package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/dexadata/dexaflow/internal/build"
)

// The preflight of a deploy (#1705) asks, in seconds and before anything is
// built, whether the deploy would be refused: the control plane and the
// token before the compile, the registry push before the build, and the
// server's dry run of the register once the spec is compiled. A refusal
// stops the deploy and names what to fix; what cannot be checked is a note
// and the deploy goes on, as it did before the preflight.

// dryRunHeader is the header a server sets on every answer to a register
// dry run, refusals included (internal/api). Without it the server predates
// the dry run and would have registered the spec for real.
const dryRunHeader = "Dexaflow-Dry-Run"

// preflightTimeout bounds each preflight request.
const preflightTimeout = 30 * time.Second

// serverAccess is what the access check found out about the control plane.
type serverAccess struct {
	// dryRun is true when the server runs a register dry run.
	dryRun bool
}

// checkServerAccess asks the control plane whether this token may register
// a version of dagID, with is_paused as the deploy will send it, by sending
// the dry run of an empty spec: the permission and scope gates answer 401 or
// 403 before the handler, and the handler refuses the empty spec with 400. A
// server that predates the dry run refuses it the same way, without
// dryRunHeader, and is not sent a compiled spec with dry_run, which it would
// register. warn receives the notes on what could not be checked.
func checkServerAccess(ctx context.Context, warn io.Writer, serverURL, token, dagID string, paused *bool) (serverAccess, error) {
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	status, header, body, err := postVersion(ctx, versionPost{serverURL: serverURL, token: token, dagID: dagID,
		spec: []byte("{}"), paused: paused, dryRun: true})
	if err != nil {
		return serverAccess{}, fmt.Errorf("preflight: cannot reach the control plane at %s: %w\n"+
			"  Check the server (--server, --target or DEXAFLOW_SERVER_URL) and that it is up", serverURL, err)
	}
	switch {
	case status == http.StatusUnauthorized:
		return serverAccess{}, fmt.Errorf("preflight: the control plane refused the token: %w\n"+
			"  Sign in again (%s --server %s) or pass a valid --token", apiStatusError(status, body), loginCommandPath, serverURL)
	case status == http.StatusForbidden && header.Get(dryRunHeader) == "":
		return serverAccess{}, fmt.Errorf("preflight: the token may not deploy to %s: %w\n"+
			"  A deploy needs the deployer role (or deploy or write on dags), and an API token the deploy scope:\n"+
			"      dexaflow auth create-token --scope deploy --name <name>", serverURL, apiStatusError(status, body))
	case status == http.StatusForbidden:
		// The handler's own refusal: is_paused without write on dags or the
		// run scope.
		return serverAccess{}, fmt.Errorf("preflight: the server would refuse this register: %w", apiStatusError(status, body))
	case status == http.StatusNotFound:
		return serverAccess{}, fmt.Errorf("preflight: %s does not serve the register route (POST /api/v2/dags/{dag_id}/versions answered 404); is it a Dexaflow control plane?", serverURL)
	case status != http.StatusBadRequest:
		//nolint:errcheck // advisory: the deploy proceeds
		fmt.Fprintf(warn, "note: could not check the control plane before the build (server returned %d); deploying\n", status)
		return serverAccess{}, nil
	}
	access := serverAccess{dryRun: header.Get(dryRunHeader) == "true"}
	if !access.dryRun {
		//nolint:errcheck // advisory: the deploy proceeds
		fmt.Fprintf(warn, "note: %s offers no dry run of the register (an older server); its policy checks run when it registers, after the build\n", serverURL)
	}
	return access, nil
}

// dryRunAnswer is the part of a dry run answer the preflight reports.
type dryRunAnswer struct {
	DryRun     bool     `json:"dry_run"`
	Created    bool     `json:"created"`
	DagCreated bool     `json:"dag_created"`
	IsPaused   bool     `json:"is_paused"`
	Warnings   []string `json:"warnings"`
}

// serverDryRun sends the compiled spec at specPath to the server's register
// dry run, with is_paused as the deploy will send it, and returns the
// refusal the register would give, labeled for version and origin. It
// prints what the register would do to out and the server's warnings to
// warn.
func serverDryRun(ctx context.Context, out, warn io.Writer, p versionPost, specPath, version string, origin labelOrigin) error {
	spec, err := os.ReadFile(specPath) //nolint:gosec // the compiled dag.json of the DAG directory
	if err != nil {
		return fmt.Errorf("reading %s: %w", specPath, err)
	}
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	p.spec, p.dryRun = spec, true
	status, header, body, err := postVersion(ctx, p)
	if err != nil {
		return fmt.Errorf("preflight: the dry run of the register: %w", err)
	}
	if header.Get(dryRunHeader) != "true" {
		return fmt.Errorf("preflight: %s answered the dry run of the register with %d and no %s header; stopping, since it may have registered the spec", p.serverURL, status, dryRunHeader)
	}
	if status >= http.StatusMultipleChoices {
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

// dryRunOutcome says what a register would do, in the words of the deploy
// output.
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
	return "content it already holds, " + state
}

// checkRegistryPush asks the registry of image whether this machine's
// credentials may push to its repository, before the build. A refusal stops
// the deploy with the login command; a check that cannot be made is a note.
func checkRegistryPush(ctx context.Context, out, warn io.Writer, image string) error {
	res, err := registryPushCheck(ctx, image)
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
// doctor: an error is a refusal, naming the login command.
func registryPushCheck(ctx context.Context, image string) (checkResult, error) {
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	access, err := build.Registry{}.CheckPush(ctx, image)
	switch {
	case err == nil:
		return checkResult{ok: access.Repository + " accepts a push"}, nil
	case errors.Is(err, build.ErrPushDenied) && access.Anonymous:
		return checkResult{}, fmt.Errorf("preflight: no credential for the registry %s, which refused a push to %s.\n"+
			"  Sign in once:  docker login %s   (or set its credential helper in the docker config, ~/.docker/config.json)\n  (%w)",
			access.Registry, access.Repository, access.Registry, err)
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
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	_, err := build.Registry{}.Resolve(ctx, image)
	return err == nil
}
