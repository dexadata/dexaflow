package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

func TestSetVariableReqSendsBodyAndReturnsVariable(t *testing.T) {
	var gotMethod, gotPath, gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"key":"region","value":"us-east-1","is_encrypted":false}`)
	}))
	defer srv.Close()

	value, desc := "us-east-1", "default region"
	v, err := setVariableReq(context.Background(), srv.URL, "admin-jwt", apiclient.VariableBody{
		Key: "region", Value: &value, Description: &desc,
	})
	if err != nil {
		t.Fatalf("setVariableReq: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v2/variables" {
		t.Errorf("request = %s %s, want POST /api/v2/variables", gotMethod, gotPath)
	}
	if gotAuth != "Bearer admin-jwt" {
		t.Errorf("Authorization = %q, want the bearer", gotAuth)
	}
	if !strings.Contains(gotBody, `"key":"region"`) || !strings.Contains(gotBody, "us-east-1") {
		t.Errorf("request body missing key/value: %s", gotBody)
	}
	if v.Key != "region" || v.Value != "us-east-1" {
		t.Errorf("unexpected variable: %+v", v)
	}
}

func TestSetVariableCommandPositionalValue(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"key":"region","value":"us-east-1","is_encrypted":false}`)
	}))
	defer srv.Close()

	cfg := seedSessionConfig(t, srv.URL, "tok")
	out, _, err := run(t, "variables", "set", "region", "us-east-1", "--config", cfg)
	if err != nil {
		t.Fatalf("variables set: %v", err)
	}
	if !strings.Contains(gotBody, "us-east-1") {
		t.Errorf("positional value not sent: %s", gotBody)
	}
	if !strings.Contains(out, "region") {
		t.Errorf("output = %q, want a confirmation naming the key", out)
	}
}

func TestSetVariableCommandValueStdin(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"key":"apikey","value":"***","is_encrypted":false}`)
	}))
	defer srv.Close()

	cfg := seedSessionConfig(t, srv.URL, "tok")
	out, _, err := runWithStdin(t, "SECRET-VALUE\n", "variables", "set", "apikey", "--config", cfg, "--value-stdin")
	if err != nil {
		t.Fatalf("variables set --value-stdin: %v", err)
	}
	var sent apiclient.VariableBody
	if uerr := json.Unmarshal([]byte(gotBody), &sent); uerr != nil {
		t.Fatalf("request body not JSON: %v (%s)", uerr, gotBody)
	}
	if sent.Value == nil || *sent.Value != "SECRET-VALUE" {
		t.Errorf("value from stdin not sent: %s", gotBody)
	}
	if strings.Contains(out, "SECRET-VALUE") {
		t.Errorf("output leaked the stdin value: %q", out)
	}
}

func TestSetVariableCommandRequiresKey(t *testing.T) {
	cfg := seedSessionConfig(t, "http://127.0.0.1:0", "tok")
	if _, _, err := run(t, "variables", "set", "--config", cfg); err == nil {
		t.Error("expected an error when the key is missing")
	}
}

// TestSetVariableCommandRequiresValue: `variables set <key>` with no value and no
// --value-stdin must error, not silently overwrite an existing variable's value
// with the empty string. It must never reach the server.
func TestSetVariableCommandRequiresValue(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	cfg := seedSessionConfig(t, srv.URL, "tok")
	if _, _, err := run(t, "variables", "set", "onlykey", "--config", cfg); err == nil {
		t.Error("expected an error when no value is supplied")
	}
	if called {
		t.Error("no request should be sent when the value is missing")
	}
}

// TestSetVariableCommandExplicitEmptyValue: an explicit empty value (`set k ""`)
// is a deliberate write and must be allowed through.
func TestSetVariableCommandExplicitEmptyValue(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"key":"k","value":"","is_encrypted":false}`)
	}))
	defer srv.Close()

	cfg := seedSessionConfig(t, srv.URL, "tok")
	if _, _, err := run(t, "variables", "set", "k", "", "--config", cfg); err != nil {
		t.Fatalf("explicit empty value should be allowed: %v", err)
	}
	var sent apiclient.VariableBody
	if uerr := json.Unmarshal([]byte(gotBody), &sent); uerr != nil {
		t.Fatalf("request body not JSON: %v (%s)", uerr, gotBody)
	}
	if sent.Value == nil || *sent.Value != "" {
		t.Errorf("explicit empty value not sent: %s", gotBody)
	}
}

// TestVariablesGetCommand exercises the full `variables get` path (request +
// single-variable printer).
func TestVariablesGetCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":"region","value":"us-east-1","description":"default region","is_encrypted":false}`)
	}))
	defer srv.Close()

	cfg := seedSessionConfig(t, srv.URL, "tok")
	out, _, err := run(t, "variables", "get", "region", "--config", cfg)
	if err != nil {
		t.Fatalf("variables get: %v", err)
	}
	for _, want := range []string{"region", "us-east-1", "default region"} {
		if !strings.Contains(out, want) {
			t.Errorf("get output missing %q:\n%s", want, out)
		}
	}
}

// TestVariablesListCommand exercises the full `variables list` path (request +
// table printer); the value column is never rendered.
func TestVariablesListCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"variables":[{"key":"region","value":"us-east-1","is_encrypted":false},{"key":"api_token","value":"***","is_encrypted":true}],"total_entries":2}`)
	}))
	defer srv.Close()

	cfg := seedSessionConfig(t, srv.URL, "tok")
	out, _, err := run(t, "variables", "list", "--config", cfg)
	if err != nil {
		t.Fatalf("variables list: %v", err)
	}
	if !strings.Contains(out, "region") || !strings.Contains(out, "api_token") {
		t.Errorf("list output missing a key:\n%s", out)
	}
	if strings.Contains(out, "us-east-1") {
		t.Errorf("list output must not render the value column:\n%s", out)
	}
}

func TestGetVariableReq(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/variables/region" {
			t.Errorf("path = %s, want /api/v2/variables/region", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":"region","value":"us-east-1","is_encrypted":false}`)
	}))
	defer srv.Close()

	v, err := getVariableReq(context.Background(), srv.URL, "t", "region")
	if err != nil {
		t.Fatalf("getVariableReq: %v", err)
	}
	if v.Key != "region" || v.Value != "us-east-1" {
		t.Errorf("variable = %+v", v)
	}
}

func TestDeleteVariableReq(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v2/variables/region" {
			t.Errorf("request = %s %s, want DELETE /api/v2/variables/region", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := deleteVariableReq(context.Background(), srv.URL, "t", "region"); err != nil {
		t.Fatalf("deleteVariableReq: %v", err)
	}
}

func TestDeleteVariableReqNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := deleteVariableReq(context.Background(), srv.URL, "t", "ghost"); err == nil {
		t.Error("expected an error deleting a missing variable")
	}
}

func TestPrintVariableListMasksEncryptedValues(t *testing.T) {
	descA, descB := "region", "secret"
	coll := &apiclient.VariableCollection{Variables: &[]apiclient.Variable{
		{Key: "region", Value: "us-east-1", Description: &descA, IsEncrypted: false},
		{Key: "api_token", Value: "***", Description: &descB, IsEncrypted: true},
	}}
	var buf bytes.Buffer
	if err := printVariableList(&buf, coll); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"KEY", "DESCRIPTION", "ENCRYPTED", "region", "api_token"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q; got:\n%s", want, out)
		}
	}
}

func TestPrintVariableListEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := printVariableList(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No variables") {
		t.Errorf("empty = %q, want the friendly line", buf.String())
	}
}
