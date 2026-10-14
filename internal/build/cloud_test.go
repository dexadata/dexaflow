package build

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
)

// registry.auth_method takes auto (the default), docker_config and cloud.
// The values the schema accepted before (env, iam) still parse, with a note.
func TestParseAuthMethod(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want AuthMethod
		note bool
	}{
		{"", AuthAuto, false},
		{"auto", AuthAuto, false},
		{"docker_config", AuthDockerConfig, false},
		{"cloud", AuthCloud, false},
		{"iam", AuthCloud, true},
		{"env", AuthAuto, true},
	} {
		got, note, err := ParseAuthMethod(tc.in)
		if err != nil || got != tc.want || (note != "") != tc.note {
			t.Errorf("ParseAuthMethod(%q) = %q, %q, %v; want %q, a note %v", tc.in, got, note, err, tc.want, tc.note)
		}
		if tc.note && !strings.Contains(note, "registry.auth_method: "+tc.in) {
			t.Errorf("the note of %q does not name the setting: %q", tc.in, note)
		}
	}
	if _, _, err := ParseAuthMethod("vault"); err == nil {
		t.Error("an unknown method parsed")
	}
}

// The registries a cloud CLI signs in to are told by their host, and only
// those: a host that merely contains a cloud name is not one.
func TestDetectCloudRegistry(t *testing.T) {
	for _, tc := range []struct {
		host                   string
		provider, region, name string
	}{
		{"gcr.io", "Google Cloud", "", ""},
		{"eu.gcr.io", "Google Cloud", "", ""},
		{"europe-west1-docker.pkg.dev", "Google Cloud", "", ""},
		{"123456789012.dkr.ecr.eu-west-1.amazonaws.com", "AWS", "eu-west-1", ""},
		{"123456789012.dkr.ecr-fips.us-east-1.amazonaws.com", "AWS", "us-east-1", ""},
		{"123456789012.dkr.ecr.cn-north-1.amazonaws.com.cn", "AWS", "cn-north-1", ""},
		{"acmeprod.azurecr.io", "Azure", "", "acmeprod"},
		{"ghcr.io", "", "", ""},
		{"registry.example.com", "", "", ""},
		{"gcr.io.example.com", "", "", ""},
		{"dkr.ecr.eu-west-1.amazonaws.com", "", "", ""},
		{"127.0.0.1:5000", "", "", ""},
	} {
		got, ok := DetectCloudRegistry(tc.host)
		if ok != (tc.provider != "") || got.Provider != tc.provider || got.Region != tc.region || got.Name != tc.name {
			t.Errorf("DetectCloudRegistry(%q) = %+v, %v; want provider %q region %q name %q", tc.host, got, ok, tc.provider, tc.region, tc.name)
		}
	}
}

// The command that signs a tool in to a registry is the one its cloud
// documents: docker gets the cloud CLI's own login, any other tool the
// cloud's token piped to `<tool> login`.
func TestLoginCommand(t *testing.T) {
	ecr := "123456789012.dkr.ecr.eu-west-1.amazonaws.com"
	for _, tc := range []struct{ host, tool, want string }{
		{"europe-west1-docker.pkg.dev", "docker", "gcloud auth configure-docker europe-west1-docker.pkg.dev"},
		{"gcr.io", "podman", "gcloud auth print-access-token | podman login --username oauth2accesstoken --password-stdin gcr.io"},
		{ecr, "docker", "aws ecr get-login-password --region eu-west-1 | docker login --username AWS --password-stdin " + ecr},
		{ecr, "podman", "aws ecr get-login-password --region eu-west-1 | podman login --username AWS --password-stdin " + ecr},
		{"acmeprod.azurecr.io", "docker", "az acr login --name acmeprod"},
		{"acmeprod.azurecr.io", "nerdctl", "az acr login --name acmeprod --expose-token --output tsv --query accessToken | " +
			"nerdctl login --username 00000000-0000-0000-0000-000000000000 --password-stdin acmeprod.azurecr.io"},
		{"ghcr.io", "docker", "docker login ghcr.io"},
		{"ghcr.io", "", "docker login ghcr.io"},
		{"ghcr.io", "podman", "podman login ghcr.io"},
	} {
		if got := LoginCommand(tc.host, tc.tool); got != tc.want {
			t.Errorf("LoginCommand(%q, %q) =\n  %q\nwant\n  %q", tc.host, tc.tool, got, tc.want)
		}
	}
}

// recordingRunner stands in for the cloud CLIs: it records each argv and
// answers out, or fails with err.
type recordingRunner struct {
	mu    sync.Mutex
	out   string
	err   error
	calls []string
}

