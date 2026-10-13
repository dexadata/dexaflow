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
// deploys nowhere.
func applyWorkspaceDefaults(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, withTarget bool) (defaultsProvenance, error) {
	layers, err := defaultsLayers(cmd, dir, withTarget)
	if err != nil {
		return defaultsProvenance{}, err
	}
	store, err := config.ReadUserStore(configFilePath(cmd))
	if err != nil {
		return defaultsProvenance{}, err
	}
	prov := defaultsProvenance{registrySource: applyRegistry(cmd, dir, cfg, layers, store, configFilePath(cmd))}
	prov.platformsSource, prov.baseImageSource = applyBuildDefaults(dir, cfg, layers)
	if prov.platformsSource, err = applyPlatformFlag(cmd, cfg, prov.platformsSource); err != nil {
		return defaultsProvenance{}, err
	}
	return prov, nil
}

// defaultsLayers returns the target's defaults and the workspace's, most
// specific first.
//
// With a target (withTarget), both come from the workspace around the current
// folder, the one the target was resolved from, so a target and the defaults
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

// applyRegistry settles registry.url and image_name and returns the url's
// source.
func applyRegistry(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, layers []defaultsLayer, store *config.UserStore, storePath string) string {
	if cfg.Registry == nil {
		cfg.Registry = &domain.RegistryConfig{}
	}
	url, source := pickRegistry(cmd, dir, cfg, layers, store, storePath)
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
func pickRegistry(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, layers []defaultsLayer, store *config.UserStore, storePath string) (url, source string) {
	if f, _ := cmd.Flags().GetString("registry"); f != "" { //nolint:errcheck // only deploy has --registry.
		return f, "--registry"
	}
	if v, ok := envcompat.Lookup("REGISTRY"); ok {
		return v, "DEXAFLOW_REGISTRY"
	}
	if u := strings.TrimSpace(cfg.Registry.URL); u != "" {
		return u, projectConfigPath(dir)
	}
	for _, l := range layers {
		if l.values.Registry != "" {
			return l.values.Registry, l.name
		}
	}
	if store.Registry != "" {
		return store.Registry, "registry in " + storePath
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
