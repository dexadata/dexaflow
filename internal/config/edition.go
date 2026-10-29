package config

// The values of ui.edition (ADR 0027). The edition says how the server is
// delivered, not what it may do: the Helm chart on Kubernetes or the
// installable binary. Both are the same commit and the same license (ADR 0028).
const (
	// EditionCluster is the Helm chart on Kubernetes.
	EditionCluster = "cluster"
	// EditionLite is the installable binary (`dexaflow lite`).
	EditionLite = "lite"
	// EditionProAlias is the earlier name of EditionCluster. It is still
	// accepted so a chart that sets it keeps working while the rename rolls
	// out; IsClusterEdition treats both the same.
	EditionProAlias = "pro"
)

// IsClusterEdition reports whether edition is the cluster edition, under its
// current name or the earlier one.
func IsClusterEdition(edition string) bool {
	return edition == EditionCluster || edition == EditionProAlias
}

// ClusterEdition reports whether the server runs as the cluster edition.
func (c *ServerConfig) ClusterEdition() bool {
	return IsClusterEdition(c.UI.Edition)
}

// PoolsEnabled reports whether named pools are on: scheduler.pools.enabled
// when it is set, otherwise the cluster edition (#2173).
func (c *ServerConfig) PoolsEnabled() bool {
	if c.Scheduler.Pools.Enabled != nil {
		return *c.Scheduler.Pools.Enabled
	}
	return c.ClusterEdition()
}
