package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// dagVersionSpecMaxBody bounds the spec GetDagVersionSpec reads.
const dagVersionSpecMaxBody = 16 << 20

// StatusError is the answer of a hand-written call when the server answers
// with a status the call does not expect: the status and the body as served
// (bounded), so the caller can tell one refusal from another.
type StatusError struct {
	// Path is the route that was called.
	Path string
	// StatusCode is the HTTP status of the answer.
	StatusCode int
	// Body is the answer's body.
	Body []byte
}

// Error names the route and the status.
func (e *StatusError) Error() string {
	return fmt.Sprintf("%s returned %d", e.Path, e.StatusCode)
}

// GetDagVersionSpec reads GET
// /api/v2/dags/{dag_id}/dagVersions/{version_number}/spec, the dag.json
// registered with that version of the DAG, through the client's Doer, server
// URL and request editors, then reqEditors (#1708). The spec is returned as
// served, byte for byte, so it can be registered again unchanged. An answer
// other than 200 is a *StatusError: a 404 is an unknown version or a server
// from before the route. The route gets no generated method (see
// overlay.yaml): a new method on ClientInterface would break every
// implementation of it.
func (c *Client) GetDagVersionSpec(ctx context.Context, dagID string, versionNumber int, reqEditors ...RequestEditorFn) (json.RawMessage, error) {
	path := "/api/v2/dags/" + url.PathEscape(dagID) + "/dagVersions/" + strconv.Itoa(versionNumber) + "/spec"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Server, "/")+path, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building the %s request: %w", path, err)
	}
	if eerr := c.applyEditors(ctx, req, reqEditors); eerr != nil {
		return nil, eerr
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // read-only response
	body, err := io.ReadAll(io.LimitReader(resp.Body, dagVersionSpecMaxBody))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Path: path, StatusCode: resp.StatusCode, Body: body}
	}
	return json.RawMessage(body), nil
}
