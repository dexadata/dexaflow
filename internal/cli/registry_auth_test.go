package cli

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dexadata/dexaflow/internal/build"
)

// fakeCloudSession stands in for the cloud CLIs (cliDeps.registryCloud): it holds
// user:pass for one registry host and counts the hosts it was asked for.
type fakeCloudSession struct {
	host, user, pass string
	mu               sync.Mutex
	asked            int
}

func (f *fakeCloudSession) Owns(host string) bool { return host == f.host }

func (f *fakeCloudSession) Credential(_ context.Context, host string) (build.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	if host != f.host {
		return build.Credential{}, nil
	}
	return build.Credential{Username: f.user, Password: f.pass, Source: "the fake cloud session"}, nil
}

func (f *fakeCloudSession) askedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked
}

// withCloudSession makes the registry client of the commands run under d
// read the cloud session from a fake holding user:pass for host.
func withCloudSession(t *testing.T, d *cliDeps, host, user, pass string) *fakeCloudSession {
	t.Helper()
	f := &fakeCloudSession{host: host, user: user, pass: pass}
	d.registryCloud = f
	return f
}

// cloudRegistryProject is a deploy fixture against a fake registry that
// opens only to the cloud session's credential, with extra registry keys.
func cloudRegistryProject(t *testing.T, d *cliDeps, registryKeys string) (reg *fakeOCIRegistry, dir, builder string, cloud *fakeCloudSession) {
	t.Helper()
	noDockerCredentials(t)
	reg = newFakeOCIRegistry(t, singleManifest)
	reg.basicAuth = "oauth2accesstoken:tok-1"
	cloud = withCloudSession(t, d, reg.host, "oauth2accesstoken", "tok-1")
	dir, builder, _ = deployFixtureAt(t, reg.host+"/team")
	if registryKeys != "" {
		writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: etl\nregistry:\n  url: "+reg.host+"/team\n  image_name: etl\n"+registryKeys)
	}
	return reg, dir, builder, cloud
}

// TestDeployReadsTheCloudSession (#1704, #1705): with nothing in the docker
// config, a deploy reaches a registry with the session of the cloud CLI
// that owns it, with no docker login: the preflight says where the
// credential came from, and the digest is read with it.
func TestDeployReadsTheCloudSession(t *testing.T) {
	d := testDeps()
	reg, dir, builder, _ := cloudRegistryProject(t, d, "")
	fake := &deployServer{}
	out, errOut, err := deployDirDeps(t, d, fake, dir, builder, "--force")
	if err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, errOut)
	}
	want := "preflight:  " + reg.host + "/team/etl accepts a push (credential from the fake cloud session; " +
		"a local-daemon build pushes with builder.sh's own login: builder.sh login " + reg.host + ")"
	if !strings.Contains(out, want) || fake.registers != 1 {
		t.Errorf("registers=%d, output does not say %q:\n%s", fake.registers, want, out)
	}
}

// TestDeployAuthMethodDockerConfigIgnoresTheCloud: registry.auth_method
// docker_config reads the docker config only, as before auto: the cloud is
// never asked, and the preflight names the login command.
func TestDeployAuthMethodDockerConfigIgnoresTheCloud(t *testing.T) {
	d := testDeps()
	reg, dir, _, cloud := cloudRegistryProject(t, d, "  auth_method: docker_config\n")
	builder, _ := argvBuilder(t) // named docker
	_, errOut, err := deployDirDeps(t, d, &deployServer{}, dir, builder, "--force")
	if err == nil {
		t.Fatal("deploy reached a registry it holds no credential for")
	}
	if !strings.Contains(errOut, "dexaflow registry login "+reg.host) || cloud.askedCount() != 0 {
		t.Errorf("cloud asked %d times; stderr does not name dexaflow registry login %s:\n%s", cloud.askedCount(), reg.host, errOut)
	}
}

