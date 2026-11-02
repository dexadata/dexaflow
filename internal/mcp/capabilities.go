package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// CapabilitiesURI is the resource that says what this server offers (#2178),
// so a client can adapt without calling every tool to find out.
const CapabilitiesURI = "dexaflow://capabilities"

// capabilities is the body of the capabilities resource. tool_groups names
// the optional groups and whether each is on; tools lists every registered
// tool, those a program added included, as tools/list does.
type capabilities struct {
	Server     string          `json:"server"`
	Version    string          `json:"version"`
	Transport  string          `json:"transport"`
	ToolGroups map[string]bool `json:"tool_groups"`
	Tools      []string        `json:"tools"`
}

func (h *handlers) registerCapabilities(s *mcpsdk.Server) {
	s.AddResource(&mcpsdk.Resource{
		Name: "capabilities", URI: CapabilitiesURI, MIMEType: "application/json",
		Description: "What this server offers: its version, transport, which optional tool groups are on, and every tool.",
	}, h.readCapabilities)
}

func (h *handlers) readCapabilities(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	transport := "stdio"
	if h.requireBearer {
		transport = "http"
	}
	body, err := json.Marshal(capabilities{
		Server: serverName, Version: h.version, Transport: transport,
		ToolGroups: map[string]bool{"read": true, "run_control": h.planKey != nil},
		Tools:      h.registeredTools(),
	})
	if err != nil {
		return nil, fmt.Errorf("encoding capabilities: %w", err)
	}
	return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{
		URI: req.Params.URI, MIMEType: "application/json", Text: string(body),
	}}}, nil
}
