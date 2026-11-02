package admissionwebhook

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dexadata/dexaflow/pkg/admission"
)

func sampleRequest() *admission.Request {
	secs := int64(30)
	return &admission.Request{
		TenantID: "acme", DagID: "etl", RunID: "r1", TaskID: "extract", TryNumber: 1,
		Operator: "python", Image: "etl:v1",
		Resources: admission.Resources{Requests: admission.Quantity{CPU: "500m"}},
		Execution: admission.Execution{
			ServiceAccount: "default-sa",
			Tolerations:    []admission.Toleration{{Key: "spot", Operator: "Exists", Effect: "NoExecute", TolerationSeconds: &secs}},
			Labels:         map[string]string{"team": "data"},
		},
	}
}

// endpoint starts a TLS server that answers with answerFor(request) and
// returns a Hook trusting it.
func endpoint(t *testing.T, cfg Config, answerFor func(w http.ResponseWriter, r *http.Request, req map[string]any)) *Hook {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		answerFor(w, r, body)
	}))
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.URL = srv.URL
	cfg.CAFile = ca
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func uidOf(req map[string]any) string {
	return req["request"].(map[string]any)["uid"].(string)
}

func reply(w http.ResponseWriter, uid, rest string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"api_version":%q,"kind":%q,"response":{"uid":%q%s}`, APIVersion, Kind, uid, rest)
}

func TestWebhookSendsTheAttemptAndAdmitsWithChanges(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	var auth string
	h := endpoint(t, Config{TokenFile: tokenFile}, func(w http.ResponseWriter, r *http.Request, req map[string]any) {
		got, auth = req, r.Header.Get("Authorization")
		reply(w, uidOf(req), `,"decision":"admit","changes":{"node_selector":{"pool":"batch"},"tolerations":[{"key":"batch","operator":"Exists"}],"labels":{"cost":"42"}}}`)
	})
	dec, err := h.Admit(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer s3cret" {
		t.Errorf("Authorization = %q", auth)
	}
	if got["api_version"] != APIVersion || got["kind"] != Kind {
		t.Errorf("envelope = %v", got)
	}
	req := got["request"].(map[string]any)
	if req["tenant_id"] != "acme" || req["task_id"] != "extract" || req["resources"].(map[string]any)["requests"].(map[string]any)["cpu"] != "500m" {
		t.Errorf("request = %v", req)
	}
	tol := req["execution"].(map[string]any)["tolerations"].([]any)[0].(map[string]any)
	if tol["toleration_seconds"] != float64(30) || tol["effect"] != "NoExecute" {
		t.Errorf("toleration = %v", tol)
	}
	for _, k := range []string{"env", "source", "token", "secrets"} {
		if _, ok := req[k]; ok {
			t.Errorf("request carries %q", k)
		}
	}
	ch := dec.Changes()
	if ch.NodeSelector["pool"] != "batch" || len(ch.Tolerations) != 1 || ch.Tolerations[0].Operator != "Exists" || ch.Labels["cost"] != "42" {
		t.Errorf("changes = %+v", ch)
	}
}

func TestWebhookHoldAndRefuse(t *testing.T) {
	for decision, check := range map[string]func(admission.Decision) bool{
		"hold":   func(d admission.Decision) bool { r, ok := d.Held(); return ok && r == "freeze" },
		"refuse": func(d admission.Decision) bool { r, ok := d.Refused(); return ok && r == "freeze" },
		"admit": func(d admission.Decision) bool {
			_, h := d.Held()
			_, r := d.Refused()
			return !h && !r && d.Changes().IsZero()
		},
	} {
		h := endpoint(t, Config{}, func(w http.ResponseWriter, _ *http.Request, req map[string]any) {
			reason := `,"reason":"freeze"`
			if decision == "admit" {
				reason = ""
			}
			reply(w, uidOf(req), fmt.Sprintf(`,"decision":%q%s}`, decision, reason))
		})
		dec, err := h.Admit(context.Background(), sampleRequest())
		if err != nil || !check(dec) {
			t.Errorf("%s: %+v, %v", decision, dec, err)
		}
	}
}

func TestWebhookRejectsBadAnswers(t *testing.T) {
	for name, answer := range map[string]func(w http.ResponseWriter, req map[string]any){
		"status":    func(w http.ResponseWriter, _ map[string]any) { http.Error(w, "boom", http.StatusInternalServerError) },
		"wrong uid": func(w http.ResponseWriter, _ map[string]any) { reply(w, "other", `,"decision":"admit"}`) },
		"unknown field": func(w http.ResponseWriter, req map[string]any) {
			reply(w, uidOf(req), `,"decision":"admit","extra":1}`)
		},
		"unknown change": func(w http.ResponseWriter, req map[string]any) {
			reply(w, uidOf(req), `,"decision":"admit","changes":{"image":"x"}}`)
		},
		"unknown verdict": func(w http.ResponseWriter, req map[string]any) { reply(w, uidOf(req), `,"decision":"maybe"}`) },
		"trailing data": func(w http.ResponseWriter, req map[string]any) {
			reply(w, uidOf(req), `,"decision":"admit"}`)
			fmt.Fprint(w, `{}`)
		},
		"wrong version": func(w http.ResponseWriter, req map[string]any) {
			fmt.Fprintf(w, `{"api_version":"v0","kind":%q,"response":{"uid":%q,"decision":"admit"}}`, Kind, uidOf(req))
		},
		"no response": func(w http.ResponseWriter, _ map[string]any) {
			fmt.Fprintf(w, `{"api_version":%q,"kind":%q}`, APIVersion, Kind)
		},
		"refuse with changes": func(w http.ResponseWriter, req map[string]any) {
			reply(w, uidOf(req), `,"decision":"refuse","changes":{}}`)
		},
		"service account": func(w http.ResponseWriter, req map[string]any) {
			reply(w, uidOf(req), `,"decision":"admit","changes":{"service_account":"admin"}}`)
		},
		"too large": func(w http.ResponseWriter, req map[string]any) {
			reply(w, uidOf(req), `,"decision":"refuse","reason":"`+strings.Repeat("x", MaxResponseBytes)+`"}`)
		},
		"admit with a reason": func(w http.ResponseWriter, req map[string]any) {
			reply(w, uidOf(req), `,"decision":"admit","reason":"x"}`)
		},
	} {
		h := endpoint(t, Config{}, func(w http.ResponseWriter, _ *http.Request, req map[string]any) { answer(w, req) })
		if dec, err := h.Admit(context.Background(), sampleRequest()); err == nil {
			t.Errorf("%s: want an error, got %+v", name, dec)
		}
	}
}

func TestWebhookServiceAccountWithTheAllowlist(t *testing.T) {
	h := endpoint(t, Config{AllowServiceAccount: true}, func(w http.ResponseWriter, _ *http.Request, req map[string]any) {
		reply(w, uidOf(req), `,"decision":"admit","changes":{"service_account":"etl"}}`)
	})
	dec, err := h.Admit(context.Background(), sampleRequest())
	if err != nil || dec.Changes().ServiceAccount != "etl" {
		t.Fatalf("%+v, %v", dec, err)
	}
}

func TestWebhookUntrustedCertificateFails(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	h, err := New(Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Admit(context.Background(), sampleRequest()); err == nil {
		t.Error("a certificate outside the system roots and no ca_file should fail")
	}
}

func TestValidateURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://admission.policy.svc:8443/admit": true,
		"http://127.0.0.1:9000/admit":             true,
		"http://localhost/admit":                  true,
		"http://[::1]/admit":                      true,
		"http://admission.policy.svc/admit":       false,
		"https://user:pw@admission/admit":         false,
		"https://:443/admit":                      false,
		"/admit":                                  false,
		"ftp://admission/admit":                   false,
	} {
		if err := ValidateURL(raw); (err == nil) != ok {
			t.Errorf("ValidateURL(%q) = %v, want ok=%v", raw, err, ok)
		}
	}
}

func TestNewRefusesUnreadableFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Config{
		"ca file":     {URL: "https://a/", CAFile: missing},
		"ca not pem":  {URL: "https://a/", CAFile: empty},
		"token file":  {URL: "https://a/", TokenFile: missing},
		"empty token": {URL: "https://a/", TokenFile: empty},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// A redirect is an error, and its target is never contacted, so the bearer
// token never leaves for another host.
func TestWebhookFollowsNoRedirect(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := endpoint(t, Config{TokenFile: tokenFile}, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		http.Redirect(w, r, target.URL+"/admit", http.StatusTemporaryRedirect)
	})
	if _, err := h.Admit(context.Background(), sampleRequest()); err == nil {
		t.Fatal("a redirect should be an error")
	}
	if hits.Load() != 0 {
		t.Errorf("the redirect target was contacted %d times", hits.Load())
	}
}

// A failed call's error can reach a task's failure, which tenants read: it
// names neither the endpoint nor the token's path.
func TestWebhookErrorsDoNotNameTheEndpoint(t *testing.T) {
	h, err := New(Config{URL: "https://127.0.0.1:1/admit?key=secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.Admit(context.Background(), sampleRequest())
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "secret") {
		t.Errorf("error = %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if werr := os.WriteFile(tokenFile, []byte("t"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	h, err = New(Config{URL: "https://127.0.0.1:1/admit", TokenFile: tokenFile})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tokenFile); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Admit(context.Background(), sampleRequest()); err == nil || strings.Contains(err.Error(), tokenFile) {
		t.Errorf("error = %v", err)
	}
}
