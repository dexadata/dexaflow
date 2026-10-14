package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// clusterDefaultsCache holds what each server advertised during one command,
// so `deploy --all` reads it once and not once per DAG. A nil cache reads
// every time.
type clusterDefaultsCache struct {
	mu     sync.Mutex
	layers map[string]*defaultsLayer
}

// layer returns the cluster's defaults as the last layer of a deploy to
// serverURL, nil when it advertises none. Read failures warn on warn and
// return nil: the deploy goes on with its local settings, as before the
// route existed.
func (c *clusterDefaultsCache) layer(ctx context.Context, warn io.Writer, serverURL, token string) *defaultsLayer {
	if c == nil {
		return readClusterLayer(ctx, warn, serverURL, token)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.layers[serverURL]; ok {
		return l
	}
	l := readClusterLayer(ctx, warn, serverURL, token)
	if c.layers == nil {
		c.layers = map[string]*defaultsLayer{}
	}
	c.layers[serverURL] = l
	return l
}

// readClusterLayer reads and checks the server's deploy defaults.
func readClusterLayer(ctx context.Context, warn io.Writer, serverURL, token string) *defaultsLayer {
	d, found, err := fetchClusterDefaults(ctx, serverURL, token)
	if err != nil {
		//nolint:errcheck // advisory: the deploy goes on with its local settings
		fmt.Fprintf(warn, "warning: ignoring the deploy defaults of %s: %v; deploying with the local settings only\n", serverURL, err)
		return nil
	}
	if !found || d.IsEmpty() {
		return nil
	}
	return &defaultsLayer{
		values: deployDefaults{Registry: d.Registry, Platforms: d.Platforms, BaseImage: d.BaseImage},
		name:   "the deploy defaults of " + serverURL,
	}
}

// fetchClusterDefaults reads GET /api/v2/deployDefaults with the public
// client (apiclient.Client.GetDeployDefaults). found is false, with no
// error, when the server has no such route (404). An answer the server
// should never send (an error status, a body that is not the documented
// object, a value no deploy could use) is an error.
func fetchClusterDefaults(ctx context.Context, serverURL, token string) (d domain.DeployDefaults, found bool, err error) {
	c, err := apiclient.NewClient(serverURL, apiclient.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}))
	if err != nil {
		return d, false, fmt.Errorf("building the client: %w", err)
	}
	var editors []apiclient.RequestEditorFn
	if token != "" {
		editors = append(editors, func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		})
	}
	got, err := c.GetDeployDefaults(ctx, editors...)
	if errors.Is(err, apiclient.ErrDeployDefaultsNotServed) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d = domain.DeployDefaults{Registry: got.Registry, Platforms: got.Platforms, BaseImage: got.BaseImage}
	if verr := d.Validate("deploy.defaults"); verr != nil {
		return domain.DeployDefaults{}, false, verr
	}
	return d, true, nil
}