// TestDeployWarnsOfADeprecatedAuthMethod: the values the schema accepted
// before any was read still load, with a note, and read as auto.
func TestDeployWarnsOfADeprecatedAuthMethod(t *testing.T) {
	d := testDeps()
	_, dir, builder, cloud := cloudRegistryProject(t, d, "  auth_method: iam\n")
	out, errOut, err := deployDirDeps(t, d, &deployServer{}, dir, builder, "--force")
	if err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(errOut, "registry.auth_method: iam is deprecated") || cloud.askedCount() == 0 {
		t.Errorf("cloud asked %d times; stderr does not warn of iam:\n%s", cloud.askedCount(), errOut)
	}
}

// TestDoctorReadsTheCloudSession: doctor's push check uses the credentials
// a deploy would, the cloud session included.
func TestDoctorReadsTheCloudSession(t *testing.T) {
	d := testDeps()
	isolateCLIEnv(t)
	reg, dir, _, _ := cloudRegistryProject(t, d, "")
	t.Chdir(dir)
	srv := httptest.NewServer(&deployServer{})
	t.Cleanup(srv.Close)
	out := doctorAgainstDeps(t, d, srv.URL, "tok")
	want := "registry      " + reg.host + "/team/etl accepts a push (credential from the fake cloud session; " +
		"a local-daemon build pushes with docker's own login: dexaflow registry login " + reg.host + ")"
	if !strings.Contains(out, want) {
		t.Errorf("doctor does not say %q:\n%s", want, out)
	}
}

// The refusal of a push names the command of the registry's cloud, and,
// when the cloud CLI could not give a credential, why.
func TestPushRefusalNamesTheCloudCommand(t *testing.T) {
	t.Parallel()
	ecr := "123456789012.dkr.ecr.eu-west-1.amazonaws.com"
	denied := build.ErrPushDenied
	for _, tc := range []struct {
		name    string
		access  build.PushAccess
		builder string
		method  build.AuthMethod
		fail    bool
		want    []string
	}{
		{"ECR, nothing found", build.PushAccess{Registry: ecr, Repository: ecr + "/team/etl", Anonymous: true,
			CloudError: errors.New("aws is not on PATH")}, "docker", build.AuthAuto, false,
			[]string{"aws ecr get-login-password --region eu-west-1 | docker login --username AWS --password-stdin " + ecr,
				"aws is not on PATH", "aws sso login"}},
		{"Artifact Registry, docker config refused", build.PushAccess{Registry: "europe-west1-docker.pkg.dev",
			Repository: "europe-west1-docker.pkg.dev/p/r/etl", Source: build.SourceDockerConfig}, "docker", build.AuthAuto, true,
			[]string{"gcloud auth configure-docker europe-west1-docker.pkg.dev", "registry.auth_method: cloud"}},
		{"ACR, podman", build.PushAccess{Registry: "acmeprod.azurecr.io", Repository: "acmeprod.azurecr.io/etl",
			Source: build.SourceDockerConfig}, "/usr/bin/podman", build.AuthAuto, false,
			[]string{"az acr login --name acmeprod --expose-token --output tsv --query accessToken | podman login"}},
		{"Artifact Registry, the gcloud session refused", build.PushAccess{Registry: "europe-west1-docker.pkg.dev",
			Repository: "europe-west1-docker.pkg.dev/p/r/etl", Source: "the gcloud session"}, "docker", build.AuthAuto, true,
			[]string{"gcloud auth login", "roles/artifactregistry.writer", "the gcloud session"}},
		{"ACR, docker config refused", build.PushAccess{Registry: "acmeprod.azurecr.io", Repository: "acmeprod.azurecr.io/etl",
			Source: build.SourceDockerConfig}, "docker", build.AuthAuto, true,
			[]string{"az acr login --name acmeprod", "az login", "registry.auth_method: cloud"}},
		{"cloud mode, the gcloud session refused", build.PushAccess{Registry: "europe-west1-docker.pkg.dev",
			Repository: "europe-west1-docker.pkg.dev/p/r/etl", Source: "the gcloud session"}, "docker", build.AuthCloud, true,
			[]string{"gcloud auth login", "The docker config is not read with registry.auth_method: cloud", "auth_method: auto"}},
		{"docker_config never offers the cloud session", build.PushAccess{Registry: ecr, Repository: ecr + "/team/etl",
			Anonymous: true}, "docker", build.AuthDockerConfig, false,
			[]string{"docker login --username AWS"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := pushRefusal(tc.access, denied, tc.builder, tc.method)
			text := res.note
			if err != nil {
				text = err.Error()
			}
			if (err != nil) != tc.fail {
				t.Fatalf("err = %v, want a failure %v", err, tc.fail)
			}
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Errorf("refusal does not carry %q:\n%s", w, text)
				}
			}
			if tc.access.Source == "the gcloud session" && strings.Contains(text, "configure-docker") {
				t.Errorf("a refused cloud session is sent to configure-docker:\n%s", text)
			}
			if tc.method == build.AuthDockerConfig && strings.Contains(text, "session") {
				t.Errorf("a docker_config refusal offers the cloud session:\n%s", text)
			}
		})
	}
}

