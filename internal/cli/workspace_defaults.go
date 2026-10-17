package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	yaml "go.yaml.in/yaml/v3"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/envcompat"
)

// defaultsLayer is one source of deploy defaults, named for the user.
type defaultsLayer struct {
	values deployDefaults
	name   string
	// taskServiceAccount and warmPools are what the cluster's layer reports
	// about where tasks run (#1890); a workspace or a target sets neither.
	taskServiceAccount string
	warmPools          *domain.WarmPoolDefaults
}

// defaultsProvenance says where applyWorkspaceDefaults found what it applied.
type defaultsProvenance struct {
	// registrySource names where registry.url came from, "" when none is set.
	registrySource string
	// platformsSource and baseImageSource name where build.platforms and
	// base_image came from: --platform, the project's file, a target or the
	// workspace defaults; "" means the built-in default.
	platformsSource string
	baseImageSource string
	// sourceMode is the runtime image of source mode and where it came
	// from: a target, the workspace defaults or the cluster (#1772); empty
	// when nothing sets one, or when a local choice turns it off
	// (resolveSourceMode).
	sourceMode sourceModeChoice
	// remoteSource names where build.remote_builder came from: the
	// project's file, a target, the workspace defaults or the cluster; ""
	// when none is set.
	remoteSource string
	// cluster is the cluster's layer, nil when it advertises nothing: what
	// it says about the task service account and the warm pools is
	// reported (clusterExecutionLines), never written into the project.
	cluster *defaultsLayer
}

// applyWorkspaceDefaults fills what the project's dexaflow.yaml leaves unset
// from the workspace it lives in and the target being deployed to, so a
// workspace states its registry, platforms and base image once instead of in
// every project.
//
// The registry url is taken, in order, from: --registry, DEXAFLOW_REGISTRY
// (LEOFLOW_REGISTRY), the project's registry.url, the target's registry, the
// workspace's defaults.registry, then registry in the user's config file. With
// a url and no image_name, the image is named after the dag_id, as the schema
// documents. build.platforms comes from --platform, then the project, then
// the target, then the workspace defaults; base_image from the project, then
// the target, then the workspace defaults. withTarget is false for Lite, which
// deploys nowhere. A deploy adds the cluster's defaults after all of these
// (applyDeployDefaults).
func applyWorkspaceDefaults(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, withTarget bool) (defaultsProvenance, error) {
	return applyDefaults(cmd, dir, cfg, withTarget, nil)
}

// applyDeployDefaults is applyWorkspaceDefaults for a deploy, with the
// deploy defaults the cluster advertises (cluster, nil when none) as the
// last source of each value, after the user's config file and before the
// built-in default: every local source is a choice of the person deploying,
// so a cluster that starts advertising defaults changes no deploy that
// already resolved a value (#1704).
func applyDeployDefaults(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, cluster *defaultsLayer) (defaultsProvenance, error) {
	return applyDefaults(cmd, dir, cfg, true, cluster)
}

// applyDefaults is applyWorkspaceDefaults with an optional cluster layer.
func applyDefaults(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, withTarget bool, cluster *defaultsLayer) (defaultsProvenance, error) {
	layers, err := defaultsLayers(cmd, dir, withTarget)
	if err != nil {
		return defaultsProvenance{}, err
	}
	store, err := config.ReadUserStore(configFilePath(cmd))
	if err != nil {
		return defaultsProvenance{}, err
	}
	prov := defaultsProvenance{registrySource: applyRegistry(cmd, dir, cfg, registrySources{layers: layers, store: store, storePath: configFilePath(cmd), cluster: cluster}),
		cluster: cluster}
	if prov.sourceMode, err = resolveSourceMode(cmd, layers, cluster, prov.registrySource); err != nil {
		return defaultsProvenance{}, err
	}
	if cluster != nil {
		// The user's config file holds no platforms, base image or remote
		// builder, so the cluster is simply the last layer for those.
		layers = append(layers, *cluster)
	}
	prov.platformsSource, prov.baseImageSource = applyBuildDefaults(dir, cfg, layers)
	prov.remoteSource = applyRemoteBuilder(dir, cfg, layers)
	if prov.platformsSource, err = applyPlatformFlag(cmd, cfg, prov.platformsSource); err != nil {
		return defaultsProvenance{}, err
	}
	if cerr := checkPlatforms(cfg, prov.platformsSource); cerr != nil {
		return defaultsProvenance{}, cerr
	}
	return prov, nil
}

// defaultsLayers returns the target's defaults and the workspace's, most
// specific first.
//
// With a target (withTarget), both come from the workspace around the
// command's work dir (the current folder unless withWorkDir set another), the
// one the target was resolved from, so a target and the defaults
// it overrides always belong to the same file; that workspace's defaults apply
// only to projects inside it. Without one (Lite), the defaults are those of
// the workspace holding dir.
func defaultsLayers(cmd *cobra.Command, dir string, withTarget bool) ([]defaultsLayer, error) {
	if !withTarget {
		ws, err := findWorkspace(dir)
		if err != nil || ws == nil {
			return nil, err
		}
		return []defaultsLayer{{values: ws.Spec.Defaults, name: "defaults in " + ws.File}}, nil
	}
	serverFlag, _ := cmd.Flags().GetString("server") //nolint:errcheck // a command without --server has no override.
	choice, err := resolveServer(cmd, serverFlag)
	if err != nil {
		return nil, err
	}
	var layers []defaultsLayer
	ws := choice.tc.ws
	if t := choice.Target; t != nil {
		where := choice.tc.storePath
		if t.Scope == scopeWorkspace {
			where = ws.File
		}
		layers = append(layers, defaultsLayer{values: t.Defaults, name: fmt.Sprintf("target %q in %s", t.Name, where)})
	}
	if ws != nil && insideDir(dir, ws.Root) {
		layers = append(layers, defaultsLayer{values: ws.Spec.Defaults, name: "defaults in " + ws.File})
	}
	return layers, nil
}

