package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// deployedState is what the control plane already holds for a content label.
type deployedState int

const (
	// deployUnknown: not registered, or the check could not tell; build.
	deployUnknown deployedState = iota
	// deployCurrent: the label is the DAG's current version; nothing to do.
	deployCurrent
	// deployStale: the label is registered but another version is current.
	deployStale
)

// checkDeployed asks the control plane, before anything is built, what it
// holds for the content-derived default label of dagID.
//
// The default label changes exactly when the DAG's hashed inputs change, so
// the label being the DAG's current version means this content is already
// what runs. Rebuilding it anyway (a re-run CI job) produced a new image
// digest under the same label, which the server refuses with 409 after a full
// build and push. A label that is registered but not current is content the
// DAG ran before; registering it again would be refused too, and making it
// current again is a rollback (#1702).
//
// The current label is the bundle_version of GET /api/v2/dags/{dag_id}/details
// and the registered ones are the bundle_version of each entry of
// GET /api/v2/dags/{dag_id}/dagVersions, both Airflow-compatible reads; with
// no current label the DAG is treated as unknown. A 404 (a DAG never
// registered), a 403, a network error and a 5xx after one retry let the
// deploy proceed as it did before this check existed, with a note for all but
// the 404. A 401 stops the deploy with the usual sign-in hint, since the
// register would be refused the same way; any other answer stops it too,
// naming --force.
func checkDeployed(ctx context.Context, warn io.Writer, serverURL, token, dagID, label string) (deployedState, string, error) {
	if dagID == "" || label == "" {
		return deployUnknown, "", nil
	}
	base := strings.TrimRight(serverURL, "/") + "/api/v2/dags/" + url.PathEscape(dagID)
	var details struct {
		BundleVersion *string `json:"bundle_version"`
	}
	if ok, err := getControlPlaneJSON(ctx, warn, base+"/details", token, label, &details); !ok || err != nil {
		return deployUnknown, "", err
	}
	current := ""
	if details.BundleVersion != nil {
		current = *details.BundleVersion
	}
	if current == "" {
		// No current version to compare with: nothing says this content is
		// stale, so build as for an unknown label.
		return deployUnknown, "", nil
	}
	if current == label {
		return deployCurrent, current, nil
	}
	var versions struct {
		DagVersions []struct {
			BundleVersion *string `json:"bundle_version"`
		} `json:"dag_versions"`
	}
	if ok, err := getControlPlaneJSON(ctx, warn, base+"/dagVersions", token, label, &versions); !ok || err != nil {
		return deployUnknown, current, err
	}
	for _, v := range versions.DagVersions {
		if v.BundleVersion != nil && *v.BundleVersion == label {
			return deployStale, current, nil
		}
	}
	return deployUnknown, current, nil
}

// getControlPlaneJSON GETs u and decodes a 2xx body into out. It reports
// ok=false and no error when the deploy should simply proceed: a 404, a 403,
// a network or read error, or a 5xx that a single retry did not cure. It
// returns an error when the deploy should stop: a 401, any other status, or a
// 2xx body it cannot decode.
func getControlPlaneJSON(ctx context.Context, warn io.Writer, u, token, label string, out any) (ok bool, err error) {
	note := func(reason string) {
		slog.DebugContext(ctx, "pre-deploy check skipped", "url", u, "reason", reason)
		//nolint:errcheck // advisory: the deploy proceeds either way
		fmt.Fprintf(warn, "note: could not check whether %s is already deployed (%s); deploying\n", label, reason)
	}
	status, body, err := getOnce(ctx, u, token)
	if err == nil && status >= http.StatusInternalServerError {
		status, body, err = getOnce(ctx, u, token)
	}
	switch {
	case err != nil:
		note(err.Error())
		return false, nil
	case status == http.StatusUnauthorized:
		return false, fmt.Errorf("checking whether %s is already deployed: %w", label, apiStatusError(status, body))
	case status == http.StatusNotFound:
		return false, nil
	case status == http.StatusForbidden || status >= http.StatusInternalServerError:
		note(fmt.Sprintf("server returned %d", status))
		return false, nil
	case status >= http.StatusMultipleChoices:
		return false, fmt.Errorf("checking whether %s is already deployed: %w; pass --force to deploy without the check",
			label, apiStatusError(status, body))
	}
	if derr := json.Unmarshal(body, out); derr != nil {
		return false, fmt.Errorf("checking whether %s is already deployed: decoding %s: %w; pass --force to deploy without the check", label, u, derr)
	}
	return true, nil
}

// getOnce performs one authenticated GET and returns its status and body.
func getOnce(ctx context.Context, u, token string) (status int, body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return 0, nil, fmt.Errorf("building request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only response body
	body, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("reading the response: %w", err)
	}
	return resp.StatusCode, body, nil
}
