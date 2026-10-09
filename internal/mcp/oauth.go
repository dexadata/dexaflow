package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// wellKnownResourcePath is where RFC 9728 places protected resource metadata.
const wellKnownResourcePath = "/.well-known/oauth-protected-resource"

// ProtectedResource is the OAuth protected resource metadata (RFC 9728) the
// HTTP transport advertises, so an MCP client that signs in with OAuth (the
// MCP authorization specification) can find the authorization server (#1470).
// The zero value turns it off.
//
// It only points clients at the authorization server. The tokens they bring
// are verified by /api/v2 on every call, as any other bearer (ADR 0050 D9).
type ProtectedResource struct {
	// Resource is this MCP endpoint's URL as clients reach it, such as
	// https://dexaflow.example.com/mcp.
	Resource string
	// AuthorizationServers are the issuer URLs of the OAuth authorization
	// servers that mint tokens for Resource.
	AuthorizationServers []string
	// Scopes are the scopes advertised as scopes_supported; empty omits it.
	Scopes []string
}

// Enabled reports whether a resource is configured.
func (p ProtectedResource) Enabled() bool { return p.Resource != "" }

// Validate checks a configured resource: an absolute https URL (http only on
// a loopback host) without query or fragment, at least one authorization
// server of the same form, and no empty scope. The zero value is valid.
func (p ProtectedResource) Validate() error {
	if !p.Enabled() {
		if len(p.AuthorizationServers) > 0 || len(p.Scopes) > 0 {
			return errors.New("mcp resource is required when authorization servers or scopes are set")
		}
		return nil
	}
	if err := checkURL("mcp resource", p.Resource); err != nil {
		return err
	}
	if err := checkResourcePath(p.Resource); err != nil {
		return err
	}
	if len(p.AuthorizationServers) == 0 {
		return errors.New("mcp authorization servers are required when the resource is set")
	}
	for _, s := range p.AuthorizationServers {
		if err := checkURL("mcp authorization server", s); err != nil {
			return err
		}
	}
	for _, s := range p.Scopes {
		if strings.TrimSpace(s) == "" {
			return errors.New("mcp scopes must not be empty")
		}
	}
	return nil
}

// checkURL refuses anything but an absolute https URL, or http on a loopback
// host for local development, with no query or fragment.
func checkURL(what, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s %q must be an absolute URL without query or fragment", what, raw)
	}
	if u.Scheme == "https" || (u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil
	}
	return fmt.Errorf("%s %q must use https (http only on a loopback host)", what, raw)
}

// checkResourcePath refuses a resource path the metadata route cannot carry
// verbatim: anything but letters, digits and -._~ in its segments, an empty or
// dot segment, or a percent escape. The metadata path is the well-known prefix
// followed by this path (RFC 9728 section 3.1), so a space or a brace would
// otherwise reach the HTTP mux as a method or a wildcard, and an escape or a
// dot segment would be served at a path the client does not derive.
func checkResourcePath(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("mcp resource %q: %w", raw, err)
	}
	path := u.EscapedPath()
	if path == "" || path == "/" {
		return nil
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, seg := range segments {
		if seg == "" && i == len(segments)-1 {
			continue // one trailing slash is part of the resource
		}
		if seg == "" || seg == "." || seg == ".." || !isPlainSegment(seg) {
			return fmt.Errorf("mcp resource %q: path %q may hold only letters, digits and -._~ in non-empty segments", raw, path)
		}
	}
	return nil
}

func isPlainSegment(seg string) bool {
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '.', r == '_', r == '~':
		default:
			return false
		}
	}
	return true
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// metadataPath is the path RFC 9728 section 3.1 derives from the resource:
// the well-known prefix followed by the resource's own path, verbatim. Only a
// path that is a lone slash is dropped; a trailing slash after a path stays.
func (p ProtectedResource) metadataPath() string {
	u, err := url.Parse(p.Resource)
	if err != nil {
		return wellKnownResourcePath
	}
	path := u.EscapedPath()
	if path == "/" {
		path = ""
	}
	return wellKnownResourcePath + path
}

// MetadataURL is the absolute URL of the metadata document, the one a 401
// names in its WWW-Authenticate challenge.
func (p ProtectedResource) MetadataURL() string {
	u, err := url.Parse(p.Resource)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + p.metadataPath()
}

// HTTPHandler serves srv over stateless Streamable HTTP at /mcp, with
// GET /healthz. With p enabled it also serves the metadata document, at the
// RFC 9728 path and at the bare well-known path, and answers an /mcp request
// that carries no bearer with 401 and a WWW-Authenticate pointing at it.
func HTTPHandler(srv *mcpsdk.Server, p ProtectedResource) http.Handler {
	var mcpHandler http.Handler = mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return srv },
		&mcpsdk.StreamableHTTPOptions{Stateless: true},
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if p.Enabled() {
		meta := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
			Resource:               p.Resource,
			AuthorizationServers:   p.AuthorizationServers,
			ScopesSupported:        p.Scopes,
			BearerMethodsSupported: []string{"header"},
		})
		mux.Handle(wellKnownResourcePath, meta)
		if path := p.metadataPath(); path != wellKnownResourcePath {
			// A pattern ending in a slash matches a whole subtree; {$}
			// keeps the metadata at exactly its path.
			if strings.HasSuffix(path, "/") {
				path += "{$}"
			}
			mux.Handle(path, meta)
		}
		mcpHandler = auth.RequireBearerToken(anyBearer, &auth.RequireBearerTokenOptions{
			ResourceMetadataURL:    p.MetadataURL(),
			AllowMissingExpiration: true,
		})(mcpHandler)
	}
	mux.Handle("/mcp", mcpHandler)
	return mux
}

// anyBearer accepts every bearer it is given. The MCP server holds no key to
// verify one; /api/v2 verifies it on every call the tools make, so the
// challenge here only tells a client without a token where to get one.
func anyBearer(context.Context, string, *http.Request) (*auth.TokenInfo, error) {
	return &auth.TokenInfo{}, nil
}
