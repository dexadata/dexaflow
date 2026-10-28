package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// promoteOptions holds the flags of `dexaflow promote`.
type promoteOptions struct {
	from, to           string
	version            string
	registry           string
	fromToken, toToken string
	output             string
	dryRun, yes        bool
}

// newPromoteCommand builds `dexaflow promote <dag_id> --from <target> --to
// <target>` (#1708): it registers on one control plane the exact version
// another one runs, the same dag.json and the same image digest, with no
// build. The destination gets it through the register every deploy uses, so
// its checks, the paused-at-creation rule and the audit trail apply.
func newPromoteCommand() *cobra.Command {
	var o promoteOptions
	cmd := &cobra.Command{
		Use:   "promote <dag_id> --from <target> --to <target>",
		Short: "Register on one target the exact version another target runs (same digest, no build).",
		Long: "Reads a registered version of the DAG from the --from target (its current version, or the one " +
			"--version names: a label first, then a version number from `dexaflow dags versions`) and registers " +
			"the same dag.json, with the same image digest, on the --to target. Nothing is built.\n\n" +
			"The image must be pinned by digest. When the destination pulls from another registry (--registry, " +
			"else the registry of the --to target, else the deploy defaults its control plane advertises), the " +
			"image is copied to that registry by digest, byte for byte, keeping its repository path, and the version " +
			"registered with the copy (a tag that already names another digest there is never moved); " +
			"otherwise the same reference is registered.\n\n" +
			"The destination runs the register's dry run first, so a refusal stops the promote before any copy. " +
			"A DAG the promote creates starts paused as a deploy's would, and an existing DAG keeps is_paused. " +
			"The register's audit entry records the source target and server.\n\n" +
			"Each control plane gets its own token: --from-token and --to-token, else the session saved for its " +
			"server by `dexaflow auth login --target <name>`. DEXAFLOW_TOKEN is not used, so one token never " +
			"goes to two control planes. The source side reads each version's spec, which needs read on dags " +
			"(an API token with the read scope); the destination side needs only what a deploy needs.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPromoteCommand(cmd, args[0], o)
		},
	}
	cmd.Flags().StringVar(&o.from, "from", "", "target to read the version from (required)")
	cmd.Flags().StringVar(&o.to, "to", "", "target to register the version on (required)")
	cmd.Flags().StringVar(&o.version, "version", "", "version to promote: matched first as a label, then as a version number (default: the source's current version)")
	cmd.Flags().StringVar(&o.registry, "registry", "", "registry prefix the destination pulls from, over the --to target's registry and its cluster's deploy defaults")
	cmd.Flags().StringVar(&o.fromToken, "from-token", "", "bearer token for the --from control plane (default: the session saved for its server)")
	cmd.Flags().StringVar(&o.toToken, "to-token", "", "bearer token for the --to control plane (default: the session saved for its server)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "say what would be copied and registered, with the destination's dry run of the register, without doing it")
	cmd.Flags().BoolVarP(&o.yes, "yes", "y", false, "skip the confirmation prompt (for automation)")
	cmd.Flags().StringVar(&o.output, "output", outputText, "output format: text, or json for one JSON document on stdout (human lines go to stderr)")
	return cmd
}

// promoteReport is the JSON document of --output json.
type promoteReport struct {
	DagID      string       `json:"dag_id"`
	From       promoteSide  `json:"from"`
	To         promoteSide  `json:"to"`
	Copied     bool         `json:"copied"`
	Status     string       `json:"status"`
	SpecHash   string       `json:"spec_hash,omitempty"`
	DagCreated bool         `json:"dag_created"`
	IsPaused   *bool        `json:"is_paused,omitempty"`
	Error      string       `json:"error,omitempty"`
	DryRun     *dryRunReply `json:"dry_run,omitempty"`
}

// promoteSide is one end of a promote as the report shows it.
type promoteSide struct {
	Target        string `json:"target"`
	Server        string `json:"server"`
	Version       string `json:"version,omitempty"`
	VersionNumber int    `json:"version_number,omitempty"`
	Image         string `json:"image,omitempty"`
}

// dryRunReply is the destination's dry run answer, as the report shows it.
type dryRunReply struct {
	Created    bool     `json:"created"`
	DagCreated bool     `json:"dag_created"`
	IsPaused   bool     `json:"is_paused"`
	Warnings   []string `json:"warnings,omitempty"`
}

