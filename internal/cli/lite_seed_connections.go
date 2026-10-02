package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"

	"github.com/dexadata/dexaflow/internal/domain"
)

// connectionsToSeed decides which of a DAG's declared connections Lite may take
// from the environment, and reports the ones it refused and why (#1103).
//
// Registration is fail-closed against the vault: a DAG declaring a connection
// the vault does not hold is rejected, with a message naming the fix. In Lite
// that produces a second declaration of the same value, because the subprocess
// executor inherits the environment and connections reach tasks as
// AIRFLOW_CONN_<ID> — so the developer has already exported the connection for
// the task to consume, and must then write it again for registration to pass.
//
// Taking a secret from ambient environment is only defensible under conditions,
// and they are the reason this is a separate, testable decision rather than a
// loop inlined at the call site:
//
//   - DECLARED ONLY. Anything AIRFLOW_CONN_* that the DAG did not declare is
//     ignored. Seeding whatever happens to be exported would copy unrelated
//     credentials out of a developer's shell into a database.
//   - THE VAULT WINS. A connection already stored is never replaced, so a stale
//     export cannot shadow a value somebody set deliberately.
//   - UNUSABLE IS REPORTED, NOT STORED. A URI that does not parse is skipped
//     with its name, never its value: storing garbage would replace "this
//     connection is missing", which registration says clearly, with a
//     connection that exists and fails inside the task.
//   - LITE ONLY. Enforced by the caller — this runs from `leoflow dev`, which
//     is the unsandboxed dev loop, and has no path into a Pro control plane.
//
// lookup is the environment accessor (os.Getenv in production), injected so the
// decision is testable without mutating the process environment.
func connectionsToSeed(declared []string, existing map[string]bool, lookup func(string) string) (seed []domain.Connection, skipped []string) {
	for _, id := range declared {
		if existing[id] {
			continue
		}
		uri := lookup("AIRFLOW_CONN_" + strings.ToUpper(id))
		if uri == "" {
			continue
		}
		// The stored id keeps the DAG's spelling; only the env lookup is
		// upper-cased, because that is the convention for the variable name and
		// not for the connection.
		c, err := domain.ParseConnectionURI(id, uri)
		if err != nil {
			// Never the URI, and never the parser's echo of it: the value is a
			// secret, the reason it was rejected is not.
			skipped = append(skipped, fmt.Sprintf("%s (its AIRFLOW_CONN_%s is not a usable connection URI)", id, strings.ToUpper(id)))
			continue
		}
		seed = append(seed, c)
	}
	sort.Slice(seed, func(i, j int) bool { return seed[i].ConnID < seed[j].ConnID })
	sort.Strings(skipped)
	return seed, skipped
}

// seedDeclaredConnections writes, into the Lite vault, the declared connections
// this machine's environment already carries — so the first `leoflow dev` of a
// project works without declaring the same value twice (#1103).
//
// It is deliberately BEST EFFORT and never fails the reload: registration
// already reports a missing connection with a message naming the fix, and that
// message is a better failure than a boot that dies because seeding could not
// reach the API. Everything it does is announced by name, never by value —
// a secret store that fills itself without saying so is a surprise waiting to
// happen, and the point here is to remove a step the developer knows about, not
// to hide one.
//
// Lite only: this is reachable from `leoflow dev`, the unsandboxed local loop.
// Nothing calls it from a path that can reach a Pro control plane.
func seedDeclaredConnections(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, token, serverURL string) {
	if ws == nil {
		return
	}
	names := declaredConnectionNames(ws)
	if len(names) == 0 {
		return
	}
	c, err := apiclient.New(serverURL, token)
	if err != nil {
		return
	}
	existing := map[string]bool{}
	if resp, lerr := c.ListConnectionsWithResponse(ctx, nil); lerr == nil &&
		resp.JSON200 != nil && resp.JSON200.Connections != nil {
		for _, conn := range *resp.JSON200.Connections {
			existing[conn.ConnectionId] = true
		}
	} else {
		// The vault could not be read, so "already set" is unknown. Seeding now
		// could overwrite a stored secret, which is the one thing this must
		// never do — so do nothing and let registration speak.
		return
	}

	seed, skipped := connectionsToSeed(names, existing, os.Getenv)
	for _, conn := range seed {
		resp, cerr := c.CreateConnectionWithResponse(ctx, connectionBody(conn))
		if cerr != nil || resp.StatusCode() >= 300 {
			devPrintf(cmd.OutOrStdout(), "  (warning) could not seed connection %q from the environment; define it with `dexaflow connections set %s`\n", conn.ConnID, conn.ConnID)
			continue
		}
		devPrintf(cmd.OutOrStdout(), "▸ seeded connection %q from AIRFLOW_CONN_%s (value not shown; `dexaflow connections set` overrides it)\n",
			conn.ConnID, strings.ToUpper(conn.ConnID))
	}
	for _, s := range skipped {
		devPrintf(cmd.OutOrStdout(), "  (warning) not seeding %s\n", s)
	}
}

// declaredConnectionNames collects every connection id the workspace declares,
// at the DAG level and per task, sorted so the seeding output is stable.
func declaredConnectionNames(ws *WorkspaceSpec) []string {
	declared := map[string]bool{}
	for _, p := range ws.Projects {
		if p.Config == nil {
			continue
		}
		for _, id := range p.Config.Connections {
			declared[id] = true
		}
		for _, t := range p.Config.Tasks {
			for _, id := range t.Connections {
				declared[id] = true
			}
		}
	}
	names := make([]string, 0, len(declared))
	for id := range declared {
		names = append(names, id)
	}
	sort.Strings(names)
	return names
}

// connectionBody converts a parsed Connection into the API's create body. Empty
// optional fields are left absent rather than sent as empty strings, so seeding
// writes exactly what the URI carried.
func connectionBody(conn domain.Connection) apiclient.ConnectionBody {
	body := apiclient.ConnectionBody{ConnType: conn.ConnType, Port: conn.Port}
	id := conn.ConnID
	body.ConnectionId = &id
	set := func(dst **string, v string) {
		if v != "" {
			s := v
			*dst = &s
		}
	}
	set(&body.Host, conn.Host)
	set(&body.Login, conn.Login)
	set(&body.Password, conn.Password)
	set(&body.Schema, conn.Schema)
	set(&body.Extra, conn.Extra)
	return body
}