// insideDir reports whether dir is root or below it, symlinks resolved.
func insideDir(dir, root string) bool {
	d, err := resolvedDir(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, d)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// registrySources are the places a registry url may come from below the
// flag, the environment and the project: the target and workspace layers,
// the user's config file at storePath, then the cluster (nil when none).
type registrySources struct {
	layers    []defaultsLayer
	store     *config.UserStore
	storePath string
	cluster   *defaultsLayer
}

// applyRegistry settles registry.url and image_name and returns the url's
// source.
func applyRegistry(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, src registrySources) string {
	if cfg.Registry == nil {
		cfg.Registry = &domain.RegistryConfig{}
	}
	url, source := pickRegistry(cmd, dir, cfg, src)
	if url == "" {
		return ""
	}
	cfg.Registry.URL = url
	if cfg.Registry.ImageName == "" {
		cfg.Registry.ImageName = imageNameFor(cfg.DagID)
	}
	return source
}

// pickRegistry returns the registry url by precedence and where it was found.
func pickRegistry(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, src registrySources) (url, source string) {
	if f, _ := cmd.Flags().GetString("registry"); f != "" { //nolint:errcheck // only deploy has --registry.
		return f, "--registry"
	}
	if v, ok := envcompat.Lookup("REGISTRY"); ok {
		return v, "DEXAFLOW_REGISTRY"
	}
	if u := strings.TrimSpace(cfg.Registry.URL); u != "" {
		return u, projectConfigPath(dir)
	}
	for _, l := range src.layers {
		if l.values.Registry != "" {
			return l.values.Registry, l.name
		}
	}
	if src.store.Registry != "" {
		return src.store.Registry, "registry in " + src.storePath
	}
	if c := src.cluster; c != nil && c.values.Registry != "" {
		return c.values.Registry, c.name
	}
	return "", ""
}

// applyBuildDefaults fills build.platforms and base_image from the layers when
// the project sets them not, and returns where each came from: the project's
// file, a layer, or "" for the built-in default.
func applyBuildDefaults(dir string, cfg *domain.LeoflowConfig, layers []defaultsLayer) (platforms, baseImage string) {
	if projectSetsPlatforms(dir) {
		platforms = projectConfigPath(dir)
	} else {
		for _, l := range layers {
			if len(l.values.Platforms) > 0 {
				cfg.Build.Platforms = append([]string(nil), l.values.Platforms...)
				platforms = l.name
				break
			}
		}
	}
	if cfg.BaseImage != "" {
		return platforms, projectConfigPath(dir)
	}
	for _, l := range layers {
		if l.values.BaseImage != "" {
			cfg.BaseImage = l.values.BaseImage
			return platforms, l.name
		}
	}
	return platforms, ""
}

// pickSourceMode returns the first layer's source_mode_image, most specific
// first, and the layer it came from; the zero value when none sets one. A
// layer that sets none (sourceModeNone) clears it: the choice is then off,
// with cleared set.
func pickSourceMode(layers []defaultsLayer) sourceModeChoice {
	for _, l := range layers {
		switch img := l.values.SourceModeImage; img {
		case "":
		case sourceModeNone:
			return sourceModeChoice{source: l.name, cleared: true}
		default:
			return sourceModeChoice{image: img, source: l.name}
		}
	}
	return sourceModeChoice{}
}

// applyRemoteBuilder fills build.remote_builder from the target, the
// workspace defaults or the cluster's (#1890), in that order, when the
// project sets none, and returns where it came from, "" when nothing sets
// one.
func applyRemoteBuilder(dir string, cfg *domain.LeoflowConfig, layers []defaultsLayer) string {
	if cfg.Build.RemoteBuilder != nil {
		return projectConfigPath(dir)
	}
	for _, l := range layers {
		if l.values.RemoteBuilder != nil {
			rb := *l.values.RemoteBuilder
			cfg.Build.RemoteBuilder = &rb
			return l.name
		}
	}
	return ""
}

// projectSetsPlatforms reports whether the project's own file declares
// build.platforms. The loaded config cannot say: ApplyDefaults fills in
// linux/amd64 when it is absent.
func projectSetsPlatforms(dir string) bool {
	data, err := os.ReadFile(projectConfigPath(dir)) //nolint:gosec // G304: the project the operator named.
	if err != nil {
		return false
	}
	var raw struct {
		Build *struct {
			Platforms []string `yaml:"platforms"`
		} `yaml:"build"`
	}
	if yaml.Unmarshal(data, &raw) != nil {
		return false
	}
	return raw.Build != nil && raw.Build.Platforms != nil
}

// imageNameFor derives an image name from a dag_id: lowercase, since image
// names cannot hold capitals, and starting and ending with a letter or digit.
func imageNameFor(dagID string) string {
	return strings.Trim(strings.ToLower(dagID), "_-")
}
