package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The remote builder, task service account and warm pools a cluster
// advertises (#1890).

// clusterRemoteBuilder is a deployDefaults answer that advertises a Cloud
// Build remote builder on project, and nothing else.
func clusterRemoteBuilder(project string) string {
	return `{"registry":"","platforms":[],"base_image":"","source_mode_image":"",` +
		`"remote_builder":{"cloud_build":{"project":"` + project + `","region":"us-central1"}},"task_service_account":"","warm_pools":null}`
}

// TestDeployBuildsOnTheClusterRemoteBuilder: a project that needs a real
// build and names no remote builder, outside any workspace, is built on the
// one the cluster advertises, and the builder line says it came from there.
func TestDeployBuildsOnTheClusterRemoteBuilder(t *testing.T) {
	argvLog, _ := fakeGcloudOnPath(t)
	dir, host, _ := splitProject(t, "")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	cloudBuildPushes(t, host+"/team/etl:v1")
	daemon, daemonLog := argvBuilder(t)
	fake := &clusterServer{deployServer: &deployServer{}, body: clusterRemoteBuilder("acme-cluster")}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", daemon, "--dag-version", "v1")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	want := "so the remote builder from the deploy defaults of " + srv.URL + " builds the whole image"
	if !strings.Contains(out, want) {
		t.Errorf("output does not say %q:\n%s", want, out)
	}
	if calls := argvLines(t, daemonLog); calls != nil {
		t.Errorf("the daemon ran: %q", calls)
	}
	argv := argvLines(t, argvLog)
	if len(argv) != 1 || !strings.Contains(argv[0], "--project=acme-cluster") || !strings.Contains(argv[0], "--region=us-central1") {
		t.Fatalf("gcloud calls = %q, want one builds submit on acme-cluster in us-central1", argv)
	}
}

// TestProjectRemoteBuilderWinsOverTheCluster: the project's own
// build.remote_builder is used, not the cluster's.
func TestProjectRemoteBuilderWinsOverTheCluster(t *testing.T) {
	argvLog, _ := fakeGcloudOnPath(t)
	dir, host, _ := splitProject(t, cloudBuildYAML)
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	cloudBuildPushes(t, host+"/team/etl:v1")
	daemon, _ := argvBuilder(t)
	fake := &clusterServer{deployServer: &deployServer{}, body: clusterRemoteBuilder("acme-cluster")}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", daemon, "--dag-version", "v1")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "the remote builder from "+projectConfigPath(dir)) {
		t.Errorf("builder line does not name the project's file:\n%s", out)
	}
	if argv := argvLines(t, argvLog); len(argv) != 1 || !strings.Contains(argv[0], "--project=acme-ci") {
		t.Fatalf("gcloud calls = %q, want the project's acme-ci", argv)
	}
}

