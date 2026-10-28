package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
)

// dockerfileShadow is what warnShadowedByDockerfile needs to know about a
// build: whether the project ships its own Dockerfile (own) and its name, the
// project's config file (dexaflow.yaml, or a legacy leoflow.yaml), and where
// base_image came from (defaultsProvenance.baseImageSource).
type dockerfileShadow struct {
	own             bool
	dockerfile      string
	configFile      string
	baseImageSource string
}

// shadowedByDockerfile lists the fields the project's config file declares
// that only the generated Dockerfile applies: base_image, dependencies,
// connectors, system_packages and include_paths entries other than ".". A
// project Dockerfile is used as-is (ADR 0003), so none of them reaches the
// image it builds. A base_image the workspace, a target or the cluster filled
// in is not the project's own declaration, so it is not listed.
func shadowedByDockerfile(cfg *domain.LeoflowConfig, s dockerfileShadow) []string {
	var fields []string
	if cfg.BaseImage != "" && s.baseImageSource == s.configFile {
		fields = append(fields, "base_image")
	}
	if len(cfg.Dependencies) > 0 {
		fields = append(fields, "dependencies")
	}
	if len(cfg.Connectors) > 0 {
		fields = append(fields, "connectors")
	}
	if len(cfg.SystemPackages) > 0 {
		fields = append(fields, "system_packages")
	}
	for _, p := range cfg.IncludePaths {
		if q := strings.TrimSpace(p); q != "" && filepath.Clean(q) != "." {
			fields = append(fields, "include_paths")
			break
		}
	}
	return fields
}

// warnShadowedByDockerfile tells the author that the project Dockerfile
// replaces the generated one, so the fields of the config file only the
// generated one applies are ignored, and names them. Nothing is printed for a
// generated Dockerfile, nor when the project declares none of them.
func warnShadowedByDockerfile(w io.Writer, s dockerfileShadow, cfg *domain.LeoflowConfig) {
	if !s.own {
		return
	}
	fields := shadowedByDockerfile(cfg, s)
	if len(fields) == 0 {
		return
	}
	verb, pronoun := "are", "them"
	if len(fields) == 1 {
		verb, pronoun = "is", "it"
	}
	name := filepath.Base(s.configFile)
	//nolint:errcheck // advisory output; a write failure must not fail the build
	fmt.Fprintf(w, "warning: the project builds from its own Dockerfile (%s), so %s in %s %s ignored: "+
		"only the generated Dockerfile applies %s. Install %s in your Dockerfile, or remove the Dockerfile to build from "+
		"%s (a project Dockerfile is the escape hatch of ADR 0003)\n",
		s.dockerfile, strings.Join(fields, ", "), name, verb, pronoun, pronoun, name)
}

// warnIgnoredBuildFields warns about the build fields the config file
// (configFile, dexaflow.yaml or a legacy leoflow.yaml) accepts and nothing
// applies: build.context other than "." (the build always uses the DAG
// directory, #1062) and build.labels (no label is set on the image).
func warnIgnoredBuildFields(w io.Writer, configFile string, cfg *domain.LeoflowConfig) {
	if cfg.Build == nil {
		return
	}
	if c := strings.TrimSpace(cfg.Build.Context); c != "" && filepath.Clean(c) != "." {
		//nolint:errcheck // advisory output; a write failure must not fail the compile
		fmt.Fprintf(w, "warning: build.context is ignored: the build always uses the directory holding %s, not %q (#1062)\n", configFile, cfg.Build.Context)
	}
	if len(cfg.Build.Labels) > 0 {
		//nolint:errcheck // advisory output; a write failure must not fail the compile
		fmt.Fprintln(w, "warning: build.labels is ignored: no label is set on the image, whichever builder runs")
	}
}
