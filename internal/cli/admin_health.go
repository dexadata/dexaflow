package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// healthyStatus is the component status string the control plane reports for a
// component that is up.
const healthyStatus = "healthy"

// adminComponent is one named control-plane component and its reported status.
type adminComponent struct {
	name   string
	status string
}

// adminHealthReport is the compact status assembled from the monitor endpoints.
type adminHealthReport struct {
	components []adminComponent
	executor   *apiclient.ExecutorInfo
	version    *apiclient.VersionInfo
	healthy    bool
}

// newAdminHealthCommand builds `leoflow admin health`: a post-deploy smoke test
// that prints component health, executor capability, and version, and exits
// non-zero when the control plane is unhealthy.
func newAdminHealthCommand() *cobra.Command {
	var f adminFlags
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Report control-plane health; non-zero exit when unhealthy.",
		Long: "Query the monitor endpoints and print a compact status: component " +
			"health (scheduler, metadatabase, DAG processor, triggerer), executor " +
			"capability, and version. Exits non-zero when any component is unhealthy " +
			"or the health endpoint is unreachable — usable as a post-deploy smoke test.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, base, err := adminClient(cmd, f)
			if err != nil {
				return err
			}
			rep, err := fetchAdminHealth(cmdContext(cmd), c)
			if err != nil {
				return err
			}
			if rerr := renderAdminHealth(cmd.OutOrStdout(), base, rep); rerr != nil {
				return rerr
			}
			if !rep.healthy {
				return fmt.Errorf("control plane is unhealthy")
			}
			return nil
		},
	}
	addAdminFlags(cmd, &f)
	return cmd
}

// fetchAdminHealth queries /monitor/health for component status (the source of
// truth for the healthy/unhealthy verdict) and, best-effort, /monitor/executor
// and /version for the informational lines — an executor or version hiccup does
// not by itself flip the health verdict.
func fetchAdminHealth(ctx context.Context, c *apiclient.ClientWithResponses) (*adminHealthReport, error) {
	hresp, err := c.GetMonitorHealthWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying health: %w", err)
	}
	if hresp.StatusCode() != http.StatusOK || hresp.JSON200 == nil {
		return nil, fmt.Errorf("health endpoint returned %d: %s", hresp.StatusCode(), string(hresp.Body))
	}
	h := hresp.JSON200
	rep := &adminHealthReport{healthy: true}
	for _, cc := range []struct {
		name string
		comp *apiclient.ComponentHealth
	}{
		{"scheduler", h.Scheduler},
		{"metadatabase", h.Metadatabase},
		{"dag_processor", h.DagProcessor},
		{"triggerer", h.Triggerer},
	} {
		status := componentStatus(cc.comp)
		rep.components = append(rep.components, adminComponent{name: cc.name, status: status})
		if status != healthyStatus {
			rep.healthy = false
		}
	}
	if eresp, eerr := c.GetMonitorExecutorWithResponse(ctx); eerr == nil && eresp.JSON200 != nil {
		rep.executor = eresp.JSON200
	}
	if vresp, verr := c.GetVersionWithResponse(ctx); verr == nil && vresp.JSON200 != nil {
		rep.version = vresp.JSON200
	}
	return rep, nil
}

// componentStatus reports a component's status, treating a missing component or
// missing status as "unknown" (which counts as unhealthy).
func componentStatus(c *apiclient.ComponentHealth) string {
	if c == nil || c.Status == nil {
		return "unknown"
	}
	return *c.Status
}

// renderAdminHealth writes the compact report as a single block, ending in a
// HEALTHY / UNHEALTHY verdict line.
func renderAdminHealth(w io.Writer, base string, rep *adminHealthReport) error {
	lines := make([]string, 0, len(rep.components)+3)
	lines = append(lines, fmt.Sprintf("Control plane: %s", base))
	if rep.version != nil {
		lines = append(lines, healthLine("version", versionLine(rep.version)))
	}
	for _, c := range rep.components {
		lines = append(lines, healthLine(c.name, c.status))
	}
	if rep.executor != nil {
		lines = append(lines, healthLine("executor", executorLine(rep.executor)))
	}
	verdict := "HEALTHY"
	if !rep.healthy {
		verdict = "UNHEALTHY"
	}
	lines = append(lines, "Status: "+verdict)
	return writeLine(w, strings.Join(lines, "\n"))
}

// healthLine formats one aligned "  label:   value" row.
func healthLine(label, value string) string {
	return fmt.Sprintf("  %-14s %s", label+":", value)
}

// versionLine renders the version endpoint's info as "<version> (git <sha>)".
func versionLine(v *apiclient.VersionInfo) string {
	ver := deref(v.Version)
	if ver == "" {
		ver = "unknown"
	}
	if git := deref(v.GitVersion); git != "" {
		return fmt.Sprintf("%s (git %s)", ver, git)
	}
	return ver
}

// executorLine renders executor capability as a compact key=value summary.
func executorLine(e *apiclient.ExecutorInfo) string {
	parts := make([]string, 0, 3)
	if e.ExecutionModes != nil {
		parts = append(parts, "modes=["+strings.Join(*e.ExecutionModes, ",")+"]")
	}
	if e.PodDispatchEnabled != nil {
		parts = append(parts, fmt.Sprintf("pod_dispatch=%t", *e.PodDispatchEnabled))
	}
	if ns := deref(e.TaskNamespace); ns != "" {
		parts = append(parts, "namespace="+ns)
	}
	if len(parts) == 0 {
		return "(no capability reported)"
	}
	return strings.Join(parts, " ")
}