// The credential source is not an input of the image: the content label
// is the same whatever registry.auth_method says, and the same as before
// auto existed, so no deployed label moves.
func TestContentLabelIgnoresTheAuthMethod(t *testing.T) {
	t.Parallel()
	methods := []string{"", "auto", "docker_config", "cloud"}
	hashes := make([]string, 0, len(methods))
	for _, m := range methods {
		dir := goldenProject(t)
		yaml := "dag_id: golden\ninclude_paths: [lib]\n"
		if m != "" {
			yaml += "registry:\n  auth_method: " + m + "\n"
		}
		writeProjectFile(t, dir, "dexaflow.yaml", yaml)
		cfg, err := loadProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := contentHash(dir, cfg, "Dockerfile", filepath.Join(dir, "dag.json"))
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, got)
	}
	for i, h := range hashes[1:] {
		if h != hashes[0] {
			t.Errorf("auth method #%d moves the label: %s, want %s", i+1, h, hashes[0])
		}
	}
}

// The dependency layer, which pushes without a daemon, reads the
// registry with the project's auth method too.
func TestTheDependencyLayerUsesTheAuthMethod(t *testing.T) {
	d := testDeps()
	dir, _, _ := wheelProject(t, d, "[pandas==2.2.3]")
	editYAML(t, dir, "  image_name: etl\n", "  image_name: etl\n  auth_method: docker_config\n")
	split, why := splitOf(t, d, dir)
	if split == nil || split.Wheels == nil {
		t.Fatalf("split = %+v (%q), want the dependency layer", split, why)
	}
	if split.Wheels.Registry.Auth != build.AuthDockerConfig {
		t.Errorf("dependency layer auth = %q, want docker_config", split.Wheels.Registry.Auth)
	}
}

// TestDeployIamReadsTheDockerConfig: iam reads as auto, so a project that
// set it next to a docker login keeps that credential.
func TestDeployIamReadsTheDockerConfig(t *testing.T) {
	d := testDeps()
	reg := newFakeOCIRegistry(t, singleManifest)
	reg.basicAuth = "bot:secret"
	dockerCredentialsFor(t, reg.host)
	withCloudSession(t, d, reg.host, "oauth2accesstoken", "other")
	dir, builder, _ := deployFixtureAt(t, reg.host+"/team")
	writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: etl\nregistry:\n  url: "+reg.host+"/team\n  image_name: etl\n  auth_method: iam\n")
	out, errOut, err := deployDirDeps(t, d, &deployServer{}, dir, builder, "--force")
	if err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(out, "accepts a push (credential from the docker config)") {
		t.Errorf("output does not push with the docker config:\n%s", out)
	}
}

