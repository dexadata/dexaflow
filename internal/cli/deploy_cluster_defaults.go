package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/spf13/cobra"

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

// buildClusterLayer is the cluster layer of `build --push`: the deploy
// defaults of the server a deploy would go to, read as a deploy reads them
// (404 silent, other failures warned), so the image it pushes carries the
// label a later `deploy --skip-build` to that server looks up. It is nil
// when no server is configured (the built-in localhost is not assumed) or
// the server cannot be resolved; the build then uses the local sources only.
func buildClusterLayer(cmd *cobra.Command) *defaultsLayer {
	choice, token, err := resolveServerChoiceToken(cmd, "", "")
	if err != nil || choice.Implicit {
		return nil
	}
	return readClusterLayer(cmdContext(cmd), cmd.ErrOrStderr(), choice.URL, token)
}

// readClusterLayer reads and checks the server's deploy defaults
// (clusterLayer), warning on warn and returning nil when they are unusable.
func readClusterLayer(ctx context.Context, warn io.Writer, serverURL, token string) *defaultsLayer {
	l, err := clusterLayer(ctx, serverURL, token)
	if err != nil {
		//nolint:errcheck // advisory: the deploy goes on with its local settings
		fmt.Fprintf(warn, "warning: ignoring the deploy defaults of %s: %v; deploying with the local settings only\n", serverURL, err)
		return nil
	}
	return l
}

// clusterLayer reads and checks the server's deploy defaults as the last
// layer of a deploy to serverURL: nil when it advertises none, an error when
// what it advertises cannot be used (a deploy then ignores it).
func clusterLayer(ctx context.Context, serverURL, token string) (*defaultsLayer, error) {
	d, found, err := fetchClusterDefaults(ctx, serverURL, token)
	if err != nil {
		return nil, err
	}
	if !found || d.IsEmpty() {
		return nil, nil //nolint:nilnil // no defaults advertised is a normal outcome, not an error.
	}
	return &defaultsLayer{
		values: deployDefaults{Registry: d.Registry, Platforms: d.Platforms, BaseImage: d.BaseImage, SourceModeImage: d.SourceModeImage,
			RemoteBuilder: d.RemoteBuilder},
		name:               "the deploy defaults of " + serverURL,
		taskServiceAccount: d.TaskServiceAccount,
		warmPools:          d.WarmPools,
	}, nil
}

// fetchClusterDefaults reads GET /api/v2/deployDefaults with the public
// client (apiclient.Client.GetDeployDefaults). found is false, with no
// error, when the server has no such route (404). An answer the server
// should never send (an error status, a body that is not the documented
// object, a value no deploy could use) is an error.
func fetchClusterDefaults(ctx context.Context, serverURL, token string) (d domain.DeployDefaults, found bool, err error) {
	c, err := cliBareAPIClient(serverURL, 15*time.Second)
	if err != nil {
		return d, false, fmt.Errorf("building the client: %w", err)
	}
	editors := []apiclient.RequestEditorFn{func(_ context.Context, req *http.Request) error {
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return nil
	}}
	got, err := c.GetDeployDefaults(ctx, editors...)
	if errors.Is(err, apiclient.ErrDeployDefaultsNotServed) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d = domain.DeployDefaults{Registry: got.Registry, Platforms: got.Platforms, BaseImage: got.BaseImage, SourceModeImage: got.SourceModeImage,
		RemoteBuilder: remoteBuilderOf(got.RemoteBuilder), TaskServiceAccount: got.TaskServiceAccount}
	if w := got.WarmPools; w != nil {
		d.WarmPools = &domain.WarmPoolDefaults{Enabled: w.Enabled, MinIdleWorkers: w.MinIdleWorkers, MaxPoolSize: w.MaxPoolSize}
	}
	if verr := d.Validate("deploy.defaults"); verr != nil {
		return domain.DeployDefaults{}, false, verr
	}
	return d, true, nil
}

// remoteBuilderOf is the remote builder a deployDefaults answer advertises,
// nil when none. One with no backend is kept, so Validate refuses it.
func remoteBuilderOf(rb *apiclient.DeployRemoteBuilder) *domain.RemoteBuilderConfig {
	if rb == nil {
		return nil
	}
	out := &domain.RemoteBuilderConfig{}
	if cb := rb.CloudBuild; cb != nil {
		out.CloudBuild = &domain.CloudBuildConfig{Project: cb.Project, Region: cb.Region, WorkerPool: cb.WorkerPool, ServiceAccount: cb.ServiceAccount}
	}
	if bk := rb.BuildKit; bk != nil {
		// No kubeconfig context: a server cannot know the names of this
		// machine's contexts, so the current one reaches the builder.
		out.BuildKit = &domain.BuildKitConfig{Namespace: bk.Namespace, PodSelector: bk.PodSelector}
	}
	return out
}
