package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dexadata/dexaflow/internal/build"
)

// deployPlan is what deploy --plan says one DAG's deploy would do, and why.
type deployPlan struct {
	Build          bool   `json:"build"`
	Push           bool   `json:"push"`
	Register       bool   `json:"register"`
	Reason         string `json:"reason"`
	LabelOrigin    string `json:"label_origin"`
	CurrentVersion string `json:"current_version,omitempty"`
	Registered     bool   `json:"registered_on_server"`
	SetIsPaused    *bool  `json:"set_is_paused,omitempty"`
	// SourceMode says the deploy registers dag.py on the runtime image,
	// with no build or push (#1772).
	SourceMode bool `json:"source_mode,omitempty"`
}

// planInput is what deploy --plan decides from: the resolved label and image,
// where the label came from, and the flags that change what a deploy does.
type planInput struct {
	serverURL, token string
	dagID, version   string
	image            string
	origin           labelOrigin
	skipBuild, force bool
	// sourceMode is a deploy that registers dag.py on the runtime image
	// (image), with no build or push.
	sourceMode bool
	paused     *bool
	// reuse and platforms let the plan check the skip path as the deploy
	// would: whether the registry already holds the image for this label.
	reuse     bool
	platforms []string
	// registry reads the registry with the project's credentials.
	registry build.Registry
}

// serverDag is what the plan learns about the DAG from the server's details:
// whether it is registered, and its state when it is.
type serverDag struct {
	registered bool
	known      bool // the details were read
	paused     bool
}

// readServerDag reads the DAG's details for the plan. A 404 is a DAG the
// server does not have; a 401 stops the plan as it would stop the deploy;
// anything else unreadable leaves the plan to say it could not tell.
func readServerDag(ctx context.Context, serverURL, token, dagID string) (serverDag, error) {
	u := strings.TrimRight(serverURL, "/") + "/api/v2/dags/" + url.PathEscape(dagID) + "/details"
	status, body, err := getOnce(ctx, u, token)
	if err != nil {
		// Unreachable: the content check that follows says so with a note.
		return serverDag{}, nil //nolint:nilerr // the plan proceeds unsure, as a deploy proceeds past an unreachable pre-check
	}
	switch {
	case status == http.StatusNotFound:
		return serverDag{known: true}, nil
	case status == http.StatusUnauthorized:
		return serverDag{}, fmt.Errorf("reading %s from the control plane: %w", dagID, apiStatusError(status, body))
	case status >= http.StatusMultipleChoices:
		return serverDag{}, nil
	}
	var d dagDetailsRead
	if jerr := json.Unmarshal(body, &d); jerr != nil {
		return serverDag{}, nil //nolint:nilerr // an unreadable answer leaves the plan unsure, as an unreachable server does
	}
	return serverDag{registered: true, known: true, paused: d.IsPaused}, nil
}

// planDeploy decides, without building, pushing or registering anything,
// what a deploy of the DAG would do: from the content label and the server's
// current version (the same pre-check a deploy runs), or from the flags that
// skip that check.
func planDeploy(ctx context.Context, warn io.Writer, in planInput) (deployResult, error) {
	res := deployResult{DagID: in.dagID, Status: deployStatusPlanned, Version: in.version, Image: in.image}
	dag, err := readServerDag(ctx, in.serverURL, in.token, in.dagID)
	if err != nil {
		return res, err
	}
	building := !in.skipBuild && !in.sourceMode
	p := &deployPlan{Build: building, Push: building, Register: true, SourceMode: in.sourceMode,
		LabelOrigin: labelOriginName(in.origin), Registered: dag.registered, SetIsPaused: in.paused}
	res.Plan = p
	if dag.registered {
		res.IsPaused = &dag.paused
	}
	switch {
	case in.origin != labelDefault:
		p.Reason = "the label comes from --dag-version, so it is not checked against the server; a label registered with other content is refused with 409"
	case in.force:
		p.Reason = "--force: " + planSteps(in, "built, pushed and registered", "registered") + " without checking the content label"
	case dag.known && !dag.registered:
		p.Reason = "not registered on the server: the deploy creates the DAG"
	default:
		state, current, cerr := checkDeployed(ctx, warn, in.serverURL, in.token, in.dagID, in.version)
		if cerr != nil {
			return res, cerr
		}
		p.CurrentVersion = current
		planFromState(&res, state, current, in)
	}
	planReuse(ctx, p, in)
	return res, nil
}

