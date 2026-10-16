package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestImplicitServerOnLiteIsTheLitePort pins #1912: with no server set
// anywhere, a machine set up for Lite calls the Lite port from the config file
// (8088 when the file names none), not the built-in localhost:8080. A server
// that is set anywhere still wins, and a machine without Lite keeps 8080.
func TestImplicitServerOnLiteIsTheLitePort(t *testing.T) {
	cases := []struct {
		name   string
		config string
		env    map[string]string
		want   string
	}{
		{name: "lite_port from setup", config: "lite_executor: subprocess\nlite_port: 8099\n", want: "http://localhost:8099"},
		{name: "Lite with no lite_port", config: "lite_executor: k8s\n", want: "http://localhost:8088"},
		{name: "no Lite", config: "admin_email: a@b.c\n", want: "http://localhost:8080"},
		{name: "server_url beats Lite", config: "lite_executor: subprocess\nlite_port: 8099\nserver_url: https://pro.example\n", want: "https://pro.example"},
		{name: "DEXAFLOW_SERVER_URL beats Lite", config: "lite_executor: subprocess\nlite_port: 8099\n",
			env: map[string]string{"DEXAFLOW_SERVER_URL": "https://env.example"}, want: "https://env.example"},
		{name: "LEOFLOW_SERVER_URL beats Lite", config: "lite_executor: subprocess\nlite_port: 8099\n",
			env: map[string]string{"LEOFLOW_SERVER_URL": "https://legacy-env.example"}, want: "https://legacy-env.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLIEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cfg := filepath.Join(t.TempDir(), "config.yaml")
			writeTree(t, cfg, tc.config)
			server, _, err := resolveVia(t, "dags", "list", "--config", cfg)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if server != tc.want {
				t.Errorf("server = %q, want %q", server, tc.want)
			}
		})
	}
}

// TestImplicitLiteServerFromEitherHome: the Lite port is read from the
// default config file, under ~/.dexaflow or an install's ~/.leoflow from
// before the rename.
func TestImplicitLiteServerFromEitherHome(t *testing.T) {
	for _, dir := range []string{".dexaflow", ".leoflow"} {
		t.Run(dir, func(t *testing.T) {
			isolateCLIEnv(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			writeTree(t, filepath.Join(home, dir, "config.yaml"), "lite_executor: subprocess\nlite_port: 8097\n")
			server, _, err := resolveVia(t, "dags", "list")
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if server != "http://localhost:8097" {
				t.Errorf("server = %q, want the Lite port from ~/%s/config.yaml", server, dir)
			}
		})
	}
}

// TestDagsListReachesLiteOnItsPort drives `dexaflow dags list` with nothing
// but a Lite config: it must call the server listening on lite_port.
func TestDagsListReachesLiteOnItsPort(t *testing.T) {
	isolateCLIEnv(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/dags" {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dags":[{"dag_id":"lite_dag"}],"total_entries":1}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("HOME")
	writeTree(t, filepath.Join(home, ".dexaflow", "config.yaml"), "lite_executor: subprocess\nlite_port: "+u.Port()+"\n")
	out, _, err := run(t, "dags", "list")
	if err != nil {
		t.Fatalf("dags list: %v", err)
	}
	if hits.Load() != 1 || !strings.Contains(out, "lite_dag") {
		t.Errorf("hits = %d, output %q, want the DAGs of the Lite server on its port", hits.Load(), out)
	}
}
