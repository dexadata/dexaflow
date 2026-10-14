package cli

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
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

// sourceModeNone is the source_mode_image that clears the one a less
// specific layer, or the cluster, sets: the deploy builds.
const sourceModeNone = "none"

// noSourceModeFlag is the deploy and status flag that turns source mode off
// for one command, whatever sets source_mode_image.
const noSourceModeFlag = "no-source-mode"

// sourceModeChoice is the runtime image of a source mode deploy and where it
// came from; the zero value is a deploy that builds.
type sourceModeChoice struct {
	image  string
	source string
	// cleared is a layer's source_mode_image: none (pickSourceMode).
	cleared bool
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
	choice, _ := resolveSourceMode(cmd, layers, nil, "") //nolint:errcheck // with no cluster there is nothing to disagree with.
	return choice
}

// resolveSourceMode settles the runtime image of a deploy from the local
// layers (a target, then the workspace defaults), the cluster's deploy
// defaults (nil when none) and registrySource, where the registry url came
// from.
//
// --no-source-mode, or a local source_mode_image: none, turns source mode
// off. A local image wins, but one that differs from the image the cluster
// advertises is refused, naming both: the server runs a source mode version
// only on its own runtime image, so the deploy could never run. The
// cluster's image applies alone only when no local source chose a registry
// (#1704): a person who set one meant a build.
func resolveSourceMode(cmd *cobra.Command, local []defaultsLayer, cluster *defaultsLayer, registrySource string) (sourceModeChoice, error) {
	if off, _ := cmd.Flags().GetBool(noSourceModeFlag); off { //nolint:errcheck // only deploy and status have the flag.
		return sourceModeChoice{}, nil
	}
	choice := pickSourceMode(local)
	if choice.cleared {
		return sourceModeChoice{}, nil
	}
	var offer sourceModeChoice
	if cluster != nil {
		offer = pickSourceMode([]defaultsLayer{*cluster})
	}
	switch {
	case choice.on() && offer.on() && offer.image != choice.image:
		return sourceModeChoice{}, fmt.Errorf("source_mode_image %q (from %s) is not the runtime image %q of %s; "+
			"the server runs source mode only on its own execution.source_mode.image: set that image, "+
			"or source_mode_image: none (or --no-source-mode) to build",
			printable(choice.image), choice.source, printable(offer.image), offer.source)
	case choice.on():
		return choice, nil
	case offer.on() && (registrySource == "" || registrySource == cluster.name):
		return offer, nil
	}
	return sourceModeChoice{}, nil
}

// check refuses a source mode deploy that cannot work: a runtime image not
// pinned by a full digest, which no server runs in source mode, or a DAG
// that needs a build (needs, from scanSourceMode).
func (s sourceModeChoice) check(dir string, needs []string) error {
	if !s.on() {
		return nil
	}
	if err := checkSourceModeImage(s.image); err != nil {
		return fmt.Errorf("source_mode_image %q (from %s) %v; "+
			"source mode runs only the server's execution.source_mode.image: copy it byte for byte", printable(s.image), s.source, err)
	}
	if len(needs) == 0 {
		return nil
	}
	return fmt.Errorf("source mode (source_mode_image from %s) registers dag.py alone on the runtime image, with no build, "+
		"but %s needs a build:\n  - %s\n"+
		"  Remove what needs the build, or deploy to a target without source_mode_image to build an image",
		s.source, dir, strings.Join(needs, "\n  - "))
}

