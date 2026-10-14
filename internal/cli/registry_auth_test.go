package cli

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dexadata/dexaflow/internal/build"
)

// fakeCloudSession stands in for the cloud CLIs (registryCloud): it holds
// user:pass for one registry host and counts the hosts it was asked for.
type fakeCloudSession struct {
	host, user, pass string
	mu               sync.Mutex
	asked            int
}

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

// withCloudSession makes the registry client read the cloud session from
// a fake holding user:pass for host, for the rest of the test.
func withCloudSession(t *testing.T, host, user, pass string) *fakeCloudSession {
	t.Helper()
	f := &fakeCloudSession{host: host, user: user, pass: pass}
	prev := registryCloud
	registryCloud = f
	t.Cleanup(func() { registryCloud = prev })
	return f
}

// cloudRegistryProject is a deploy fixture against a fake registry that
// opens only to the cloud session's credential, with extra registry keys.
func cloudRegistryProject(t *testing.T, registryKeys string) (reg *fakeOCIRegistry, dir, builder string, cloud *fakeCloudSession) {
	t.Helper()
	noDockerCredentials(t)
	reg = newFakeOCIRegistry(t, singleManifest)
	reg.basicAuth = "oauth2accesstoken:tok-1"
	cloud = withCloudSession(t, reg.host, "oauth2accesstoken", "tok-1")
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
	reg, dir, builder, _ := cloudRegistryProject(t, "")
	fake := &deployServer{}
	out, errOut, err := deployDir(t, fake, dir, builder, "--force")
	if err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, errOut)
	}
	want := "preflight:  " + reg.host + "/team/etl accepts a push (credential from the fake cloud session)"
	if !strings.Contains(out, want) || fake.registers != 1 {
		t.Errorf("registers=%d, output does not say %q:\n%s", fake.registers, want, out)
	}
}

// TestDeployAuthMethodDockerConfigIgnoresTheCloud: registry.auth_method
// docker_config reads the docker config only, as before auto: the cloud is
// never asked, and the preflight names the login command.
func TestDeployAuthMethodDockerConfigIgnoresTheCloud(t *testing.T) {
	reg, dir, _, cloud := cloudRegistryProject(t, "  auth_method: docker_config\n")
	builder, _ := argvBuilder(t) // named docker
	_, errOut, err := deployDir(t, &deployServer{}, dir, builder, "--force")
	if err == nil {
		t.Fatal("deploy reached a registry it holds no credential for")
	}
	if !strings.Contains(errOut, "docker login "+reg.host) || cloud.askedCount() != 0 {
		t.Errorf("cloud asked %d times; stderr does not name docker login %s:\n%s", cloud.askedCount(), reg.host, errOut)
	}
}

// TestDeployWarnsOfADeprecatedAuthMethod: the values the schema accepted
// before any was read still load, with a note; iam reads the cloud.
func TestDeployWarnsOfADeprecatedAuthMethod(t *testing.T) {
	_, dir, builder, cloud := cloudRegistryProject(t, "  auth_method: iam\n")
	out, errOut, err := deployDir(t, &deployServer{}, dir, builder, "--force")
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
	isolateCLIEnv(t)
	reg, dir, _, _ := cloudRegistryProject(t, "")
	t.Chdir(dir)
	srv := httptest.NewServer(&deployServer{})
	t.Cleanup(srv.Close)
	out := doctorAgainst(t, srv.URL, "tok")
	want := "registry      " + reg.host + "/team/etl accepts a push (credential from the fake cloud session)"
	if !strings.Contains(out, want) {
		t.Errorf("doctor does not say %q:\n%s", want, out)
	}
}

// The refusal of a push names the command of the registry's cloud, and,
// when the cloud CLI could not give a credential, why.
func TestPushRefusalNamesTheCloudCommand(t *testing.T) {
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
	dir, _, _ := wheelProject(t, "[pandas==2.2.3]")
	editYAML(t, dir, "  image_name: etl\n", "  image_name: etl\n  auth_method: docker_config\n")
	split, why := splitOf(t, dir)
	if split == nil || split.Wheels == nil {
		t.Fatalf("split = %+v (%q), want the dependency layer", split, why)
	}
	if split.Wheels.Registry.Auth != build.AuthDockerConfig {
		t.Errorf("dependency layer auth = %q, want docker_config", split.Wheels.Registry.Auth)
	}
}
