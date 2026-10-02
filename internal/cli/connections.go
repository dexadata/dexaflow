package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// newConnectionsCommand groups the Airflow-style Connection CRUD subcommands.
// It is the first-class replacement for hand-rolled curl against
// /api/v2/connections, and makes the repository's "run `dexaflow connections set`"
// hint real (#881). Secrets (password, extra) are sent on write but never echoed
// back — reads are masked by the server and the printers omit the extra column.
func newConnectionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connections",
		Short: "Manage control-plane connections.",
	}
	cmd.AddCommand(newConnectionsSetCommand())
	cmd.AddCommand(newConnectionsListCommand())
	cmd.AddCommand(newConnectionsGetCommand())
	cmd.AddCommand(newConnectionsDeleteCommand())
	return cmd
}

// connectionSetFlags holds the write-path flags for `connections set`.
type connectionSetFlags struct {
	serverURL, token                                     string
	connType, host, login, password, schema, extra, desc string
	extraFile                                            string
	port                                                 int
	passwordStdin                                        bool
}

func newConnectionsSetCommand() *cobra.Command {
	var f connectionSetFlags
	cmd := &cobra.Command{
		Use:   "set <connection_id>",
		Short: "Create or update a connection (upsert).",
		Long: "Creates a connection, or updates an existing one with the same id. " +
			"--conn-type is required.\n\n" +
			"Only the fields you pass are changed; any field you omit keeps its " +
			"current value. So you can change just --host without re-supplying the " +
			"password (which cannot be read back anyway). To clear a field, pass it " +
			"as an empty string, e.g. --login '' or --schema ''.\n\n" +
			"The password and extra are sent to the control plane but never printed " +
			"back; read commands show masked values. Prefer --password-stdin / " +
			"--extra-file over --password / --extra so a secret never lands in your " +
			"shell history or the process table.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			connID := args[0]
			if f.connType == "" {
				return fmt.Errorf("--conn-type is required")
			}
			base, bearer, err := resolveServerToken(cmd, f.serverURL, f.token)
			if err != nil {
				return err
			}
			pw, perr := resolvePassword(cmd, f.password, f.passwordStdin)
			if perr != nil {
				return perr
			}
			extra, xerr := resolveConnectionExtra(cmd, f.extra, f.extraFile)
			if xerr != nil {
				return xerr
			}
			body := connectionBodyFromFlags(cmd, connID, f, pw, extra)
			conn, err := setConnectionReq(cmdContext(cmd), base, bearer, body)
			if err != nil {
				return err
			}
			return printConnectionSet(cmd.OutOrStdout(), conn)
		},
	}
	cmd.Flags().StringVar(&f.serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&f.token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	cmd.Flags().StringVar(&f.connType, "conn-type", "", "connection type, e.g. postgres, http, aws (required)")
	cmd.Flags().StringVar(&f.host, "host", "", "connection host")
	cmd.Flags().StringVar(&f.login, "login", "", "connection login/username")
	cmd.Flags().StringVar(&f.password, "password", "", "connection password (prefer --password-stdin)")
	cmd.Flags().BoolVar(&f.passwordStdin, "password-stdin", false, "read the password from stdin instead of --password (avoids ps/shell-history exposure)")
	cmd.Flags().StringVar(&f.schema, "schema", "", "connection schema/database")
	cmd.Flags().IntVar(&f.port, "port", 0, "connection port (omit to keep the stored value; an explicit value, including 0, overwrites it)")
	cmd.Flags().StringVar(&f.extra, "extra", "", "extra JSON blob (provider-specific; secrets here are masked on read)")
	cmd.Flags().StringVar(&f.extraFile, "extra-file", "", "read the extra JSON from a file instead of --extra (keeps provider secrets out of argv)")
	cmd.Flags().StringVar(&f.desc, "description", "", "human-readable description")
	return cmd
}

// resolveConnectionExtra applies the extra-source precedence: --extra-file reads
// the JSON blob from a file (keeping a provider secret out of argv/shell history)
// and is mutually exclusive with the inline --extra. Returns whether an extra was
// supplied at all so the caller can leave it untouched when neither flag was used.
func resolveConnectionExtra(cmd *cobra.Command, inline, file string) (string, error) {
	byFlag := cmd.Flags().Changed("extra")
	byFile := cmd.Flags().Changed("extra-file")
	if byFlag && byFile {
		return "", fmt.Errorf("--extra and --extra-file are mutually exclusive")
	}
	if byFile {
		data, err := os.ReadFile(file) //nolint:gosec // path is operator-supplied on their own CLI.
		if err != nil {
			return "", fmt.Errorf("reading --extra-file: %w", err)
		}
		// Drop a single trailing newline so a normal text editor's file and an
		// inline --extra produce the identical stored blob (matches --value-stdin).
		return strings.TrimSuffix(string(data), "\n"), nil
	}
	return inline, nil
}

// connectionBodyFromFlags maps the CLI flags to the typed request body, sending
// only the fields the operator actually set: an unset flag is left out (nil), and
// the control plane preserves the stored value for any omitted field (its upsert
// COALESCEs EXCLUDED over the existing row). So changing one field never wipes the
// others — in particular the unreadable password survives a `set --host x`.
func connectionBodyFromFlags(cmd *cobra.Command, connID string, f connectionSetFlags, password, extra string) apiclient.ConnectionBody {
	body := apiclient.ConnectionBody{ConnectionId: &connID, ConnType: f.connType}
	if cmd.Flags().Changed("host") {
		body.Host = &f.host
	}
	if cmd.Flags().Changed("login") {
		body.Login = &f.login
	}
	// Send the password only when it was actually supplied — via --password
	// (including an explicit empty string, which clears it) or --password-stdin.
	// Omitting both leaves it absent (nil), so the control plane preserves the
	// stored, unreadable password. Gating on non-empty would make --password ''
	// a silent no-op, inconsistent with --login '' clearing.
	if cmd.Flags().Changed("password") || cmd.Flags().Changed("password-stdin") {
		body.Password = &password
	}
	if cmd.Flags().Changed("schema") {
		body.Schema = &f.schema
	}
	if cmd.Flags().Changed("port") {
		p := f.port
		body.Port = &p
	}
	if cmd.Flags().Changed("extra") || cmd.Flags().Changed("extra-file") {
		body.Extra = &extra
	}
	if cmd.Flags().Changed("description") {
		body.Description = &f.desc
	}
	return body
}

func newConnectionsListCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List connections (secrets never shown).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			coll, err := listConnectionsReq(cmdContext(cmd), base, bearer)
			if err != nil {
				return err
			}
			return printConnectionList(cmd.OutOrStdout(), coll)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	return cmd
}

func newConnectionsGetCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "get <connection_id>",
		Short: "Show a connection (password omitted, extra masked).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			conn, err := getConnectionReq(cmdContext(cmd), base, bearer, args[0])
			if err != nil {
				return err
			}
			return printConnection(cmd.OutOrStdout(), conn)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	return cmd
}

func newConnectionsDeleteCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "delete <connection_id>",
		Short: "Delete a connection.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			connID := args[0]
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			if err := deleteConnectionReq(cmdContext(cmd), base, bearer, connID); err != nil {
				return err
			}
			_, werr := fmt.Fprintf(cmd.OutOrStdout(), "Deleted connection %q.\n", connID)
			return werr
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	return cmd
}

// setConnectionReq posts (upserts) a connection through the shared typed client
// and returns the masked connection the server echoes back.
func setConnectionReq(ctx context.Context, serverURL, token string, body apiclient.ConnectionBody) (apiclient.Connection, error) {
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return apiclient.Connection{}, err
	}
	resp, err := c.CreateConnectionWithResponse(ctx, body)
	if err != nil {
		return apiclient.Connection{}, fmt.Errorf("posting to %s/api/v2/connections: %w", serverURL, err)
	}
	if resp.StatusCode() != http.StatusCreated {
		return apiclient.Connection{}, connError(resp.StatusCode(), resp.Body)
	}
	if resp.JSON201 == nil {
		return apiclient.Connection{}, fmt.Errorf("server returned no connection")
	}
	return *resp.JSON201, nil
}

// listConnectionsReq gets the connection collection through the typed client.
func listConnectionsReq(ctx context.Context, serverURL, token string) (*apiclient.ConnectionCollection, error) {
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return nil, err
	}
	resp, err := c.ListConnectionsWithResponse(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("listing connections: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, connError(resp.StatusCode(), resp.Body)
	}
	return resp.JSON200, nil
}

// getConnectionReq fetches one connection through the typed client.
func getConnectionReq(ctx context.Context, serverURL, token, connID string) (apiclient.Connection, error) {
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return apiclient.Connection{}, err
	}
	resp, err := c.GetConnectionWithResponse(ctx, connID)
	if err != nil {
		return apiclient.Connection{}, fmt.Errorf("getting connection %q: %w", connID, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return apiclient.Connection{}, connError(resp.StatusCode(), resp.Body)
	}
	if resp.JSON200 == nil {
		return apiclient.Connection{}, fmt.Errorf("server returned no connection")
	}
	return *resp.JSON200, nil
}

// deleteConnectionReq deletes one connection through the typed client.
func deleteConnectionReq(ctx context.Context, serverURL, token, connID string) error {
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return err
	}
	resp, err := c.DeleteConnectionWithResponse(ctx, connID)
	if err != nil {
		return fmt.Errorf("deleting connection %q: %w", connID, err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return connError(resp.StatusCode(), resp.Body)
	}
	return nil
}

// connError renders a non-2xx response as an error carrying the server's body,
// so the encryption-unavailable (503) message and 404s reach the operator.
func connError(status int, body []byte) error {
	return apiStatusError(status, body)
}

// printConnectionSet prints a concise, secret-free confirmation of an upsert.
func printConnectionSet(w io.Writer, c apiclient.Connection) error {
	_, err := fmt.Fprintf(w, "Set connection %q (type %s).\n", c.ConnectionId, c.ConnType)
	return err
}

// printConnectionList renders the collection as an aligned table. The extra
// column is deliberately omitted so no secret can surface, even masked.
func printConnectionList(w io.Writer, coll *apiclient.ConnectionCollection) error {
	if coll == nil || coll.Connections == nil || len(*coll.Connections) == 0 {
		_, err := fmt.Fprintln(w, "No connections.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "CONNECTION_ID\tTYPE\tHOST\tLOGIN\tSCHEMA"); err != nil {
		return err
	}
	for _, c := range *coll.Connections {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			c.ConnectionId, c.ConnType, deref(c.Host), deref(c.Login), deref(c.Schema)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// printConnection prints a single connection's masked fields as key/value lines.
func printConnection(w io.Writer, c apiclient.Connection) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	rows := [][2]string{
		{"connection_id", c.ConnectionId},
		{"conn_type", c.ConnType},
		{"description", deref(c.Description)},
		{"host", deref(c.Host)},
		{"login", deref(c.Login)},
		{"schema", deref(c.Schema)},
		{"port", intPtrString(c.Port)},
		{"extra", deref(c.Extra)},
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", r[0], r[1]); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// intPtrString renders a nullable int pointer, showing an empty string for nil.
func intPtrString(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}
