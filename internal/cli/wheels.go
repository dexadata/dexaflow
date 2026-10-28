package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// wheelKeyFormat versions the dependency key of an image built from wheels
// (ADR 0078). It differs from depsKeyFormat, so a dependency image built
// from wheels and one the daemon built never share a tag. Bump it whenever
// the layout of the dependency layer changes (where files land, their
// modes, the script and RECORD rewrites, the image config), so no image of
// the old layout is reused.
const wheelKeyFormat = 2

// manylinuxTags are the glibc minors uv knows a manylinux tag for, newest
// first, from the ceiling the dependency layer asks wheels for: Debian 12
// ships glibc 2.36 and Debian 13 (the runtime base today) 2.41, so a wheel
// tagged up to manylinux_2_36 runs on both.
var manylinuxTags = []int{36, 35, 34, 33, 32, 31, 28, 17}

// wheelArch maps a platform to the machine of its wheel tags.
var wheelArch = map[string]string{"linux/amd64": "x86_64", "linux/arm64": "aarch64", "linux/arm64/v8": "aarch64"}

// slowResolution is how long a resolution may take before the builder line
// says how long it took.
const slowResolution = time.Second

// plainRequirement matches a requirement by name: extras, a version
// specifier and an environment marker allowed, no URL, path or option.
var plainRequirement = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\s*(\[[A-Za-z0-9._,\s-]*\])?\s*[^@#\\]*$`)

// inlineOption matches an option after a requirement (` --hash`, ` -e`),
// which a requirements file would read and pip on the command line would
// not.
var inlineOption = regexp.MustCompile(`\s-`)

// wheelPlatform is the resolution of one platform.
type wheelPlatform struct {
	// platform is os/arch[/variant], lower case.
	platform string
	// tag is the uv platform of the wheels, x86_64-manylinux_2_36.
	tag string
	// base is the platform image of the runtime base.
	base build.BaseImage
	// resolved is the resolution, sorted by name.
	resolved []resolvedDist
}

// wheelPlan is how the dependency image of a split is built from wheels.
type wheelPlan struct {
	uv *uvTool
	// reader reads the base image with the project's credentials.
	reader    baseReader
	base      build.Pinned
	python    string
	platforms []wheelPlatform
	env       []string
	user      string
	took      time.Duration
}

// planWheels decides whether the dependency image can be built from wheels
// without a daemon (ADR 0078) and resolves the dependencies for every
// platform when it can; otherwise it returns nil and why not, for the
// builder line.
func planWheels(ctx context.Context, u *uvTool, cfg *domain.LeoflowConfig, deps []string, reader baseReader, base build.Pinned, platforms []string) (plan *wheelPlan, why string) {
	if why := wheelBlocker(ctx, cfg, deps, platforms); why != "" {
		return nil, why
	}
	if err := u.findPython(ctx); err != nil {
		return nil, fmt.Sprintf("uv finds no Python on this machine, which it needs even for another version (%v)", err)
	}
	p := &wheelPlan{uv: u, reader: reader, base: base}
	if cfg.Dbt == nil {
		p.env = []string{"PYTHONPATH=" + build.Workdir}
	}
	if len(deps) > 0 {
		p.user = "65532:65532"
	}
	sorted := normalizedPlatforms(platforms)
	start := time.Now()
	for _, plat := range sorted {
		wp, why := p.resolve(ctx, cfg, deps, plat)
		if why != "" {
			return nil, why
		}
		p.platforms = append(p.platforms, wp)
	}
	p.took = time.Since(start)
	return p, ""
}

// wheelBlocker says why a project cannot take the dependency layer before
// anything is read: a platform with no wheel tag, system packages, a
// dependency that is not a plain requirement by name. The operating system
// is cliDeps.hostOS, so the tests of this package can stand on another one.
func wheelBlocker(ctx context.Context, cfg *domain.LeoflowConfig, deps []string, platforms []string) string {
	return wheelBlockerOn(depsFrom(ctx).hostOS, cfg, deps, platforms)
}

// wheelBlockerOn is wheelBlocker for a CLI running on goos, such as the
// Linux runner of a pipeline that `ci init` writes from another machine.
func wheelBlockerOn(goos string, cfg *domain.LeoflowConfig, deps []string, platforms []string) string {
	if goos == "windows" {
		return "the dependency layer does not run on Windows yet"
	}
	for _, p := range platforms {
		if wheelArch[strings.ToLower(p)] == "" {
			return fmt.Sprintf("the platform %s has no wheel tag the dependency layer knows (linux/amd64, linux/arm64)", p)
		}
	}
	if len(cfg.SystemPackages) > 0 {
		return "system_packages need a real build"
	}
	for _, d := range deps {
		d = strings.TrimSpace(d)
		switch {
		case strings.Contains(d, "://") || strings.Contains(d, "@") || strings.HasPrefix(d, "git+") ||
			strings.HasPrefix(d, ".") || strings.HasPrefix(d, "/") || strings.HasPrefix(d, "file:"):
			return fmt.Sprintf("the dependency %q is a URL, a path or a VCS reference, which the dependency layer does not install yet", d)
		case !plainRequirement.MatchString(d) || inlineOption.MatchString(d):
			return fmt.Sprintf("the dependency %q is not a requirement by name (name, extras, version, marker)", d)
		}
	}
	return ""
}

// normalizedPlatforms lower-cases and sorts platforms, without repeats.
func normalizedPlatforms(platforms []string) []string {
	out := make([]string, 0, len(platforms))
	for _, p := range platforms {
		out = append(out, strings.ToLower(p))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// resolve reads the base image for one platform, checks that it is a
// Dexaflow runtime base, and resolves the dependencies for it.
func (p *wheelPlan) resolve(ctx context.Context, cfg *domain.LeoflowConfig, deps []string, platform string) (wp wheelPlatform, why string) {
	bi, err := p.reader.Base(ctx, p.base, platform)
	if err != nil {
		return wheelPlatform{}, fmt.Sprintf("the base image could not be read for %s (%v)", platform, unwrapped(err))
	}
	if why = runtimeBaseBlocker(cfg, bi); why != "" {
		return wheelPlatform{}, why
	}
	if p.python == "" {
		p.python = bi.Python
	} else if p.python != bi.Python {
		return wheelPlatform{}, fmt.Sprintf("the base image has Python %s for one platform and %s for %s", p.python, bi.Python, platform)
	}
	tag, why := manylinuxTag(bi.Glibc)
	if why != "" {
		return wheelPlatform{}, why
	}
	wp = wheelPlatform{platform: platform, tag: wheelArch[platform] + "-" + tag, base: bi}
	if len(deps) == 0 {
		return wp, ""
	}
	wp.resolved, err = p.uv.compile(ctx, wp.tag, bi.Python, bi.Dists, deps)
	if err != nil {
		return wheelPlatform{}, fmt.Sprintf("uv could not resolve the dependencies for %s: %v", platform, err)
	}
	return wp, ""
}

// runtimeBaseBlocker says why bi is not a runtime base the dependency layer
// can install into, or "".
func runtimeBaseBlocker(cfg *domain.LeoflowConfig, bi build.BaseImage) string {
	if bi.Python == "" || !bi.SitePackages {
		return "the base image is not a Dexaflow runtime base (its config sets no PYTHON_VERSION, or it has no " +
			"/usr/local/lib/pythonX.Y/site-packages)"
	}
	if len(bi.PipConfig) > 0 {
		return fmt.Sprintf("the base image configures pip (%s), which the daemon's pip honors and the dependency layer cannot",
			strings.Join(bi.PipConfig, ", "))
	}
	if !cfg.PythonVersionDefaulted && cfg.PythonVersion != "" && cfg.PythonVersion != bi.MinorPython() {
		return fmt.Sprintf("python_version %s in dexaflow.yaml does not match Python %s of the base image", cfg.PythonVersion, bi.Python)
	}
	return ""
}

// manylinuxTag is the newest manylinux tag at or under the ceiling that a
// base with glibc X.Y runs, or "" and why there is none.
func manylinuxTag(glibc string) (tag, why string) {
	major, minor, ok := strings.Cut(glibc, ".")
	m, err := strconv.Atoi(minor)
	if !ok || major != "2" || err != nil {
		return "", "the base image records no glibc version (no libc6 in var/lib/dpkg/status), so manylinux wheels may not run on it"
	}
	for _, t := range manylinuxTags {
		if t <= m {
			return "manylinux_2_" + strconv.Itoa(t), ""
		}
	}
	return "", fmt.Sprintf("the base image has glibc %s, older than any manylinux tag uv knows", glibc)
}

// key is the dependency key of the plan (ADR 0078): the SHA-256 of a
// canonical document holding the key format, the installer and its
// version, the base image digest, the Python version, every platform with
// its wheel tag and resolution (pins and hashes), and the image
// configuration the dependency image sets.
func (p *wheelPlan) key() string {
	type platformDoc struct {
		Platform string   `json:"platform"`
		Tag      string   `json:"wheel_tag"`
		Resolved []string `json:"resolved"`
	}
	doc := struct {
		Format    int           `json:"format"`
		Installer string        `json:"installer"`
		Version   string        `json:"installer_version"`
		Base      string        `json:"base"`
		Python    string        `json:"python"`
		Platforms []platformDoc `json:"platforms"`
		Env       []string      `json:"env"`
		User      string        `json:"user"`
	}{Format: wheelKeyFormat, Installer: "uv", Version: p.uv.version, Base: p.base.Ref[strings.LastIndex(p.base.Ref, "@")+1:],
		Python: p.python, Env: p.env, User: p.user}
	for _, wp := range p.platforms {
		pd := platformDoc{Platform: wp.platform, Tag: wp.tag, Resolved: []string{}}
		// parseResolved sorted the resolution by name.
		for _, d := range wp.resolved {
			pd.Resolved = append(pd.Resolved, d.line())
		}
		doc.Platforms = append(doc.Platforms, pd)
	}
	slices.SortFunc(doc.Platforms, func(a, b platformDoc) int { return strings.Compare(a.Platform, b.Platform) })
	data, _ := json.Marshal(doc) //nolint:errcheck // strings and ints cannot fail to marshal
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// layer is the build.DependencyLayer of the plan.
func (p *wheelPlan) layer() *build.DependencyLayer {
	minor := build.BaseImage{Python: p.python}.MinorPython()
	return &build.DependencyLayer{Base: p.base, Python: minor, Env: p.env, User: p.user, Installer: wheelInstaller{plan: p}}
}

// note is what the builder line adds about the resolution: how long it
// took, when that is long enough to notice.
func (p *wheelPlan) note() string {
	if p.took < slowResolution {
		return ""
	}
	return fmt.Sprintf("uv resolved the dependencies in %.1fs", p.took.Seconds())
}

// wheelInstaller installs the resolution of a platform with uv, minus what
// the base image already holds at the same version.
type wheelInstaller struct{ plan *wheelPlan }

// Describe names uv and its version.
func (w wheelInstaller) Describe() string { return w.plan.uv.version }

// Install installs the resolution of platform into dir.
func (w wheelInstaller) Install(ctx context.Context, platform, dir string) error {
	for _, wp := range w.plan.platforms {
		if !strings.EqualFold(wp.platform, platform) {
			continue
		}
		lines := toInstall(wp)
		if len(lines) == 0 {
			return nil
		}
		return w.plan.uv.install(ctx, wp.tag, w.plan.python, lines, dir)
	}
	return fmt.Errorf("no dependencies were resolved for %s", platform)
}

// toInstall is the resolution of wp without the distributions the base
// image holds at the same version, as hash-pinned requirement lines.
func toInstall(wp wheelPlatform) []string {
	inBase := map[string]bool{}
	for _, d := range wp.base.Dists {
		inBase[d] = true
	}
	var lines []string
	for _, d := range wp.resolved {
		if !inBase[d.pin()] {
			lines = append(lines, d.line())
		}
	}
	return lines
}