func (r *recordingRunner) run(_ context.Context, argv []string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, strings.Join(argv, " "))
	return []byte(r.out), r.err
}

// Each cloud's token comes from its own CLI, with the user name its
// registry expects; nothing runs for a host no cloud owns.
func TestCloudCLIReadsTheTokenOfEachCloud(t *testing.T) {
	for _, tc := range []struct{ host, argv, user string }{
		{"europe-west1-docker.pkg.dev", "gcloud auth print-access-token", "oauth2accesstoken"},
		{"123456789012.dkr.ecr.eu-west-1.amazonaws.com", "aws ecr get-login-password --region eu-west-1", "AWS"},
		{"acmeprod.azurecr.io", "az acr login --name acmeprod --expose-token --output tsv --query accessToken",
			"00000000-0000-0000-0000-000000000000"},
	} {
		r := &recordingRunner{out: "tok-123\n"}
		c := NewCloudCLI(r.run)
		got, err := c.Credential(context.Background(), tc.host)
		if err != nil {
			t.Fatalf("%s: %v", tc.host, err)
		}
		if got.Username != tc.user || got.Password != "tok-123" || !strings.Contains(got.Source, strings.Fields(tc.argv)[0]) {
			t.Errorf("%s: credential = %+v, want user %q, the token and the CLI as its source", tc.host, got, tc.user)
		}
		if strings.Join(r.calls, ";") != tc.argv {
			t.Errorf("%s: ran %q, want %q", tc.host, r.calls, tc.argv)
		}
	}
	r := &recordingRunner{out: "tok"}
	got, err := NewCloudCLI(r.run).Credential(context.Background(), "ghcr.io")
	if err != nil || got != (Credential{}) || r.calls != nil {
		t.Errorf("ghcr.io: credential %+v, err %v, ran %q; want none and nothing run", got, err, r.calls)
	}
}

// A token is read once and reused while it is fresh, so one deploy does
// not run the cloud CLI for every registry request.
func TestCloudCLIReusesAFreshToken(t *testing.T) {
	r := &recordingRunner{out: "tok"}
	c := NewCloudCLI(r.run)
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }
	host := "acmeprod.azurecr.io"
	for range 3 {
		if _, err := c.Credential(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.calls) != 1 {
		t.Fatalf("ran the CLI %d times, want once", len(r.calls))
	}
	now = now.Add(cloudTokenTTL + time.Second)
	if _, err := c.Credential(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 {
		t.Errorf("ran the CLI %d times after the token aged, want twice", len(r.calls))
	}
}

// A CLI that fails or prints no token gives no credential and says why; a
// failure is not cached, so signing in mid-watch is picked up.
func TestCloudCLIFailureSaysWhy(t *testing.T) {
	r := &recordingRunner{err: errors.New("exit status 1: You do not currently have an active account selected")}
	c := NewCloudCLI(r.run)
	host := "europe-west1-docker.pkg.dev"
	_, err := c.Credential(context.Background(), host)
	if err == nil || !strings.Contains(err.Error(), "gcloud auth print-access-token") || !strings.Contains(err.Error(), "active account") {
		t.Fatalf("err = %v, want the command and its failure", err)
	}
	r.err, r.out = nil, "  \n"
	if _, err := c.Credential(context.Background(), host); err == nil || len(r.calls) != 2 {
		t.Errorf("err = %v after %d runs, want an error for an empty token, run again", err, len(r.calls))
	}
}

// The real runner reports a CLI that is not installed by name.
func TestExecRunnerNamesAMissingCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := execRunner(context.Background(), []string{"gcloud", "auth", "print-access-token"})
	if err == nil || !strings.Contains(err.Error(), "gcloud is not on PATH") {
		t.Errorf("err = %v, want gcloud named as missing", err)
	}
}

// fakeCloud is a CloudKeychain that holds a credential for one host.
type fakeCloud struct {
	host, user, pass string
	err              error
}

func (f fakeCloud) Credential(_ context.Context, host string) (Credential, error) {
	if host != f.host {
		return Credential{}, nil
	}
	if f.err != nil {
		return Credential{}, f.err
	}
	return Credential{Username: f.user, Password: f.pass, Source: "the fake cloud session"}, nil
}

