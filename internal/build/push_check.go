package build

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	gtransport "github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// ErrPushDenied is the registry refusing a push to the repository (401 or
// 403): the credential is missing, or may not push there.
var ErrPushDenied = errors.New("the registry refused a push")

// PushAccess is what a push check found out about a repository.
type PushAccess struct {
	// Registry is the registry host, as a `docker login` takes it.
	Registry string
	// Repository is the repository a push goes to, <registry>/<path>.
	Repository string
	// Anonymous is true when no credential was found for the registry, in
	// the docker config, its credential helpers or the cloud session, as
	// the auth method reads them.
	Anonymous bool
	// Source names where the credential came from (SourceDockerConfig, or
	// the cloud CLI's session); empty when Anonymous.
	Source string
	// CloudError says why the cloud session gave no credential, when it was
	// asked and could not (the CLI is not installed or not signed in). It
	// carries the command and the last line of its stderr, capped; the token
	// is on stdout and never in it.
	CloudError error
}

// CheckPush asks the registry of image whether a push to its repository is
// allowed, with the credentials a push would use (resolveAuth),
// before anything is built (#1705). It starts a blob upload, which a registry
// authorizes like a push, and cancels it, best effort (cancelUpload): a
// registry that refuses the DELETE (ECR and Harbor may) keeps an empty
// upload until it expires it, and no blob or manifest is written. A 401 or 403
// is ErrPushDenied; any other failure says nothing about the right to push
// and is returned as it is. Plain HTTP is spoken only to the loopback, as for
// Resolve.
func (r Registry) CheckPush(ctx context.Context, image string) (PushAccess, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return PushAccess{}, fmt.Errorf("image %q is not a valid reference: %w", image, err)
	}
	repo := ref.Context()
	cred, err := r.resolveAuth(ctx, repo)
	if err != nil {
		return PushAccess{}, err
	}
	access := PushAccess{Registry: repo.RegistryStr(), Repository: repo.Name(), Anonymous: cred.auth == authn.Anonymous,
		Source: cred.source, CloudError: cred.cloudErr}
	rt, err := gtransport.NewWithContext(ctx, repo.Registry, cred.auth, transport(remote.DefaultTransport),
		[]string{repo.Scope(gtransport.PushScope)})
	if err != nil {
		return access, pushCheckError(access, err)
	}
	client := &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	start := url.URL{Scheme: repo.Scheme(), Host: repo.RegistryStr(), Path: "/v2/" + repo.RepositoryStr() + "/blobs/uploads/"}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, start.String(), http.NoBody)
	if err != nil {
		return access, fmt.Errorf("building the push check: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return access, pushCheckError(access, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // the status is the answer
	if cerr := gtransport.CheckError(resp, http.StatusAccepted); cerr != nil {
		return access, pushCheckError(access, cerr)
	}
	cancelUpload(ctx, client, resp)
	return access, nil
}

// cancelUpload deletes the upload a push check started, best effort: its
// answer is ignored, and a registry that refuses the DELETE (ECR and Harbor
// may) expires the empty upload on its own.
func cancelUpload(ctx context.Context, client *http.Client, resp *http.Response) {
	loc, err := resp.Location()
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, loc.String(), http.NoBody)
	if err != nil {
		return
	}
	if dresp, derr := client.Do(req); derr == nil {
		_ = dresp.Body.Close() //nolint:errcheck // best effort
	}
}

// pushCheckError wraps err of a push check of access, as ErrPushDenied when
// the registry answered 401 or 403.
func pushCheckError(access PushAccess, err error) error {
	var terr *gtransport.Error
	if errors.As(err, &terr) && (terr.StatusCode == http.StatusUnauthorized || terr.StatusCode == http.StatusForbidden) {
		return fmt.Errorf("%w: %s: %w", ErrPushDenied, access.Repository, err)
	}
	return fmt.Errorf("checking push access to %s: %w", access.Repository, err)
}
