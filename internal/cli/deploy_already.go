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

// alreadyDeployed reports whether dagID already has a version registered
// under the content-derived default label, checked before anything is built.
//
// The default label changes exactly when the DAG's hashed inputs change, so a
// registered label means this content is already deployed. Rebuilding it
// anyway (a re-run CI job) produced a new image digest under the same label,
// which the server refuses with 409 after a full build and push. A check that
// cannot be made (network, auth, an older server) is not a reason to stop a
// deploy that worked without it: it reports false, and the deploy proceeds and
// meets the server's own verdict at register.
func alreadyDeployed(ctx context.Context, warn io.Writer, serverURL, token, dagID, label string) bool {
	if dagID == "" || label == "" {
		return false
	}
	ok, err := versionRegistered(ctx, serverURL, token, dagID, label)
	if err != nil {
		slog.DebugContext(ctx, "checking for an already deployed version", "dag_id", dagID, "version", label, "error", err)
		//nolint:errcheck // advisory: the deploy proceeds either way
		fmt.Fprintf(warn, "note: could not check whether %s is already deployed (%v); deploying\n", label, err)
		return false
	}
	return ok
}

// versionRegistered asks the control plane, through the Airflow-compatible
// GET /api/v2/dags/{dag_id}/dagVersions that carries the label as
// bundle_version, whether dagID has a version labeled label. A 404 is a DAG
// never registered; any other non-2xx is an error.
func versionRegistered(ctx context.Context, serverURL, token, dagID, label string) (bool, error) {
	u := strings.TrimRight(serverURL, "/") + "/api/v2/dags/" + url.PathEscape(dagID) + "/dagVersions"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return false, fmt.Errorf("building request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return false, fmt.Errorf("listing versions of %s: %w", dagID, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only response body
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return false, fmt.Errorf("listing versions of %s: server returned %d", dagID, resp.StatusCode)
	}
	var body struct {
		DagVersions []struct {
			BundleVersion *string `json:"bundle_version"`
		} `json:"dag_versions"`
	}
	if derr := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&body); derr != nil {
		return false, fmt.Errorf("decoding versions of %s: %w", dagID, derr)
	}
	for _, v := range body.DagVersions {
		if v.BundleVersion != nil && *v.BundleVersion == label {
			return true, nil
		}
	}
	return false, nil
}