// TestWorkspaceRemoteBuilderWinsOverTheCluster: a workspace's remote_builder
// beats the cluster's, which applies only when the workspace names none.
func TestWorkspaceRemoteBuilderWinsOverTheCluster(t *testing.T) {
	for _, tc := range []struct {
		name, workspaceRB, wantProject, wantFrom string
	}{
		{name: "workspace sets one", workspaceRB: "  remote_builder:\n    cloud_build: {project: acme-shared}\n", wantProject: "acme-shared", wantFrom: "defaults in "},
		{name: "workspace sets none", wantProject: "acme-cluster", wantFrom: "the deploy defaults of "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLIEnv(t)
			root, project := defaultsWorkspace(t, "dag_id: orders\n")
			ws := readProjectFile(t, root, workspaceFileName)
			writeTree(t, filepath.Join(root, workspaceFileName), ws+tc.workspaceRB)
			srv := httptest.NewServer(&clusterServer{deployServer: &deployServer{}, body: clusterRemoteBuilder("acme-cluster")})
			defer srv.Close()
			t.Chdir(root)
			cmd, rest, err := NewRootCommand().Find([]string{"deploy", "--config", emptyConfig(t), "--target", "bare"})
			if err != nil {
				t.Fatal(err)
			}
			if perr := cmd.ParseFlags(rest); perr != nil {
				t.Fatal(perr)
			}
			cluster, err := clusterLayer(context.Background(), srv.URL, "tok")
			if err != nil || cluster == nil {
				t.Fatalf("clusterLayer = %v, %v; want the advertised remote builder", cluster, err)
			}
			cfg, err := loadProjectConfig(project)
			if err != nil {
				t.Fatal(err)
			}
			prov, err := applyDeployDefaults(cmd, project, cfg, cluster)
			if err != nil {
				t.Fatalf("applyDeployDefaults: %v", err)
			}
			if rb := cfg.Build.RemoteBuilder; rb == nil || rb.CloudBuild == nil || rb.CloudBuild.Project != tc.wantProject {
				t.Fatalf("remote_builder = %+v, want project %s", rb, tc.wantProject)
			}
			if !strings.Contains(prov.remoteSource, tc.wantFrom) {
				t.Errorf("remote builder source = %q, want %q in it", prov.remoteSource, tc.wantFrom)
			}
		})
	}
}

// clusterExecution is a deployDefaults answer with a registry, so a deploy
// resolves, plus the given task service account and warm_pools JSON.
func clusterExecution(registry, serviceAccount, warmPools string) string {
	return `{"registry":"` + registry + `","platforms":[],"base_image":"","source_mode_image":"",` +
		`"remote_builder":null,"task_service_account":"` + serviceAccount + `","warm_pools":` + warmPools + `}`
}

// TestDeployReportsTheClusterTaskServiceAccountAndWarmPools: the deploy says
// which account tasks run as when they name none, and how many idle warm
// workers the DAG gets and where that number came from, as the server
// resolves it (EffectiveMinIdle). Nothing of it reaches dag.json.
func TestDeployReportsTheClusterTaskServiceAccountAndWarmPools(t *testing.T) {
	for _, tc := range []struct {
		name, project, warm string
		want                func(url, file string) string
	}{
		{name: "cluster floor", warm: `{"enabled":true,"min_idle_workers":1,"max_pool_size":8}`,
			want: func(url, _ string) string {
				return "warm pool:  on, 1 idle worker kept for the current version (min_idle_workers from the deploy defaults of " + url + ")"
			}},
		{name: "project value", project: "min_idle_workers: 3\n", warm: `{"enabled":true,"min_idle_workers":1,"max_pool_size":8}`,
			want: func(_, file string) string {
				return "warm pool:  on, 3 idle workers kept for the current version (min_idle_workers from " + file + ")"
			}},
		{name: "capped", project: "min_idle_workers: 20\n", warm: `{"enabled":true,"min_idle_workers":0,"max_pool_size":8}`,
			want: func(url, file string) string {
				return "warm pool:  on, 8 idle workers kept for the current version (min_idle_workers from " + file +
					", capped at max_pool_size 8 of the deploy defaults of " + url + ")"
			}},
		{name: "scale to zero", warm: `{"enabled":true,"min_idle_workers":0,"max_pool_size":8}`,
			want: func(url, _ string) string {
				return "warm pool:  on, 0 idle workers kept for the current version (min_idle_workers from the deploy defaults of " + url + ")"
			}},
		{name: "off", warm: `{"enabled":false,"min_idle_workers":1,"max_pool_size":8}`,
			want: func(url, _ string) string {
				return "warm pool:  off (from the deploy defaults of " + url + "): every task runs on a dedicated pod"
			}},
		{name: "off, project asks", project: "min_idle_workers: 2\n", warm: `{"enabled":false,"min_idle_workers":0,"max_pool_size":8}`,
			want: func(url, file string) string {
				return "warm pool:  off (from the deploy defaults of " + url + "): every task runs on a dedicated pod; min_idle_workers: 2 in " + file + " keeps no worker warm"
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, stub, _ := clusterProject(t, tc.project)
			fake := &clusterServer{deployServer: &deployServer{status: http.StatusNotFound}, body: clusterExecution("ghcr.io/acme", "dexaflow-task", tc.warm)}
			srv := httptest.NewServer(fake)
			defer srv.Close()
			out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "tok", "--builder", stub, "--plan", "--config", emptyConfig(t))
			if err != nil {
				t.Fatalf("deploy --plan: %v\n%s", err, out)
			}
			sa := "task SA:    dexaflow-task (from the deploy defaults of " + srv.URL + "), for tasks that set no execution.service_account"
			if line := lineWith(out, "task SA:"); line != sa {
				t.Errorf("task SA line = %q, want %q:\n%s", line, sa, out)
			}
			if want, line := tc.want(srv.URL, projectConfigPath(dir)), lineWith(out, "warm pool:"); line != want {
				t.Errorf("warm pool line = %q, want %q:\n%s", line, want, out)
			}
		})
	}
}

