package main

import (
	"reflect"
	"testing"

	"github.com/dexadata/dexaflow/internal/mcp"
)

// TestProtectedResourceFromFlags locks the #1470 wiring: the comma-separated
// flags reach the transport's metadata, trimmed, and an invalid set fails.
func TestProtectedResourceFromFlags(t *testing.T) {
	got, err := protectedResource("https://acme.example.com/mcp", "https://auth.example.com, https://backup.example.com", "dexaflow:read,dexaflow:run")

	if err != nil {
		t.Fatalf("protectedResource: %v", err)
	}
	want := mcp.ProtectedResource{
		Resource:             "https://acme.example.com/mcp",
		AuthorizationServers: []string{"https://auth.example.com", "https://backup.example.com"},
		Scopes:               []string{"dexaflow:read", "dexaflow:run"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("protectedResource = %+v, want %+v", got, want)
	}
}

func TestProtectedResourceFromFlagsUnsetIsOff(t *testing.T) {
	got, err := protectedResource("", "", "")

	if err != nil || got.Enabled() {
		t.Errorf("protectedResource(unset) = %+v, %v; want off", got, err)
	}
}

func TestProtectedResourceFromFlagsRefusesAHalfSet(t *testing.T) {
	if _, err := protectedResource("https://acme.example.com/mcp", "", ""); err == nil {
		t.Error("protectedResource without authorization servers = nil error, want refused")
	}
}

// TestProtectedResourceFromFlagsRefusesAnUnroutablePath: a resource path the
// metadata route cannot carry is refused at start-up (exit 2), not a panic in
// the HTTP mux.
func TestProtectedResourceFromFlagsRefusesAnUnroutablePath(t *testing.T) {
	for _, resource := range []string{"https://acme.example.com/a%20b/mcp", "https://acme.example.com/{x}/mcp"} {
		if _, err := protectedResource(resource, "https://auth.example.com", ""); err == nil {
			t.Errorf("protectedResource(%q) = nil error, want refused", resource)
		}
	}
}