// checkFlags refuses --dockerfile in source mode, which builds nothing, and,
// when notify is set (not on a watch's redeploys), notes on stderr each build
// flag it ignores (--registry, --platform, --rebuild, --builder) and warns
// about the files a build would have copied (scan.extras).
func (s sourceModeChoice) checkFlags(cmd *cobra.Command, scan sourceModeScan, notify bool) error {
	if !s.on() {
		return nil
	}
	if cmd.Flags().Changed("dockerfile") {
		return fmt.Errorf("--dockerfile names a Dockerfile to build, but source mode (source_mode_image from %s) builds nothing: "+
			"drop --dockerfile, or pass --no-source-mode to build", s.source)
	}
	if !notify {
		return nil
	}
	warn := cmd.ErrOrStderr()
	for _, f := range []string{"registry", platformFlag, "rebuild", "builder"} {
		if cmd.Flags().Changed(f) {
			//nolint:errcheck // a note that cannot be delivered must not fail the deploy
			fmt.Fprintf(warn, "note: --%s changes nothing in source mode (source_mode_image from %s): nothing is built or pushed\n", f, s.source)
		}
	}
	if w := scan.warning(scan.source); w != "" {
		io.WriteString(warn, w) //nolint:errcheck,gosec // a warning that cannot be delivered must not fail the deploy
	}
	return nil
}

// checkSourceModeImage refuses a runtime image no server runs in source
// mode: one not pinned by a full digest, or not an image reference at all
// (domain.CheckImageReference, then the reference grammar, strictly).
func checkSourceModeImage(image string) error {
	if !domain.IsDigestPinned(image) {
		return errors.New("is not pinned by a full digest (image@sha256:<64 hex>)")
	}
	if domain.CheckImageReference("source_mode_image", image) != nil {
		return errors.New("is not an image reference: it holds whitespace, a control character or a character no reference can")
	}
	ref, err := name.ParseReference(image, name.StrictValidation)
	if err != nil {
		return fmt.Errorf("is not an image reference (%s)", printable(err.Error()))
	}
	if _, ok := ref.(name.Digest); !ok {
		return errors.New("is not an image reference pinned by a digest")
	}
	return nil
}

// sourceModeScan is what source mode makes of a project: needs, what needs
// an image build, which source mode refuses; extras, the other files a build
// would have copied into the image, which the task will not see, and which
// it only warns about.
type sourceModeScan struct {
	needs  []string
	extras []string
	// source is the DAG source's project-relative path, for the warning.
	source string
}

// scanSourceMode scans the project at dir for a source mode deploy:
// packages to install, build steps, and the files the task would need
// besides the DAG source. cfg is the project's own config, read before any
// default fills it in.
func scanSourceMode(dir string, cfg *domain.LeoflowConfig) sourceModeScan {
	needs := configNeeds(cfg)
	needs = append(needs, includeNeeds(cfg)...)
	needs = append(needs, fileNeeds(dir)...)
	files, err := projectFiles(dir, cfg)
	if err != nil {
		return sourceModeScan{needs: append(needs, fmt.Sprintf("the project's files (%v)", err))}
	}
	modules, extras := classifyProjectFiles(dir, cfg, files)
	return sourceModeScan{needs: append(needs, modules...), extras: extras, source: filepath.ToSlash(filepath.Clean(cfg.DagSource))}
}

// warning words the extras of a source mode deploy of the DAG source
// named source, "" when there are none.
func (s sourceModeScan) warning(source string) string {
	if len(s.extras) == 0 {
		return ""
	}
	shown, more := s.extras, ""
	if len(shown) > maxExtrasShown {
		shown, more = shown[:maxExtrasShown], fmt.Sprintf(" and %d more", len(s.extras)-maxExtrasShown)
	}
	return fmt.Sprintf("warning: source mode registers %s alone; a build would also have copied %s%s, "+
		"which the task will not see (add them to exclude_paths to silence this)\n", source, strings.Join(shown, ", "), more)
}

// maxExtrasShown caps the files the source mode warning names.
const maxExtrasShown = 10

// includeNeeds lists the include_paths entries a build would copy into the
// image besides the DAG source (extraIncludePaths, the DAG source being its
// cleaned project-relative path; the default "." copies nothing more).
func includeNeeds(cfg *domain.LeoflowConfig) []string {
	extra, err := extraIncludePaths(cfg, map[string]bool{filepath.Clean(cfg.DagSource): true})
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
		{cfg.Build != nil && cfg.Build.Context != "" && filepath.Clean(cfg.Build.Context) != ".", "build.context in dexaflow.yaml (a build context to copy from)"},
		{cfg.Dbt != nil || len(cfg.DbtGroups) > 0, "a dbt project (dbt or dbt_groups in dexaflow.yaml)"},
	}
	for _, k := range keys {
		if k.set {
			needs = append(needs, k.what)
		}
	}
	return needs
}