// basicAuthRegistry is an in-memory registry that answers only requests
// with Basic credentials user:pass.
func basicAuthRegistry(t *testing.T, user, pass string) string {
	t.Helper()
	inner := registry.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// With auth_method auto, a registry the docker config holds nothing for is
// reached with the cloud session's credential; the push check says where
// the credential came from. docker_config never asks the cloud.
func TestCheckPushUsesTheCloudSessionByMethod(t *testing.T) {
	withDockerConfig(t, "")
	host := basicAuthRegistry(t, "oauth2accesstoken", "tok")
	cloud := fakeCloud{host: host, user: "oauth2accesstoken", pass: "tok"}
	got, err := Registry{Cloud: cloud}.CheckPush(context.Background(), host+"/team/etl:v1")
	if err != nil {
		t.Fatalf("auto: %v", err)
	}
	if got.Anonymous || got.Source != "the fake cloud session" {
		t.Errorf("auto: access = %+v, want the cloud session as the source", got)
	}
	got, err = Registry{Auth: AuthDockerConfig, Cloud: cloud}.CheckPush(context.Background(), host+"/team/etl:v1")
	if !errors.Is(err, ErrPushDenied) || !got.Anonymous {
		t.Errorf("docker_config: access %+v, err %v; want an anonymous denial", got, err)
	}
}

// The docker config comes first under auto; cloud ignores it.
func TestCheckPushOrderOfSources(t *testing.T) {
	host := basicAuthRegistry(t, "bot", "secret")
	withDockerConfig(t, host)
	wrongCloud := fakeCloud{host: host, user: "oauth2accesstoken", pass: "other"}
	got, err := Registry{Cloud: wrongCloud}.CheckPush(context.Background(), host+"/team/etl:v1")
	if err != nil || got.Source != SourceDockerConfig {
		t.Errorf("auto: access %+v, err %v; want the docker config's credential", got, err)
	}
	got, err = Registry{Auth: AuthCloud, Cloud: wrongCloud}.CheckPush(context.Background(), host+"/team/etl:v1")
	if !errors.Is(err, ErrPushDenied) || got.Source != "the fake cloud session" {
		t.Errorf("cloud: access %+v, err %v; want the cloud credential, refused", got, err)
	}
}

// A cloud session that cannot give a credential leaves the check anonymous
// and keeps the reason for the preflight.
func TestCheckPushKeepsWhyTheCloudGaveNothing(t *testing.T) {
	withDockerConfig(t, "")
	host := basicAuthRegistry(t, "u", "p")
	got, err := Registry{Cloud: fakeCloud{host: host, err: errors.New("aws is not on PATH")}}.CheckPush(context.Background(), host+"/team/etl:v1")
	if !errors.Is(err, ErrPushDenied) || !got.Anonymous || got.CloudError == nil || !strings.Contains(got.CloudError.Error(), "aws is not on PATH") {
		t.Errorf("access %+v, err %v; want an anonymous denial carrying the cloud error", got, err)
	}
}

// Every registry request, not only the push check, reads the cloud
// session: Resolve reaches a registry only the cloud credential opens.
func TestResolveUsesTheCloudSession(t *testing.T) {
	withDockerConfig(t, "")
	host := basicAuthRegistry(t, "AWS", "pw")
	reg := Registry{Cloud: fakeCloud{host: host, user: "AWS", pass: "pw"}}
	if _, err := reg.Resolve(context.Background(), host+"/team/etl:v1"); err == nil || !strings.Contains(err.Error(), "NAME_UNKNOWN") {
		t.Fatalf("resolve = %v, want the registry's own not found past the auth", err)
	}
	if _, err := (Registry{Auth: AuthDockerConfig, Cloud: reg.Cloud}).Resolve(context.Background(), host+"/team/etl:v1"); err == nil ||
		strings.Contains(err.Error(), "NAME_UNKNOWN") {
		t.Errorf("docker_config resolve = %v, want the auth refused", err)
	}
}

// The dependency layer pushes with its Registry's credentials.
func TestDependencyLayerPushesWithItsRegistry(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	withDockerConfig(t, "")
	dst := basicAuthRegistry(t, "AWS", "pw")
	cloud := fakeCloud{host: dst, user: "AWS", pass: "pw"}
	d := depsLayer(t, testRegistry(t), &fakeInstaller{}, "linux/amd64")
	d.Registry = Registry{Auth: AuthDockerConfig, Cloud: cloud}
	if err := d.Push(context.Background(), dst+"/dags/etl:deps-1", []string{"linux/amd64"}); err == nil {
		t.Fatal("docker_config pushed with the cloud credential")
	}
	d.Registry = Registry{Cloud: cloud}
	if err := d.Push(context.Background(), dst+"/dags/etl:deps-1", []string{"linux/amd64"}); err != nil {
		t.Fatalf("auto: %v", err)
	}
}
