package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// newDagsVersionsCommand lists a DAG's registered versions with their
// provenance: who registered each, when, from which commit and image, and
// which one new runs use (#1702).
func newDagsVersionsCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "versions <dag_id>",
		Short: "List a DAG's registered versions, marking the current one.",
		Long: "Lists the versions registered for a DAG, newest first: the version number, the label " +
			"(bundle_version), a * on the current version (the one new runs use), when it was registered and " +
			"the id of the user who registered it (`dexaflow admin users list` maps ids to emails), " +
			"the git commit it was built from (when the deploy recorded one) and " +
			"the image digest it runs. Make another version current with `dexaflow rollback`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newAPIClient(cmd, serverURL, token)
			if err != nil {
				return err
			}
			versions, err := listVersions(cmdContext(cmd), c, args[0])
			if err != nil {
				return err
			}
			return printVersions(cmd.OutOrStdout(), versions)
		},
	}
	addServerFlags(cmd, &serverURL, &token)
	return cmd
}

// newRollbackCommand makes an earlier (or any registered) version of a DAG
// current again through PUT /api/v2/dags/{dag_id}/currentVersion.
func newRollbackCommand() *cobra.Command {
	var serverURL, token, to string
	cmd := &cobra.Command{
		Use:   "rollback <dag_id>",
		Short: "Make an earlier registered version of a DAG current again.",
		Long: "Makes a registered version of the DAG the one new runs use, with that version's " +
			"schedule. Without --to it is the version registered before the current one; --to takes a " +
			"label (bundle_version) or a version number from `dexaflow dags versions`, matched as a label first " +
			"and then as a number. Runs that already " +
			"exist keep the version they started with, and the DAG stays paused or unpaused as it is. " +
			"Nothing is rebuilt: the version's pinned image is used as registered.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dagID := args[0]
			c, err := newAPIClient(cmd, serverURL, token)
			if err != nil {
				return err
			}
			ctx := cmdContext(cmd)
			versions, err := listVersions(ctx, c, dagID)
			if err != nil {
				return err
			}
			target, err := rollbackTarget(dagID, versions, to)
			if err != nil {
				return err
			}
			return setCurrentVersion(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), c, dagID, target)
		},
	}
	addServerFlags(cmd, &serverURL, &token)
	cmd.Flags().StringVar(&to, "to", "", "version to make current: matched first as a label, then as a version number (default: the one before the current version)")
	return cmd
}

