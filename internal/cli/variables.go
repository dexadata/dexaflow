package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// newVariablesCommand groups the Airflow-style Variable CRUD subcommands, the
// first-class replacement for curl against /api/v2/variables (#881). The server
// masks values of secret-ish keys on read; the list printer never prints the
// value column, so a secret cannot leak through the table.
func newVariablesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "variables",
		Short: "Manage control-plane variables.",
	}
	cmd.AddCommand(newVariablesSetCommand())
	cmd.AddCommand(newVariablesListCommand())
	cmd.AddCommand(newVariablesGetCommand())
	cmd.AddCommand(newVariablesDeleteCommand())
	return cmd
}

func newVariablesSetCommand() *cobra.Command {
	var serverURL, token, desc string
	var valueStdin bool
	cmd := &cobra.Command{
		Use:   "set <key> [value]",
		Short: "Create or replace a variable (upsert).",
		Long: "Upserts a variable: creates it, or replaces an existing one with the " +
			"same key. Pass the value as the second argument, or use --value-stdin to " +
			"read it from stdin (keeps a secret out of your shell history and argv).",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			var positional string
			var havePositional bool
			if len(args) == 2 {
				positional, havePositional = args[1], true
			}
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			value, verr := resolveVariableValue(cmd, positional, havePositional, valueStdin)
			if verr != nil {
				return verr
			}
			body := apiclient.VariableBody{Key: key, Value: &value}
			if cmd.Flags().Changed("description") {
				body.Description = &desc
			}
			v, err := setVariableReq(cmdContext(cmd), base, bearer, body)
			if err != nil {
				return err
			}
			_, werr := fmt.Fprintf(cmd.OutOrStdout(), "Set variable %q.\n", v.Key)
			return werr
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	cmd.Flags().StringVar(&desc, "description", "", "human-readable description")
	cmd.Flags().BoolVar(&valueStdin, "value-stdin", false, "read the value from stdin instead of the positional argument")
	return cmd
}

// resolveVariableValue applies the value-source precedence: --value-stdin reads
// stdin (and conflicts with a positional value); otherwise the positional value
// is used. A value must be supplied one way or the other — omitting it entirely
// is an error rather than a silent overwrite-to-empty (which would quietly blank
// an existing variable on an upsert). An explicit empty value is still allowed
// via `set <key> ""`.
func resolveVariableValue(cmd *cobra.Command, positional string, havePositional, stdin bool) (string, error) {
	if stdin {
		if havePositional {
			return "", fmt.Errorf("a positional value and --value-stdin are mutually exclusive")
		}
		return readValueStdin(cmd.InOrStdin())
	}
	if !havePositional {
		return "", fmt.Errorf("a value is required: pass it as the second argument or via --value-stdin " +
			"(use `set <key> \"\"` to set an explicit empty value)")
	}
	return positional, nil
}

// readValueStdin reads the whole of stdin as the variable value, stripping a
// single trailing newline so `printf '%s' v | ...` and `echo v | ...` agree.
func readValueStdin(r io.Reader) (string, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("reading value from stdin: %w", err)
	}
	return strings.TrimSuffix(string(raw), "\n"), nil
}

func newVariablesListCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List variables (encrypted values not shown).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			coll, err := listVariablesReq(cmdContext(cmd), base, bearer)
			if err != nil {
				return err
			}
			return printVariableList(cmd.OutOrStdout(), coll)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	return cmd
}

func newVariablesGetCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Show a variable (value masked when the key looks sensitive).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			v, err := getVariableReq(cmdContext(cmd), base, bearer, args[0])
			if err != nil {
				return err
			}
			return printVariable(cmd.OutOrStdout(), v)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	return cmd
}

func newVariablesDeleteCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "delete <key>",
		Short: "Delete a variable.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			if err := deleteVariableReq(cmdContext(cmd), base, bearer, key); err != nil {
				return err
			}
			_, werr := fmt.Fprintf(cmd.OutOrStdout(), "Deleted variable %q.\n", key)
			return werr
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	return cmd
}

// setVariableReq posts (upserts) a variable through the shared typed client and
// returns the masked variable the server echoes back.
func setVariableReq(ctx context.Context, serverURL, token string, body apiclient.VariableBody) (apiclient.Variable, error) {
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return apiclient.Variable{}, err
	}
	resp, err := c.CreateVariableWithResponse(ctx, body)
	if err != nil {
		return apiclient.Variable{}, fmt.Errorf("posting to %s/api/v2/variables: %w", serverURL, err)
	}
	if resp.StatusCode() != http.StatusCreated {
		return apiclient.Variable{}, connError(resp.StatusCode(), resp.Body)
	}
	if resp.JSON201 == nil {
		return apiclient.Variable{}, fmt.Errorf("server returned no variable")
	}
	return *resp.JSON201, nil
}

// listVariablesReq gets the variable collection through the typed client.
func listVariablesReq(ctx context.Context, serverURL, token string) (*apiclient.VariableCollection, error) {
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return nil, err
	}
	resp, err := c.ListVariablesWithResponse(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("listing variables: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, connError(resp.StatusCode(), resp.Body)
	}
	return resp.JSON200, nil
}

// getVariableReq fetches one variable through the typed client.
func getVariableReq(ctx context.Context, serverURL, token, key string) (apiclient.Variable, error) {
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return apiclient.Variable{}, err
	}
	resp, err := c.GetVariableWithResponse(ctx, key)
	if err != nil {
		return apiclient.Variable{}, fmt.Errorf("getting variable %q: %w", key, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return apiclient.Variable{}, connError(resp.StatusCode(), resp.Body)
	}
	if resp.JSON200 == nil {
		return apiclient.Variable{}, fmt.Errorf("server returned no variable")
	}
	return *resp.JSON200, nil
}

// deleteVariableReq deletes one variable through the typed client.
func deleteVariableReq(ctx context.Context, serverURL, token, key string) error {
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return err
	}
	resp, err := c.DeleteVariableWithResponse(ctx, key)
	if err != nil {
		return fmt.Errorf("deleting variable %q: %w", key, err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		return connError(resp.StatusCode(), resp.Body)
	}
	return nil
}

// printVariableList renders the collection as an aligned table. The value column
// is deliberately omitted: encrypted values are masked server-side anyway, and
// omitting it entirely keeps even non-sensitive values off an operator's screen
// by default (use `variables get <key>` to read one).
func printVariableList(w io.Writer, coll *apiclient.VariableCollection) error {
	if coll == nil || coll.Variables == nil || len(*coll.Variables) == 0 {
		_, err := fmt.Fprintln(w, "No variables.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "KEY\tDESCRIPTION\tENCRYPTED"); err != nil {
		return err
	}
	for _, v := range *coll.Variables {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", v.Key, deref(v.Description), yesNo(v.IsEncrypted)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// printVariable prints a single variable's fields as key/value lines. The value
// is whatever the server returned — already masked when the key looks sensitive.
func printVariable(w io.Writer, v apiclient.Variable) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	rows := [][2]string{
		{"key", v.Key},
		{"value", v.Value},
		{"description", deref(v.Description)},
		{"is_encrypted", yesNo(v.IsEncrypted)},
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", r[0], r[1]); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// yesNo renders a bool as the "yes"/"no" the tables use elsewhere.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
