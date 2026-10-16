package cli

import (
	"strings"
	"testing"
)

// taskPodsJSON is a task_pods object of a dry run answer.
func taskPodsJSON(nonRoot bool, accounts string) string {
	r := "false"
	if nonRoot {
		r = "true"
	}
	return `{"run_as_non_root":` + r + `,"service_accounts":[` + accounts + `]}`
}

// deployWithTaskPods deploys a fixture whose registry holds an image, with
// dockerfile as the project's own Dockerfile ("" for the generated one),
// against a server whose dry run answers taskPods ("" for none).
func deployWithTaskPods(t *testing.T, dockerfile, taskPods string, args ...string) (fake *deployServer, built bool, out, errOut string, err error) {
	t.Helper()
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, builder, log := deployFixtureAt(t, reg.host+"/team")
	if dockerfile != "" {
		writeProjectFile(t, dir, "Dockerfile", dockerfile)
	}
	fake = &deployServer{taskPods: taskPods}
	// --force builds, though the registry holds an image under every tag.
	out, errOut, err = deployDir(t, fake, dir, builder, append([]string{"--force"}, args...)...)
	return fake, builderRan(log), out, errOut, err
}

// TestDeployStopsOnAMissingTaskServiceAccount: a ServiceAccount the dry run
// reports missing stops the deploy before the build, naming it, its tasks
// and how to fix it (#1889).
func TestDeployStopsOnAMissingTaskServiceAccount(t *testing.T) {
	fake, built, out, _, err := deployWithTaskPods(t, "", taskPodsJSON(true,
		`{"name":"etl-sa","tasks":["extract","load"],"state":"missing"}`))
	if err == nil {
		t.Fatalf("deploy succeeded:\n%s", out)
	}
	for _, want := range []string{`ServiceAccount "etl-sa"`, "extract, load", "does not exist", "execution.service_account", "taskServiceAccount.create"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not say %q:\n%v", want, err)
		}
	}
	if built || fake.registers != 0 {
		t.Errorf("built=%v registers=%d, want neither", built, fake.registers)
	}
}

// TestDeployNotesAMissingDefaultServiceAccount: the namespace's default
// ServiceAccount, which Kubernetes creates in every namespace a moment
// after the namespace itself, is a note when the dry run reports it
// missing, not a stop; a missing named account next to it still stops.
func TestDeployNotesAMissingDefaultServiceAccount(t *testing.T) {
	fake, built, out, errOut, err := deployWithTaskPods(t, "", taskPodsJSON(true,
		`{"name":"default","tasks":["after","hold"],"state":"missing"}`))
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !built || fake.registers != 1 {
		t.Errorf("built=%v registers=%d, want a build and a register", built, fake.registers)
	}
	if want := `note: the namespace's default ServiceAccount "default" (tasks after, hold) was not found`; !strings.Contains(errOut, want) {
		t.Errorf("stderr does not say %q:\n%s", want, errOut)
	}

	_, _, out, _, err = deployWithTaskPods(t, "", taskPodsJSON(true,
		`{"name":"default","tasks":["a"],"state":"missing"},{"name":"etl-sa","tasks":["b"],"state":"missing"}`))
	if err == nil || !strings.Contains(err.Error(), `"etl-sa" (tasks b)`) || strings.Contains(err.Error(), `"default"`) {
		t.Errorf("deploy = %v, want a stop naming etl-sa only\n%s", err, out)
	}
}

// TestDeployStopsOnAnInvalidTaskServiceAccountName: a ServiceAccount the
// dry run reports missing with a detail, such as a name that is not valid,
// stops the deploy with that detail and the fix.
func TestDeployStopsOnAnInvalidTaskServiceAccountName(t *testing.T) {
	_, built, out, _, err := deployWithTaskPods(t, "", taskPodsJSON(true,
		`{"name":"Bad_Name","tasks":["extract"],"state":"missing","detail":"not a valid \u001b[31mServiceAccount name"}`))
	if err == nil {
		t.Fatalf("deploy succeeded:\n%s", out)
	}
	for _, want := range []string{`ServiceAccount "Bad_Name" (tasks extract): not a valid [31mServiceAccount name`, "execution.service_account"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not say %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "\u001b") {
		t.Errorf("error carries a control character the server sent:\n%q", err)
	}
	if built {
		t.Error("built, want a stop before the build")
	}
}