// The statuses of a promote.
const (
	promoteRegistered = "registered"
	promoteUnchanged  = "unchanged"
	promotePlanned    = "planned"
	promoteFailed     = "failed"
)

// runPromoteCommand runs the promote with the chosen output: with --output
// json the human lines go to stderr and stdout carries the report, after a
// failure too.
func runPromoteCommand(cmd *cobra.Command, dagID string, o promoteOptions) error {
	if o.output != outputText && o.output != outputJSON {
		return fmt.Errorf("--output must be %s or %s, not %q", outputText, outputJSON, o.output)
	}
	rep := &promoteReport{DagID: dagID}
	if o.output != outputJSON {
		return promote(cmd, dagID, o, rep)
	}
	stdout := cmd.OutOrStdout()
	cmd.SetOut(cmd.ErrOrStderr())
	err := promote(cmd, dagID, o, rep)
	if err != nil {
		rep.Status, rep.Error = promoteFailed, err.Error()
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if eerr := enc.Encode(rep); eerr != nil && err == nil {
		err = fmt.Errorf("writing the promote report: %w", eerr)
	}
	return err
}

// promoteEnd is one control plane of a promote: its target and the token
// sent to it.
type promoteEnd struct {
	target *activeTarget
	token  string
}

// promoteRun is one promote once both ends are resolved.
type promoteRun struct {
	cmd      *cobra.Command
	o        promoteOptions
	dagID    string
	from, to promoteEnd
	rep      *promoteReport
}

// promote reads the version from the source, decides the destination image,
// runs the destination's dry run, then copies the image when needed and
// registers the version.
func promote(cmd *cobra.Command, dagID string, o promoteOptions, rep *promoteReport) error {
	from, to, err := promoteEnds(cmd, o)
	if err != nil {
		return err
	}
	p := &promoteRun{cmd: cmd, o: o, dagID: dagID, from: from, to: to, rep: rep}
	rep.From = promoteSide{Target: from.target.Name, Server: from.target.Server}
	rep.To = promoteSide{Target: to.target.Name, Server: to.target.Server}
	out := cmd.OutOrStdout()
	devPrintf(out, "from:       %s (%s)\nto:         %s (%s)\n", from.target.Name, from.target.Server, to.target.Name, to.target.Server)
	ctx := cmdContext(cmd)
	src, err := p.readSource(ctx)
	if err != nil {
		return err
	}
	dest, err := p.destinationImage(ctx, src.version.image)
	if err != nil {
		return err
	}
	return p.apply(ctx, src, dest)
}

// apply registers src on the destination with the image dest, after its dry
// run and the copy of the image.
func (p *promoteRun) apply(ctx context.Context, src sourceVersion, dest destImage) error {
	spec := src.spec
	if dest.copy {
		repinned, err := repinImageInSpec(src.spec, dest.ref)
		if err != nil {
			return err
		}
		spec = repinned
	}
	p.rep.To.Image = dest.ref
	out := p.cmd.OutOrStdout()
	devPrintf(out, "version:    %s (#%d), image %s\n", src.version.label, src.version.number, src.version.image)
	if dest.copy {
		devPrintf(out, "image:      copy to %s (the destination pulls from %s, from %s)\n", dest.ref, dest.registry, dest.source)
	} else {
		devPrintf(out, "image:      the same reference (%s)\n", dest.why)
	}
	if !p.o.dryRun && shouldConfirm(p.to.target.Server, p.o.yes, cmdInteractive(p.cmd)) &&
		!confirmDeploy(p.cmd.InOrStdin(), out, fmt.Sprintf("%s %s from %s", p.dagID, src.version.label, p.from.target.Name), p.to.target.Server) {
		return errors.New("promote aborted")
	}
	post := versionPost{serverURL: p.to.target.Server, token: p.to.token, dagID: p.dagID, spec: spec,
		promoted: &domain.DagPromotion{Target: p.from.target.Name, Server: p.from.target.Server}, build: src.version.buildInfo}
	if err := p.dryRunRegister(ctx, post, src.version.label); err != nil {
		return err
	}
	if p.o.dryRun {
		p.rep.Status = promotePlanned
		if dest.copy {
			devPrintf(out, "would copy %s to %s\n", src.version.image, dest.ref)
		}
		devPrintf(out, "would register %s %s on %s; nothing was copied or registered (--dry-run)\n", p.dagID, src.version.label, p.to.target.Name)
		return nil
	}
	if err := p.copyIfNeeded(ctx, src, dest); err != nil {
		return err
	}
	return p.register(ctx, post, src.version.label)
}

// copyIfNeeded copies the image when dest is a copy, unless the dry run said
// the destination already holds this content (created false), whose image
// is then already there.
func (p *promoteRun) copyIfNeeded(ctx context.Context, src sourceVersion, dest destImage) error {
	if !dest.copy {
		return nil
	}
	if p.rep.DryRun != nil && !p.rep.DryRun.Created {
		devPrintf(p.cmd.OutOrStdout(), "image:      not copied: %s already holds this version\n", p.to.target.Name)
		return nil
	}
	if err := copyImage(ctx, p.cmd.OutOrStdout(), src.version.image, dest, src.version.label); err != nil {
		return err
	}
	p.rep.Copied = true
	return nil
}

// promoteEnds resolves --from and --to to targets, with the token of each.
func promoteEnds(cmd *cobra.Command, o promoteOptions) (from, to promoteEnd, err error) {
	if o.from == "" || o.to == "" {
		return from, to, errors.New("promote needs --from and --to: the targets to read the version from and to register it on (see `dexaflow target list`)")
	}
	if targetFlag(cmd) != "" {
		return from, to, errors.New("promote calls two targets, named by --from and --to; --target does not apply")
	}
	if o.from == o.to {
		return from, to, fmt.Errorf("--from and --to are the same target (%s): nothing to promote", o.from)
	}
	tc, err := loadTargetContext(cmd)
	if err != nil {
		return from, to, err
	}
	if from, err = promoteEndFor(cmd, tc, o.from, "--from", o.fromToken); err != nil {
		return from, to, err
	}
	if to, err = promoteEndFor(cmd, tc, o.to, "--to", o.toToken); err != nil {
		return from, to, err
	}
	if sameServer(from.target.Server, to.target.Server) {
		return from, to, fmt.Errorf("targets %s and %s are the same control plane (%s): nothing to promote", o.from, o.to, from.target.Server)
	}
	return from, to, nil
}

// promoteEndFor looks the target named name up, as flag names it, and picks
// its token: tokenFlag, else the one saved for its server (savedTokenFor),
// refreshed as every server command refreshes a saved session.
func promoteEndFor(cmd *cobra.Command, tc *targetContext, targetName, flag, tokenFlag string) (promoteEnd, error) {
	t, ok := tc.lookup(targetName)
	if !ok {
		known := tc.knownTargets()
		if len(known) == 0 {
			return promoteEnd{}, fmt.Errorf("unknown target %q (from %s): no targets are defined; add one with `dexaflow target add %s <url>`", targetName, flag, targetName)
		}
		return promoteEnd{}, fmt.Errorf("unknown target %q (from %s); known targets: %s", targetName, flag, strings.Join(known, ", "))
	}
	t.Origin = flag
	if tokenFlag != "" {
		return promoteEnd{target: t, token: tokenFlag}, nil
	}
	choice := &serverChoice{URL: t.Server, Target: t, Source: "target " + t.Name, tc: tc}
	token, source := savedTokenFor(choice, true)
	ctx := cmdContext(cmd)
	switch source {
	case tokenSession:
		token = autoRefreshWith(ctx, t.Server, token, func(fresh string) error {
			return saveSession(cmd, t.Server, fresh)
		})
	case tokenLegacy:
		token = autoRefreshToken(ctx, configFilePath(cmd), t.Server, token)
	case tokenNone, tokenExternal:
	}
	return promoteEnd{target: t, token: token}, nil
}

// sourceVersion is the version a promote copies: its listing row and the
// dag.json registered with it, as the source serves it.
type sourceVersion struct {
	version listedVersion
	spec    []byte
}

// readSource picks the version on the source (--version, else the current
// one) and reads its spec, refusing an image not pinned by digest.
func (p *promoteRun) readSource(ctx context.Context) (sourceVersion, error) {
	c, err := cliAPIClient(p.from.target.Server, p.from.token)
	if err != nil {
		return sourceVersion{}, fmt.Errorf("building the API client: %w", err)
	}
	versions, err := listVersions(ctx, c, p.dagID)
	if err != nil {
		return sourceVersion{}, fmt.Errorf("reading the versions of %s on %s: %w", p.dagID, p.from.target.Name, err)
	}
	v, err := pickPromoted(p.dagID, p.from.target.Name, versions, p.o.version)
	if err != nil {
		return sourceVersion{}, err
	}
	p.rep.From.Version, p.rep.From.VersionNumber, p.rep.From.Image = v.label, v.number, v.image
	if _, derr := name.NewDigest(v.image); derr != nil {
		return sourceVersion{}, fmt.Errorf("version %s (#%d) of %s on %s runs %s, which is not pinned by digest: a promote moves the same bytes, so it needs <repo>@sha256:<digest>; "+
			"deploy the DAG to %s with `dexaflow deploy`, which pins the image, then promote that version", v.label, v.number, p.dagID, p.from.target.Name, v.image, p.from.target.Name)
	}
	spec, err := readVersionSpec(ctx, p.from.target.Server, p.from.token, p.dagID, v)
	if err != nil {
		return sourceVersion{}, err
	}
	return sourceVersion{version: v, spec: spec}, nil
}

// pickPromoted is the version sel names (findVersion), or the current one
// without sel.
func pickPromoted(dagID, target string, versions []listedVersion, sel string) (listedVersion, error) {
	if sel != "" {
		return findVersion(dagID, versions, sel)
	}
	for _, v := range versions {
		if v.current {
			return v, nil
		}
	}
	return listedVersion{}, fmt.Errorf("%s has no current version on %s; pass --version (see `dexaflow dags versions %s --target %s`)", dagID, target, dagID, target)
}

// readVersionSpec reads the dag.json registered with version v with the
// public client (apiclient.Client.GetDagVersionSpec, GET
// /api/v2/dags/{dag_id}/dagVersions/{n}/spec), and checks it is that
// version's (its dag_id, label and image), so nothing but that version is
// ever promoted.
func readVersionSpec(ctx context.Context, serverURL, token, dagID string, v listedVersion) ([]byte, error) {
	c, err := cliBareAPIClient(serverURL, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("building the client: %w", err)
	}
	var editors []apiclient.RequestEditorFn
	if token != "" {
		editors = append(editors, func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		})
	}
	body, err := c.GetDagVersionSpec(ctx, dagID, v.number, editors...)
	var se *apiclient.StatusError
	switch {
	case errors.As(err, &se) && se.StatusCode == http.StatusNotFound:
		// The version was just listed: either it was deleted since, or the
		// server predates the route.
		return nil, fmt.Errorf("reading the spec of %s version %s: %w\n  The source may predate serving a version's spec "+
			"(GET /api/v2/dags/{dag_id}/dagVersions/{n}/spec); upgrade it to promote from it", dagID, v.label, apiStatusError(se.StatusCode, se.Body))
	case errors.As(err, &se) && se.StatusCode == http.StatusForbidden:
		// The spec holds the DAG's source, env literals and params, so it is
		// a read: a deploy-only token may not read it (ADR 0074).
		return nil, fmt.Errorf("reading the spec of %s version %s: %w\n  Reading a version's spec needs read on dags on the source, and an API token the dexaflow:read scope "+
			"(a deploy-only token cannot): pass --from-token with one, or sign in with `%s --target <from>`", dagID, v.label, apiStatusError(se.StatusCode, se.Body), loginCommandPath)
	case errors.As(err, &se):
		return nil, fmt.Errorf("reading the spec of %s version %s: %w", dagID, v.label, apiStatusError(se.StatusCode, se.Body))
	case err != nil:
		return nil, fmt.Errorf("reading the spec of %s version %s: %w", dagID, v.label, err)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(body, &spec); jerr != nil {
		return nil, fmt.Errorf("reading the spec of %s version %s: decoding it: %w", dagID, v.label, jerr)
	}
	if spec.DagID != dagID || spec.DagVersion != v.label || spec.Image != v.image {
		return nil, fmt.Errorf("the source served the spec of %s %s (image %s) for version %s (#%d), not that version's; nothing was promoted",
			spec.DagID, spec.DagVersion, spec.Image, v.label, v.number)
	}
	return body, nil
}

// destImage is the image the destination registers, and whether it is a
// copy.
type destImage struct {
	ref  string
	copy bool
	// registry and source name the destination registry and where it came
	// from, for a copy; why says why there is none.
	registry, source, why string
	// repo is the repository of the copy.
	repo string
}

// destinationImage decides what image the destination registers: a copy in
// the registry it pulls from, when that registry is known and image is not
// already in it, else image itself.
func (p *promoteRun) destinationImage(ctx context.Context, image string) (destImage, error) {
	registry, source := p.destinationRegistry(ctx)
	if registry == "" {
		return destImage{ref: image, why: "no registry is set for " + p.to.target.Name}, nil
	}
	repo, digest, _ := strings.Cut(image, "@")
	if strings.HasPrefix(repo, registry+"/") {
		return destImage{ref: image, why: "already in " + registry + ", the registry " + p.to.target.Name + " pulls from"}, nil
	}
	// The repository path under the source registry host is kept, so
	// team-a/etl and team-b/etl stay apart under the destination registry.
	srcRef, err := name.ParseReference(image)
	if err != nil {
		return destImage{}, fmt.Errorf("image %q is not a valid reference: %w", image, err)
	}
	dstRepo := registry + "/" + srcRef.Context().RepositoryStr()
	if _, err := name.NewRepository(dstRepo); err != nil {
		return destImage{}, fmt.Errorf("the destination repository %s (registry %s, from %s) is not valid: %w", dstRepo, registry, source, err)
	}
	return destImage{ref: dstRepo + "@" + digest, copy: true, registry: registry, source: source, repo: dstRepo}, nil
}

// destinationRegistry is the registry prefix the destination pulls from, and
// where it came from: --registry, the --to target's registry, then the
// deploy defaults its control plane advertises. "" when none is set.
func (p *promoteRun) destinationRegistry(ctx context.Context) (registry, source string) {
	trim := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
	if r := trim(p.o.registry); r != "" {
		return r, "--registry"
	}
	if r := trim(p.to.target.Defaults.Registry); r != "" {
		return r, "target " + p.to.target.Name
	}
	d, found, err := fetchClusterDefaults(ctx, p.to.target.Server, p.to.token)
	if err != nil {
		devPrintf(p.cmd.ErrOrStderr(), "note: could not read the deploy defaults of %s (%v); the image is registered as it is\n", p.to.target.Server, err)
		return "", ""
	}
	if found && trim(d.Registry) != "" {
		return trim(d.Registry), "deploy.defaults.registry of " + p.to.target.Name
	}
	return "", ""
}

// copyImage copies image to dest's repository by digest, tagged with label
// when it is a valid tag, with the registry client deploy uses (credentials
// as registry.auth_method auto reads them, ADR 0080). A failure names the
// command that signs in to each registry.
func copyImage(ctx context.Context, out io.Writer, image string, dest destImage, label string) error {
	tag := label
	if _, err := name.NewTag(dest.repo + ":" + label); err != nil {
		tag = ""
	}
	got, err := registryFor(ctx, nil).Copy(ctx, image, dest.repo, tag)
	if err != nil {
		return fmt.Errorf("copying the image to %s: %w\n%s", dest.registry, err, copyLoginHint(image, dest.repo))
	}
	devPrintf(out, "copied %s to %s\n", image, got)
	return nil
}

// copyLoginHint names, for the source and the destination registry, the
// command that signs in to it (build.LoginCommand) and, for a registry a
// cloud owns, the session the client also reads.
func copyLoginHint(src, dstRepo string) string {
	var b strings.Builder
	b.WriteString("  The copy reads the source and pushes to the destination with the credentials of `dexaflow registry login` or `docker login` and of a cloud CLI session. Sign in to each:")
	for _, ref := range []string{src, dstRepo} {
		host := ref
		if r, err := name.ParseReference(ref); err == nil {
			host = r.Context().RegistryStr()
		} else if repo, rerr := name.NewRepository(ref); rerr == nil {
			host = repo.RegistryStr()
		}
		fmt.Fprintf(&b, "\n      %s", build.LoginCommand(host, ""))
		if cloud, ok := build.DetectCloudRegistry(host); ok {
			fmt.Fprintf(&b, "   (or sign in to %s: %s)", cloud.Provider, cloud.SessionCommand())
		}
	}
	return b.String()
}

// dryRunRegister runs the destination's dry run of the register: a refusal
// stops the promote before anything is copied; a server without the route
// is a note.
func (p *promoteRun) dryRunRegister(ctx context.Context, post versionPost, label string) error {
	post.dryRun = true
	status, header, body, err := postVersion(ctx, post)
	if err != nil {
		return fmt.Errorf("reaching %s at %s: %w", p.to.target.Name, p.to.target.Server, err)
	}
	if header.Get(dryRunHeader) != "true" {
		switch status {
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			// A server from before the dry run route: the register checks.
			devPrintf(p.cmd.ErrOrStderr(), "note: %s offers no dry run of the register (an older server); its checks run when it registers\n", p.to.target.Server)
			return nil
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%s refused the token: %w\n  Sign in (%s --target %s) or pass --to-token", p.to.target.Name, apiStatusError(status, body), loginCommandPath, p.to.target.Name)
		}
		return fmt.Errorf("%s answered the dry run of the register with %w; nothing was copied or registered", p.to.target.Name, apiStatusError(status, body))
	}
	if status >= http.StatusMultipleChoices {
		return fmt.Errorf("%s would refuse this version: %w", p.to.target.Name, promoteConflictHint(apiStatusError(status, body), status, label))
	}
	var answer dryRunAnswer
	if jerr := json.Unmarshal(body, &answer); jerr != nil {
		return fmt.Errorf("decoding the dry run answer of %s: %w", p.to.target.Name, jerr)
	}
	for _, w := range answer.Warnings {
		devPrintf(p.cmd.ErrOrStderr(), "warning: %s: %s\n", p.to.target.Name, w)
	}
	p.rep.DryRun = &dryRunReply{Created: answer.Created, DagCreated: answer.DagCreated, IsPaused: answer.IsPaused, Warnings: answer.Warnings}
	devPrintf(p.cmd.OutOrStdout(), "preflight:  %s would register %s %s as %s\n", p.to.target.Name, p.dagID, label, dryRunOutcome(answer))
	return nil
}

// promoteConflictHint explains a 409: the destination holds the label with
// other content, which a promote cannot replace.
func promoteConflictHint(err error, status int, label string) error {
	if status != http.StatusConflict {
		return err
	}
	return fmt.Errorf("%w\nhint: the destination already has version %q with other content, and versions are immutable; "+
		"promote another version, or deploy this one to the destination under a new --dag-version", err, label)
}

// register posts the version to the destination and reports the outcome.
func (p *promoteRun) register(ctx context.Context, post versionPost, label string) error {
	status, _, body, err := postVersion(ctx, post)
	if err != nil {
		return err
	}
	if status >= http.StatusMultipleChoices {
		return promoteConflictHint(apiStatusError(status, body), status, label)
	}
	var answer registerAnswer
	_ = json.Unmarshal(body, &answer) //nolint:errcheck // an older server's body may say less; the register itself succeeded
	p.rep.SpecHash, p.rep.IsPaused = answer.SpecHash, answer.IsPaused
	p.rep.DagCreated = answer.DagCreated != nil && *answer.DagCreated
	out := p.cmd.OutOrStdout()
	if status == http.StatusOK {
		p.rep.Status = promoteUnchanged
		devPrintf(out, "%s %s was already registered on %s with this content; nothing new was registered, and it may not be the current version there (see `dexaflow dags versions %s --target %s`)\n",
			p.dagID, label, p.to.target.Name, p.dagID, p.to.target.Name)
		return nil
	}
	p.rep.Status = promoteRegistered
	devPrintf(out, "Promoted %s %s from %s to %s\n  image %s\n", p.dagID, label, p.from.target.Name, p.to.target.Name, post.promotedImage())
	res := deployResult{DagID: p.dagID, DagCreated: p.rep.DagCreated}
	return printNextStep(ctx, out, p.to.target.Server, p.to.token, &res, false)
}

// promotedImage is the image of p's spec, "" when it names none.
func (p versionPost) promotedImage() string {
	var spec struct {
		Image string `json:"image"`
	}
	_ = json.Unmarshal(p.spec, &spec) //nolint:errcheck // the spec was decoded before; this only names the image
	return spec.Image
}
