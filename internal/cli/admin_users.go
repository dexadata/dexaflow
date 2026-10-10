package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// newAdminUsersCommand groups the account-inspection operator commands. Like
// the rest of the admin tree it operates a deployed control plane over the
// typed /api/v2 client rather than the local authoring project.
func newAdminUsersCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "users",
		Short: "Inspect accounts on the running control plane.",
	}
	cmd.AddCommand(newAdminUsersListCommand())
	return cmd
}

// newAdminUsersListCommand builds `dexaflow admin users list`: a single page of
// the account list, bounded by --limit/--offset — the "who has access?" query.
// A Lite control plane returns an empty collection, which lists as no users
// rather than an error.
func newAdminUsersListCommand() *cobra.Command {
	var f adminFlags
	var limit, offset int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List accounts (email, roles, active, age), bounded by --limit/--offset.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := adminClient(cmd, f)
			if err != nil {
				return err
			}
			users, err := collectUsers(cmdContext(cmd), c, limit, offset)
			if err != nil {
				return err
			}
			return renderUsers(cmd.OutOrStdout(), users)
		},
	}
	addAdminFlags(cmd, &f)
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum number of accounts to return")
	cmd.Flags().IntVar(&offset, "offset", 0, "number of accounts to skip before returning results")
	return cmd
}

// collectUsers fetches a single page of the account list honoring the caller's
// limit/offset, and returns the decoded accounts.
func collectUsers(ctx context.Context, c *apiclient.ClientWithResponses, limit, offset int) ([]apiclient.UserListItem, error) {
	lim, off := limit, offset
	resp, err := c.ListUsersWithResponse(ctx, &apiclient.ListUsersParams{Limit: &lim, Offset: &off})
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, apiStatusError(resp.StatusCode(), resp.Body)
	}
	if resp.JSON200.Users == nil {
		return []apiclient.UserListItem{}, nil
	}
	return *resp.JSON200.Users, nil
}

// renderUsers prints the accounts as an aligned table, or a friendly note when
// there are none.
func renderUsers(w io.Writer, users []apiclient.UserListItem) error {
	if len(users) == 0 {
		return writeLine(w, "No users found.")
	}
	lines := make([]string, 0, len(users)+1)
	lines = append(lines, fmt.Sprintf("%-30s %-24s %-24s %-6s %s", "EMAIL", "ID", "ROLES", "ACTIVE", "AGE"))
	for _, u := range users {
		created := u.CreatedAt
		lines = append(lines, fmt.Sprintf("%-30s %-24s %-24s %-6s %s",
			u.Email, u.Id, strings.Join(u.Roles, ","), strconv.FormatBool(u.IsActive), ageString(&created)))
	}
	return writeLine(w, strings.Join(lines, "\n"))
}