// TestDeployReportsTaskServiceAccounts: a ServiceAccount with pull secrets
// is a preflight line, one without any is a note naming both ways a
// private registry is pulled from, and one the server could not look up is
// a note with its reason; the deploy goes on.
func TestDeployReportsTaskServiceAccounts(t *testing.T) {
	fake, built, out, errOut, err := deployWithTaskPods(t, "", taskPodsJSON(true,
		`{"name":"dexaflow-task","tasks":["a"],"state":"found","image_pull_secrets":2},`+
			`{"name":"default","tasks":["b"],"state":"found","image_pull_secrets":0},`+
			`{"name":"other","tasks":["c"],"state":"unchecked","detail":"no reader \u001b[31mhere"}`))
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !built || fake.registers != 1 {
		t.Errorf("built=%v registers=%d, want a build and a register", built, fake.registers)
	}
	if want := `preflight:  task ServiceAccount "dexaflow-task" exists, with 2 imagePullSecrets (tasks a)`; !strings.Contains(out, want) {
		t.Errorf("output does not say %q:\n%s", want, out)
	}
	for _, want := range []string{
		`note: task ServiceAccount "default" (tasks b) carries no imagePullSecrets`, "taskServiceAccount.imagePullSecrets", "node",
		`note: could not check task ServiceAccount "other" (tasks c): no reader [31mhere`,
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not say %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(errOut, "\u001b") {
		t.Errorf("stderr carries a control character the server sent:\n%q", errOut)
	}
}

// TestDeployChecksTheDockerfileUser: a project Dockerfile whose final USER
// is root or a name stops the deploy before the build when the server runs
// task pods as non-root, and names the fix; one the server would run is a
// preflight line; a Dockerfile with no USER is a note; a server that does
// not run pods as non-root takes it; an older server (no task_pods, or no
// dry run at all) gets a note and the deploy goes on (#1889).
func TestDeployChecksTheDockerfileUser(t *testing.T) {
	enforced, relaxed := taskPodsJSON(true, ""), taskPodsJSON(false, "")
	for _, tc := range []struct {
		name, dockerfile, taskPods string
		noDryRun                   bool
		stops                      bool
		want, wantErr              []string
		quiet                      bool
	}{
		{name: "root", dockerfile: "FROM python:3.12\nUSER root\n", taskPods: enforced, stops: true,
			want: []string{"Dockerfile", "USER root", "line 2", "runs as root", "USER 65532:65532", "taskPodSecurity.runAsNonRoot"}},
		{name: "named user", dockerfile: "FROM python:3.12\nUSER app\n", taskPods: enforced, stops: true,
			want: []string{"USER app", "line 2", "a name", "numeric", "USER 65532:65532"}},
		{name: "numeric", dockerfile: "FROM python:3.12\nUSER 65532:65532\n", taskPods: enforced,
			want: []string{"preflight:  Dockerfile runs as USER 65532:65532"}},
		{name: "no USER", dockerfile: "FROM python:3.12\n", taskPods: enforced,
			wantErr: []string{"note: Dockerfile sets no USER in its final stage", "the base image", "USER 65532:65532"}},
		{name: "variable", dockerfile: "FROM python:3.12\nUSER $UID\n", taskPods: enforced,
			wantErr: []string{"note: Dockerfile runs as USER $UID", "line 2"}},
		{name: "server runs root", dockerfile: "FROM python:3.12\nUSER root\n", taskPods: relaxed, quiet: true},
		{name: "older server", dockerfile: "FROM python:3.12\nUSER root\n",
			wantErr: []string{"note: Dockerfile runs as root (USER root, line 2)", "if the server runs task pods as non-root"}},
		{name: "no dry run", dockerfile: "FROM python:3.12\nUSER app\n", noDryRun: true,
			wantErr: []string{"note: Dockerfile runs as a name (USER app, line 2)"}},
		{name: "generated Dockerfile", taskPods: enforced, quiet: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noDockerCredentials(t)
			reg := newFakeOCIRegistry(t, singleManifest)
			dir, builder, log := deployFixtureAt(t, reg.host+"/team")
			if tc.dockerfile != "" {
				writeProjectFile(t, dir, "Dockerfile", tc.dockerfile)
			}
			fake := &deployServer{taskPods: tc.taskPods, noDryRun: tc.noDryRun}
			out, errOut, err := deployDir(t, fake, dir, builder, "--force")
			if tc.stops {
				if err == nil {
					t.Fatalf("deploy succeeded:\n%s", out)
				}
				for _, w := range tc.want {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error does not say %q:\n%v", w, err)
					}
				}
				if builderRan(log) || fake.registers != 0 {
					t.Errorf("built=%v registers=%d, want neither", builderRan(log), fake.registers)
				}
				return
			}
			if err != nil {
				t.Fatalf("deploy: %v\n%s\n%s", err, out, errOut)
			}
			if !builderRan(log) || fake.registers != 1 {
				t.Errorf("built=%v registers=%d, want a build and a register", builderRan(log), fake.registers)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output does not say %q:\n%s", w, out)
				}
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(errOut, w) {
					t.Errorf("stderr does not say %q:\n%s", w, errOut)
				}
			}
			if tc.quiet && (strings.Contains(out+errOut, "USER") || strings.Contains(out+errOut, "Dockerfile runs")) {
				t.Errorf("deploy speaks of the USER:\n%s\n%s", out, errOut)
			}
		})
	}
}

// TestDeploySkipBuildChecksNoDockerfileUser: --skip-build builds nothing,
// so the project's Dockerfile says nothing about the image registered.
func TestDeploySkipBuildChecksNoDockerfileUser(t *testing.T) {
	_, _, out, errOut, err := deployWithTaskPods(t, "FROM python:3.12\nUSER root\n", taskPodsJSON(true, ""), "--skip-build", "--dag-version", "v9")
	if err != nil {
		t.Fatalf("deploy --skip-build: %v\n%s", err, out)
	}
	if strings.Contains(out+errOut, "USER root") {
		t.Errorf("deploy --skip-build checks the Dockerfile:\n%s\n%s", out, errOut)
	}
}