// TestDeployReportsNothingTheClusterDoesNotAdvertise: a server that
// predates the fields, or advertises none, adds no task SA or warm pool
// line, as before.
func TestDeployReportsNothingTheClusterDoesNotAdvertise(t *testing.T) {
	for name, body := range map[string]string{
		"older server":   `{"registry":"ghcr.io/acme","platforms":[],"base_image":""}`,
		"none announced": clusterExecution("ghcr.io/acme", "", "null"),
	} {
		t.Run(name, func(t *testing.T) {
			dir, stub, _ := clusterProject(t, "min_idle_workers: 2\n")
			srv := httptest.NewServer(&clusterServer{deployServer: &deployServer{status: http.StatusNotFound}, body: body})
			defer srv.Close()
			out, errOut, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "tok", "--builder", stub, "--plan", "--config", emptyConfig(t))
			if err != nil {
				t.Fatalf("deploy --plan: %v\n%s", err, out)
			}
			if strings.Contains(out, "task SA:") || strings.Contains(out, "warm pool:") || strings.Contains(errOut, "warning") {
				t.Errorf("unexpected report:\nstdout:\n%s\nstderr:\n%s", out, errOut)
			}
		})
	}
}

// TestUnusableClusterExecutionDefaultsAreIgnored: a task service account
// that is no ServiceAccount name (such as one carrying control
// characters), a negative warm pool count or an unusable remote builder
// makes the whole answer unusable, with the usual warning.
func TestUnusableClusterExecutionDefaultsAreIgnored(t *testing.T) {
	for name, body := range map[string]string{
		"service account": clusterExecution("ghcr.io/acme", `bad\u001b[2J`, "null"),
		"warm pools":      clusterExecution("ghcr.io/acme", "", `{"enabled":true,"min_idle_workers":-1,"max_pool_size":8}`),
		"remote builder":  `{"registry":"ghcr.io/acme","platforms":[],"base_image":"","remote_builder":{"cloud_build":{"project":"X"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir, stub, _ := clusterProject(t, "registry: {url: quay.io/me}\n")
			srv := httptest.NewServer(&clusterServer{deployServer: &deployServer{status: http.StatusNotFound}, body: body})
			defer srv.Close()
			out, errOut, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "tok", "--builder", stub, "--plan", "--config", emptyConfig(t))
			if err != nil {
				t.Fatalf("deploy --plan: %v\n%s", err, out)
			}
			if !strings.Contains(errOut, "warning: ignoring the deploy defaults of "+srv.URL) {
				t.Errorf("stderr does not warn:\n%s", errOut)
			}
			if strings.Contains(out, "task SA:") || strings.Contains(out, "warm pool:") || strings.Contains(out, "(from the deploy defaults of") {
				t.Errorf("an unusable answer was applied:\n%s", out)
			}
		})
	}
}
