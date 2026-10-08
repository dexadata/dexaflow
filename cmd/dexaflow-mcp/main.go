// Command leoflow-mcp runs the Dexaflow Model Context Protocol server (ADR 0050).
// It speaks stdio by default (a local agent — Claude Desktop/Code — against a Lite
// control plane) or Streamable HTTP as an optional Pro service (POST /mcp). Either
// way it reaches the control plane only through /api/v2, carrying the caller's
// token, and is never part of leoflow-server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dexadata/dexaflow/internal/envcompat"
	"github.com/dexadata/dexaflow/internal/mcp"
	versioninfo "github.com/dexadata/dexaflow/internal/version"
	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// version is overridden at build time via -ldflags "-X main.version=...". This
// binary carries its version in package main (not internal/version), so
// -X main.version is the only injection that lands (see .goreleaser.yaml).
var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	envcompat.MirrorProcess()
	// Answer `--version` before parsing flags — flag.Parse would reject an
	// unknown --version — and before any stdout goes to the MCP channel, so an
	// operator can ask the binary its version (#593). Print main.version: this
	// binary's own ldflag target.
	if versioninfo.WantsVersion(os.Args[1:]) {
		fmt.Println("dexaflow-mcp", version)
		return 0
	}

	// Logs go to stderr: on stdio, stdout is the MCP protocol channel and must
	// carry nothing else.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	var server, transport, listen, resource, authServers, scopes string
	flag.StringVar(&server, "server", envOr("LEOFLOW_SERVER_URL", "http://localhost:8080"),
		"control plane base URL")
	flag.StringVar(&transport, "transport", envOr("LEOFLOW_MCP_TRANSPORT", "stdio"),
		"transport: stdio | http")
	flag.StringVar(&listen, "listen", envOr("LEOFLOW_MCP_LISTEN", ":9099"),
		"listen address for the http transport")
	flag.StringVar(&resource, "resource", os.Getenv("LEOFLOW_MCP_RESOURCE"),
		"http transport: this endpoint's URL as clients reach it; with --authorization-servers, serves OAuth protected resource metadata (RFC 9728)")
	flag.StringVar(&authServers, "authorization-servers", os.Getenv("LEOFLOW_MCP_AUTHORIZATION_SERVERS"),
		"http transport: comma-separated issuer URLs of the OAuth authorization servers for --resource")
	flag.StringVar(&scopes, "scopes", os.Getenv("LEOFLOW_MCP_SCOPES"),
		"http transport: comma-separated scopes advertised in the protected resource metadata")
	flag.Parse()

	if transport != "stdio" && transport != "http" {
		slog.Error("unknown transport (want stdio | http)", "transport", transport)
		return 2
	}
	httpMode := transport == "http"

	// stdio: the process token IS the caller's identity. http: identity is the
	// per-request bearer (ADR 0050 D9), so the base client holds NO ambient token
	// — a bearer-less request is refused, never served with a process credential.
	token := os.Getenv("LEOFLOW_TOKEN")
	if httpMode {
		token = ""
	}
	apiClient, err := apiclient.New(server, token)
	if err != nil {
		slog.Error("building control-plane client", "error", err)
		return 1
	}
	srv := mcp.NewServer(apiClient, server, version, httpMode)

	if httpMode {
		pr, err := protectedResource(resource, authServers, scopes)
		if err != nil {
			slog.Error("invalid protected resource metadata", "error", err)
			return 2
		}
		return runHTTP(mcp.HTTPHandler(srv, pr), listen, server)
	}
	slog.Info("leoflow-mcp starting", "server", server, "transport", "stdio", "version", version)
	if err := srv.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		slog.Error("mcp server exited", "error", err)
		return 1
	}
	return 0
}

// runHTTP serves handler (mcp.HTTPHandler: the MCP over Streamable HTTP at
// POST /mcp). Stateless: no session state is kept, so a request is authorized
// purely by its own bearer and the service scales active-active. A stray
// GET/DELETE returns 405 (spec-compliant in stateless mode). Shuts down
// gracefully on SIGINT/SIGTERM.
func runHTTP(handler http.Handler, listen, server string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	httpSrv := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		// Fresh deadline for the drain — ctx is already canceled (that's what woke
		// this goroutine), but WithoutCancel keeps its lineage for contextcheck.
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutCtx); err != nil {
			slog.Warn("http server shutdown", "error", err)
		}
	}()

	slog.Info("leoflow-mcp starting", "server", server, "transport", "http", "listen", listen, "version", version)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("http server exited", "error", err)
		return 1
	}
	return 0
}

// protectedResource builds the transport's OAuth protected resource metadata
// (#1470) from the flags, comma-separated lists split and trimmed. All empty
// leaves it off.
func protectedResource(resource, authServers, scopes string) (mcp.ProtectedResource, error) {
	pr := mcp.ProtectedResource{Resource: resource, AuthorizationServers: splitList(authServers), Scopes: splitList(scopes)}
	return pr, pr.Validate()
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