// fileNeeds lists the build files at the top of the project: a
// requirements.txt and a Dockerfile.
func fileNeeds(dir string) []string {
	var needs []string
	for _, f := range []struct{ name, why string }{
		{"requirements.txt", "packages to install"},
		{"Dockerfile", "a Dockerfile to build"},
	} {
		if fileExists(filepath.Join(dir, f.name)) {
			needs = append(needs, fmt.Sprintf("%s (%s)", f.name, f.why))
		}
	}
	return needs
}

// projectFiles lists, as sorted slash paths relative to dir, the files of
// the project the label walker keeps (exclude_paths, the .dockerignore and
// its other skip rules honored), without reading them.
func projectFiles(dir string, cfg *domain.LeoflowConfig) ([]string, error) {
	w, err := newContentWalker(dir, cfg, deployOutput(dir), false, sha256.New())
	if err != nil {
		return nil, err
	}
	var files []string
	w.list = true
	w.seen = func(file string) {
		if rel, rerr := filepath.Rel(w.realDir, file); rerr == nil {
			files = append(files, filepath.ToSlash(rel))
		}
	}
	if werr := w.hashRoot(hashRootSpec{name: ".", inside: true}); werr != nil {
		return nil, werr
	}
	sort.Strings(files)
	return files, nil
}

// classifyProjectFiles splits files (projectFiles) into the Python code a
// task could import, which only an image would carry (modules: other
// top-level modules and packages, each a need), and the other files a build
// would have copied (extras). The DAG source, the project's config, the
// build files fileNeeds names, Python test files and an empty top-level
// __init__.py are neither.
func classifyProjectFiles(dir string, cfg *domain.LeoflowConfig, files []string) (modules, extras []string) {
	source := filepath.ToSlash(filepath.Clean(cfg.DagSource))
	var mods, pkgs []string
	seenPkg := map[string]bool{}
	for _, f := range files {
		top, isPy := !strings.Contains(f, "/"), strings.HasSuffix(f, ".py")
		switch {
		case f == source, top && sourceModeKnownFiles[f]:
		case isPy && pythonTestFile(path.Base(f)):
		case f == "__init__.py" && emptyFile(filepath.Join(dir, f)):
		case isPy && top:
			mods = append(mods, f)
		case isPy:
			dirName, _, _ := strings.Cut(f, "/")
			if pkg := dirName + "/"; !seenPkg[pkg] {
				seenPkg[pkg] = true
				pkgs = append(pkgs, pkg)
			}
		default:
			extras = append(extras, f)
		}
	}
	if len(mods) > 0 {
		modules = append(modules, fmt.Sprintf("%s beside the DAG source (only dag.py reaches the task)", strings.Join(mods, ", ")))
	}
	if len(pkgs) > 0 {
		modules = append(modules, fmt.Sprintf("%s (Python packages; only dag.py reaches the task)", strings.Join(pkgs, ", ")))
	}
	return modules, extras
}

// sourceModeKnownFiles are the top-level files source mode accounts for
// itself: the project's config, and the build files fileNeeds refuses.
var sourceModeKnownFiles = map[string]bool{
	projectConfigFile: true, legacyProjectConfigFile: true,
	"requirements.txt": true, "Dockerfile": true,
	".gitignore": true, ".gitattributes": true,
}

// pythonTestFile reports whether base names a pytest file: test_*.py,
// *_test.py or conftest.py, which the task never imports.
func pythonTestFile(base string) bool {
	return base == "conftest.py" || strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")
}

// emptyFile reports whether file is a file with nothing in it but
// whitespace.
func emptyFile(file string) bool {
	data, err := os.ReadFile(file) //nolint:gosec // a file of the project being deployed
	return err == nil && strings.TrimSpace(string(data)) == ""
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