// TestDeployStopsOnARefusedCloudSession: through the deploy, a push the
// registry refuses to the cloud session's credential stops before the
// build, naming where the credential came from.
func TestDeployStopsOnARefusedCloudSession(t *testing.T) {
	d := testDeps()
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	reg.denyPush = true
	withCloudSession(t, d, reg.host, "oauth2accesstoken", "tok")
	dir, _, _ := deployFixtureAt(t, reg.host+"/team")
	builder, log := argvBuilder(t)
	fake := &deployServer{}
	_, _, err := deployDirDeps(t, d, fake, dir, builder, "--force")
	if err == nil || !strings.Contains(err.Error(), "with the credential from the fake cloud session") {
		t.Fatalf("err = %v, want the refusal of the cloud session's credential", err)
	}
	if argvLines(t, log) != nil || fake.registers != 0 {
		t.Errorf("builder %q, registers %d; want nothing done", argvLines(t, log), fake.registers)
	}
}

// refusingCloud owns every host and fails the test when asked.
type refusingCloud struct{ t *testing.T }

func (refusingCloud) Owns(string) bool { return true }

func (r refusingCloud) Credential(context.Context, string) (build.Credential, error) {
	r.t.Error("the cloud session was asked")
	return build.Credential{}, errors.New("asked")
}

// baseProject is a project whose base image is on reg, with the registry
// keys given, that splits without uv.
func baseProject(t *testing.T, reg *fakeOCIRegistry, keys string) string {
	t.Helper()
	return versionProject(t, "base_image: "+reg.host+"/runtime:py3.11\ndependencies: [pandas==2.2.3]\n"+
		"registry:\n  url: "+reg.host+"/team\n  image_name: etl\n"+keys+"build:\n  platforms: [linux/amd64]\n")
}

// The base image is read with the project's auth method: docker_config
// runs no cloud CLI for it.
func TestTheBaseImageReadHonorsDockerConfig(t *testing.T) {
	d := testDeps()
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	d.registryCloud = refusingCloud{t}
	split, why := splitOf(t, d, baseProject(t, reg, "  auth_method: docker_config\n"))
	if split == nil {
		t.Fatalf("no split (%q), want the base read", why)
	}
}

// cloud reads the base image with the cloud session and never sends it
// the docker config's credential.
func TestTheBaseImageReadHonorsCloud(t *testing.T) {
	d := testDeps()
	reg := newFakeOCIRegistry(t, singleManifest)
	reg.basicAuth = "oauth2accesstoken:tok-1"
	dockerCredentialsFor(t, reg.host) // bot:secret
	withCloudSession(t, d, reg.host, "oauth2accesstoken", "tok-1")
	split, why := splitOf(t, d, baseProject(t, reg, "  auth_method: cloud\n"))
	if split == nil {
		t.Fatalf("no split (%q), want the base read with the cloud session", why)
	}
	if slices.Contains(reg.credentials(), "bot:secret") {
		t.Errorf("the base read sent the docker config's credential: %q", reg.credentials())
	}
}

// Projects that set env or iam hashed that value into their label before
// it was read; they keep that label. The hashes are those of the CLI
// before auto existed.
func TestContentLabelKeepsTheLegacyValues(t *testing.T) {
	t.Parallel()
	for m, want := range map[string]string{
		"env": "414c13f7938067487741bf7d2e3d838a7893bf8285d7d66ef28f8d6da649dcb5",
		"iam": "676550ba23a65778ddfba9f4bcc5576ff069f506f6535d5d986a4be79ac84e13",
	} {
		dir := goldenProject(t)
		writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: golden\ninclude_paths: [lib]\nregistry:\n  auth_method: "+m+"\n")
		cfg, err := loadProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := contentHash(dir, cfg, "Dockerfile", filepath.Join(dir, "dag.json"))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: label hash %s, want %s", m, got, want)
		}
	}
}
