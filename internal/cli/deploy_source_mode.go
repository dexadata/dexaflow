package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// A deploy in source mode (#1772, ADR 0067) registers a DAG's dag.py as the
// version's source on the operator's runtime image: nothing is built or
// pushed, and no registry is needed. It applies when a target, the workspace
// defaults or the cluster's deploy defaults name the runtime image
// (source_mode_image), and only to a DAG that dag.py alone describes: one
// that needs a build is refused, with the reason, before any network call
// when the setting is local.

// sourceModeChoice is the runtime image of a source mode deploy and where it
// came from; the zero value is a deploy that builds.
type sourceModeChoice struct {
	image  string
	source string
}

// on reports whether the deploy runs in source mode.
func (s sourceModeChoice) on() bool {
	return s.image != ""
}

// localSourceMode is the source mode choice of the local layers alone, a
// target and the workspace defaults, read before the server is asked
// anything. It is the zero value when they name no runtime image or when the
// target cannot be resolved (the deploy reports that itself).
func localSourceMode(cmd *cobra.Command, dir string) sourceModeChoice {
	layers, err := defaultsLayers(cmd, dir, true)
	if err != nil {
		return sourceModeChoice{}
	}
	return pickSourceMode(layers)
}

// check refuses a source mode deploy that cannot work: a runtime image not
// pinned by a full digest, which no server runs in source mode, or a DAG
// that needs a build (needs, from sourceModeNeeds).
func (s sourceModeChoice) check(dir string, needs []string) error {
	if !s.on() {
		return nil
	}
	if !domain.IsDigestPinned(s.image) {
		return fmt.Errorf("source_mode_image %q (from %s) is not pinned by a full digest (image@sha256:<64 hex>); "+
			"source mode runs only the server's execution.source_mode.image, which always is: copy it byte for byte", s.image, s.source)
	}
	if len(needs) == 0 {
		return nil
	}
	return fmt.Errorf("source mode (source_mode_image from %s) registers dag.py alone on the runtime image, with no build, "+
		"but %s needs a build:\n  - %s\n"+
		"  Remove what needs the build, or deploy to a target without source_mode_image to build an image",
		s.source, dir, strings.Join(needs, "\n  - "))
}

// sourceModeNeeds lists what in the project at dir needs an image build,
// which source mode cannot give it: packages to install, build steps, files
// the task would need besides dag.py. cfg is the project's own config, read
// before any default fills it in.
func sourceModeNeeds(dir string, cfg *domain.LeoflowConfig) []string {
	needs := configNeeds(cfg)
	needs = append(needs, includeNeeds(dir, cfg)...)
	return append(needs, fileNeeds(dir, cfg)...)
}

// includeNeeds lists the include_paths entries a build would copy into the
// image besides the DAG source (extraIncludePaths; the default "." copies
// nothing more).
func includeNeeds(dir string, cfg *domain.LeoflowConfig) []string {
	extra, err := extraIncludePaths(cfg, map[string]bool{filepath.Base(dagSourcePath(dir, cfg)): true})
	if err != nil {
		return []string{fmt.Sprintf("include_paths in dexaflow.yaml (%v)", err)}
	}
	if len(extra) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("include_paths in dexaflow.yaml (%s, files to copy into the image)", strings.Join(extra, ", "))}
}

// configNeeds lists the dexaflow.yaml keys that need an image build.
func configNeeds(cfg *domain.LeoflowConfig) []string {
	var needs []string
	keys := []struct {
		set  bool
		what string
	}{
		{len(cfg.Dependencies) > 0, "dependencies in dexaflow.yaml (packages to install)"},
		{len(cfg.Connectors) > 0, "connectors in dexaflow.yaml (provider packages to install)"},
		{len(cfg.SystemPackages) > 0, "system_packages in dexaflow.yaml (packages to install)"},
		{cfg.BaseImage != "", "base_image in dexaflow.yaml (the image to build on)"},
		{cfg.Build != nil && cfg.Build.Dockerfile != "", "build.dockerfile in dexaflow.yaml (a Dockerfile to build)"},
		{cfg.Dbt != nil || len(cfg.DbtGroups) > 0, "a dbt project (dbt or dbt_groups in dexaflow.yaml)"},
	}
	for _, k := range keys {
		if k.set {
			needs = append(needs, k.what)
		}
	}
	return needs
}

// fileNeeds lists the files beside dag.py that need an image build: a
// requirements.txt, a Dockerfile, and other Python modules the task could
// import, which only an image would carry.
func fileNeeds(dir string, cfg *domain.LeoflowConfig) []string {
	var needs []string
	for _, f := range []struct{ name, why string }{
		{"requirements.txt", "packages to install"},
		{"Dockerfile", "a Dockerfile to build"},
	} {
		if fileExists(filepath.Join(dir, f.name)) {
			needs = append(needs, fmt.Sprintf("%s (%s)", f.name, f.why))
		}
	}
	if mods := siblingModules(dir, filepath.Base(dagSourcePath(dir, cfg))); len(mods) > 0 {
		needs = append(needs, fmt.Sprintf("%s beside the DAG source (only dag.py reaches the task)", strings.Join(mods, ", ")))
	}
	return needs
}

// siblingModules lists, sorted, the Python files in dir other than source.
func siblingModules(dir, source string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var mods []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".py") && e.Name() != source {
			mods = append(mods, e.Name())
		}
	}
	sort.Strings(mods)
	return mods
}

// checkSourceModeSpec refuses the compiled dag.json at output when the
// server would: in source mode its source must be present and at most
// domain.MaxSourceModeBytes (domain.CheckSourceMode, the check the register
// makes), on exactly the runtime image.
func checkSourceModeSpec(output, image string) error {
	data, err := os.ReadFile(output) //nolint:gosec // the compiled dag.json of the DAG directory
	if err != nil {
		return fmt.Errorf("reading %s: %w", output, err)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(data, &spec); jerr != nil {
		return fmt.Errorf("parsing %s: %w", output, jerr)
	}
	if spec.Image != image {
		return fmt.Errorf("source mode: %s names image %q, not the runtime image %q", output, spec.Image, image)
	}
	if cerr := domain.CheckSourceMode(image, &spec); cerr != nil {
		return fmt.Errorf("%w (dexaflow deploy compiled %s)", cerr, output)
	}
	return nil
}