// addServerFlags adds the --server and --token flags every control-plane
// command takes.
func addServerFlags(cmd *cobra.Command, serverURL, token *string) {
	cmd.Flags().StringVar(serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
}

// newAPIClient resolves the server and token the usual way and builds the
// typed client.
func newAPIClient(cmd *cobra.Command, serverURL, token string) (*apiclient.ClientWithResponses, error) {
	base, bearer, err := resolveServerToken(cmd, serverURL, token)
	if err != nil {
		return nil, err
	}
	c, err := cliAPIClient(base, bearer)
	if err != nil {
		return nil, fmt.Errorf("building the API client: %w", err)
	}
	return c, nil
}

// listedVersion is one registered version as the CLI shows it.
type listedVersion struct {
	number    int
	label     string
	current   bool
	createdAt *time.Time
	createdBy string
	image     string
	// buildInfo is where the version was built (#1702); nil when the
	// server recorded none or predates it. A promote forwards it.
	buildInfo *domain.BuildInfo
}

// listVersions reads the DAG's versions, newest first.
func listVersions(ctx context.Context, c *apiclient.ClientWithResponses, dagID string) ([]listedVersion, error) {
	resp, err := c.ListDagVersionsWithResponse(ctx, dagID)
	if err != nil {
		return nil, fmt.Errorf("listing versions of %s: %w", dagID, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, apiStatusError(resp.StatusCode(), resp.Body)
	}
	if resp.JSON200 == nil || resp.JSON200.DagVersions == nil {
		return nil, nil
	}
	out := make([]listedVersion, 0, len(*resp.JSON200.DagVersions))
	for _, v := range *resp.JSON200.DagVersions {
		out = append(out, listedVersion{
			number: valueOf(v.VersionNumber), label: valueOf(v.BundleVersion), current: valueOf(v.IsCurrent),
			createdAt: v.CreatedAt, createdBy: valueOf(v.CreatedBy), image: valueOf(v.Image),
			buildInfo: buildInfoOf(v.BuildInfo),
		})
	}
	return out, nil
}

// valueOf returns *p, or the zero value for a nil p.
func valueOf[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// printVersions renders the versions as an aligned table.
func printVersions(w io.Writer, versions []listedVersion) error {
	if len(versions) == 0 {
		_, err := fmt.Fprintln(w, "No versions registered.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "VERSION\tLABEL\tCURRENT\tCREATED\tCREATOR ID\tCOMMIT\tIMAGE"); err != nil {
		return err
	}
	for _, v := range versions {
		marker, created, by := "", "-", v.createdBy
		if v.current {
			marker = "*"
		}
		if v.createdAt != nil {
			created = v.createdAt.UTC().Format("2006-01-02 15:04")
		}
		if by == "" {
			by = "-"
		}
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", v.number, v.label, marker, created, by, v.shortCommit(), shortImage(v.image)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// shortCommit is the first 12 characters of the commit the version was
// built from, "-" when none was recorded.
func (v listedVersion) shortCommit() string {
	if v.buildInfo == nil || v.buildInfo.Commit == "" {
		return "-"
	}
	c := v.buildInfo.Commit
	if len(c) > 12 {
		c = c[:12]
	}
	return c
}

// buildInfoOf maps the build info a server listed to the domain's, dropping
// what this CLI would not send again (domain.BuildInfo.Sanitized): a
// promote forwards it to another server as the caller's claim. Nil when
// none was listed.
func buildInfoOf(b *apiclient.BuildInfo) *domain.BuildInfo {
	if b == nil {
		return nil
	}
	out := domain.BuildInfo{
		Commit: valueOf(b.Commit), Repo: valueOf(b.Repo), PipelineURL: valueOf(b.PipelineUrl),
		Builder: valueOf(b.Builder), BaseImage: valueOf(b.BaseImage), CLIVersion: valueOf(b.CliVersion),
	}.Sanitized()
	if out.IsZero() {
		return nil
	}
	return &out
}

// shortImage shortens a digest-pinned image to its first 12 digest characters
// (sha256:0123456789ab); an image without a digest is shown as it is.
func shortImage(image string) string {
	_, digest, ok := strings.Cut(image, "@")
	if !ok {
		return image
	}
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok || len(hex) <= 12 {
		return digest
	}
	return algo + ":" + hex[:12]
}

// rollbackTarget picks the version a rollback makes current: the one --to
// names (a label first, then a version number), or without --to the newest
// version older than the current one.
func rollbackTarget(dagID string, versions []listedVersion, to string) (listedVersion, error) {
	if to != "" {
		return findVersion(dagID, versions, to)
	}
	current := -1
	for _, v := range versions {
		if v.current {
			current = v.number
		}
	}
	if current < 0 {
		return listedVersion{}, fmt.Errorf("%s has no current version; pass --to", dagID)
	}
	var best *listedVersion
	for i, v := range versions {
		if v.number < current && (best == nil || v.number > best.number) {
			best = &versions[i]
		}
	}
	if best == nil {
		return listedVersion{}, fmt.Errorf("%s has no version before its current version %d; pass --to to pick another", dagID, current)
	}
	return *best, nil
}

// findVersion returns the version sel names: a label first, then a version
// number, as rollback --to and promote --version match them.
func findVersion(dagID string, versions []listedVersion, sel string) (listedVersion, error) {
	for _, v := range versions {
		if v.label == sel {
			return v, nil
		}
	}
	if n, err := strconv.Atoi(sel); err == nil {
		for _, v := range versions {
			if v.number == n {
				return v, nil
			}
		}
	}
	return listedVersion{}, fmt.Errorf("%s has no version %q; list them with `dexaflow dags versions %s`", dagID, sel, dagID)
}

// setCurrentVersion asks the control plane to make target current and prints
// what changed.
func setCurrentVersion(ctx context.Context, out, warn io.Writer, c *apiclient.ClientWithResponses, dagID string, target listedVersion) error {
	label := target.label
	body := apiclient.DagCurrentVersionRequest{Version: &label}
	if label == "" {
		number := target.number
		body = apiclient.DagCurrentVersionRequest{VersionNumber: &number}
	}
	resp, err := c.SetDagCurrentVersionWithResponse(ctx, dagID, body)
	if err != nil {
		return fmt.Errorf("making version %d of %s current: %w", target.number, dagID, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return apiStatusError(resp.StatusCode(), resp.Body)
	}
	if resp.JSON200 == nil {
		return errors.New("the control plane answered without a body")
	}
	change := resp.JSON200
	if change.Warnings != nil {
		printNewWarnings(warn, *change.Warnings, nil)
	}
	to := describeVersion(&change.To)
	if !change.Changed {
		_, werr := fmt.Fprintf(out, "%s already runs %s; nothing changed.\n", dagID, to)
		return werr
	}
	from := "no version"
	if change.From != nil {
		from = describeVersion(change.From)
	}
	_, werr := fmt.Fprintf(out, "%s: current version changed from %s to %s.\n"+
		"New runs use %s from the next scheduler tick; runs already created keep their version.\n",
		dagID, from, to, to)
	return werr
}

// describeVersion names a version as "label (#n)".
func describeVersion(v *apiclient.DagVersion) string {
	return fmt.Sprintf("%s (#%d)", valueOf(v.BundleVersion), valueOf(v.VersionNumber))
}