// planReuse turns a planned build into a reuse when the deploy would take
// the skip path: the registry already holds the image for this content
// label. It reads the registry, as the deploy would, and changes nothing.
func planReuse(ctx context.Context, p *deployPlan, in planInput) {
	if !p.Build || !in.reuse {
		return
	}
	if b, _ := build.Select(ctx, build.Options{Reuse: true, Registry: in.registry}, in.image, in.platforms); b.Path() != build.PathSkip {
		return
	}
	p.Build, p.Push = false, false
	p.Reason += "; the registry already holds the image for this content label, so it is pinned without a build or push (--rebuild builds it again)"
}

// planFromState fills the plan from the pre-check's verdict on the content
// label.
func planFromState(res *deployResult, state deployedState, current string, in planInput) {
	p := res.Plan
	switch state {
	case deployCurrent:
		p.Build, p.Push, p.Register = false, false, false
		p.Reason = fmt.Sprintf("up to date: %s is the current version; nothing to build, push or register", in.version)
	case deployStale:
		res.Status = deployStatusRefused
		p.Build, p.Push, p.Register = false, false, false
		p.Reason = fmt.Sprintf("refused: the content matches version %s, which is not the current version (%s); "+
			"make it current with `dexaflow rollback %s --to %s`, or pass --dag-version <new-label>", in.version, current, in.dagID, in.version)
	case deployUnknown:
		if current == "" {
			p.Reason = "the server has no current version to compare with (or could not be asked): the deploy " + planSteps(in, "builds, pushes and registers", "registers")
			return
		}
		p.Reason = fmt.Sprintf("content changed: label %s is not registered, the current version is %s", in.version, current)
	}
}

// planSteps returns built for a deploy that builds, pushes and registers,
// and registered for one that only registers (source mode).
func planSteps(in planInput, built, registered string) string {
	if in.sourceMode {
		return registered
	}
	return built
}

// labelOriginName says where the version label came from.
func labelOriginName(o labelOrigin) string {
	if o == labelDefault {
		return "content"
	}
	return "--dag-version"
}

// planStep words a plan step, with why it is skipped when skipped.
func planStep(do bool, why string) string {
	if do {
		return "yes"
	}
	if why == "" {
		return "no"
	}
	return "no (" + why + ")"
}

// pausedPlan says what the deploy does to is_paused.
func pausedPlan(res deployResult, in planInput) string {
	switch {
	case in.paused != nil && *in.paused:
		return "set to true (--paused)"
	case in.paused != nil:
		return "set to false (--active)"
	case res.Plan.Registered && res.IsPaused != nil:
		return "unchanged (" + map[bool]string{true: "paused", false: "active"}[*res.IsPaused] + ")"
	case res.Plan.Registered:
		return "unchanged"
	}
	return "at creation, per the DAG's is_paused_upon_creation, else the server's dags_are_paused_at_creation"
}

// printPlan writes the plan of one DAG for a person to read.
func printPlan(out io.Writer, dir string, res deployResult, in planInput) error {
	p := res.Plan
	skip := ""
	switch {
	case in.sourceMode:
		skip = "source mode"
	case in.skipBuild:
		skip = "--skip-build"
	}
	origin := "content label"
	if in.origin != labelDefault {
		origin = "--dag-version"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "plan for %s (%s):\n", res.DagID, dir)
	fmt.Fprintf(&b, "  version     %s (%s)\n", res.Version, origin)
	fmt.Fprintf(&b, "  image       %s\n", res.Image)
	fmt.Fprintf(&b, "  build       %s\n", planStep(p.Build, skipWhy(p.Build, skip)))
	fmt.Fprintf(&b, "  push        %s\n", planStep(p.Push, skipWhy(p.Push, skip)))
	fmt.Fprintf(&b, "  register    %s\n", planStep(p.Register, ""))
	fmt.Fprintf(&b, "  is_paused   %s\n", pausedPlan(res, in))
	fmt.Fprintf(&b, "  why         %s\n", p.Reason)
	_, err := io.WriteString(out, b.String())
	return err
}

// skipWhy names --skip-build as the reason a build or push is skipped, when
// it is; a step the content check skips gets its reason from the why line.
func skipWhy(do bool, skip string) string {
	if do {
		return ""
	}
	return skip
}

// planSummary is the closing line of deploy --all --plan.
func planSummary(n int, dir string) string {
	return "Planned " + strconv.Itoa(n) + " DAG(s) from " + dir + "; nothing was built, pushed or registered\n"
}
