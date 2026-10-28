package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// config is one measurement.
type config struct {
	// cli is the dexaflow binary to drive.
	cli string
	// workDir holds the scaffolded project, under dagID.
	workDir string
	// project is an existing project to deploy instead of a scaffold.
	project string
	// dagID names the scaffolded DAG and its image.
	dagID string
	// registry is the registry prefix the image is pushed to.
	registry     string
	platform     string
	python       string
	baseImage    string
	dependencies []string
	// n is the number of measured deploys after the discarded cold one.
	n int
	// target is the bound the p50 of the measured deploys must stay under.
	target time.Duration
	// server, token and cliConfig are passed to deploy when set.
	server, token, cliConfig string
	// deployArgs are extra deploy flags, passed as given.
	deployArgs []string
	// warm measures the warm-up of each deployed version's pool.
	warm bool
	// namespace holds the warm worker pods.
	namespace string
	// warmTarget is the bound the p50 of the warm-ups must stay under.
	warmTarget time.Duration
	// requests counts the requests the registry served, when it is the
	// local one; nil otherwise.
	requests func() int64
	// stamp makes this measurement's changes unique, so a rerun against the
	// same server registers new versions.
	stamp string
}

// watchFunc waits for the first Ready warm worker of image, timed from start.
type watchFunc func(ctx context.Context, image string, start time.Time) (warmReading, error)

// deployReading is one timed deploy.
type deployReading struct {
	wall    time.Duration
	builder string
	// requests is the number of registry requests the deploy made, or -1
	// when the registry does not count them.
	requests int64
	outcome  deployOutcome
	warm     *warmReading
}

// report is the result of a measurement.
type report struct {
	cold    deployReading
	deploys []deployReading
}

// measure prepares the project, runs one cold deploy it discards (it builds
// or finds the dependency image), then times cfg.n deploys that each change
// only dag.py, and prints a table of them to out. With watch set, it waits
// after each deploy for the first Ready warm worker of the new version.
func measure(ctx context.Context, cfg config, out io.Writer, watch watchFunc) (report, error) {
	dir, err := prepareProject(ctx, cfg)
	if err != nil {
		return report{}, err
	}
	var rep report
	for i := 0; i <= cfg.n; i++ {
		r, derr := deployOnce(ctx, cfg, dir, i, watch)
		if derr != nil {
			return rep, fmt.Errorf("deploy %d: %w", i, derr)
		}
		if i == 0 {
			rep.cold = r
		} else {
			rep.deploys = append(rep.deploys, r)
		}
		if perr := printRow(out, i, r); perr != nil {
			return rep, perr
		}
	}
	return rep, printSummary(out, cfg, rep)
}

// prepareProject returns the project directory to deploy: cfg.project, or
// a project scaffolded with `dexaflow init` (the path a new user takes) and
// given a dexaflow.yaml that pushes to cfg.registry.
func prepareProject(ctx context.Context, cfg config) (string, error) {
	if cfg.project != "" {
		return filepath.Abs(cfg.project)
	}
	dir, err := filepath.Abs(filepath.Join(cfg.workDir, cfg.dagID))
	if err != nil {
		return "", fmt.Errorf("resolving the project directory: %w", err)
	}
	if _, serr := os.Stat(dir); serr == nil {
		return "", fmt.Errorf("%s exists; give an empty --workdir", dir)
	}
	cmd := exec.CommandContext(ctx, cfg.cli, "init", dir) //nolint:gosec // the operator's own binary and directory
	if b, ierr := cmd.CombinedOutput(); ierr != nil {
		return "", fmt.Errorf("dexaflow init: %w\n%s", ierr, b)
	}
	// init names the DAG after the directory; the scaffold's dag.py stays.
	if werr := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte(projectYAML(cfg)), 0o600); werr != nil {
		return "", fmt.Errorf("writing dexaflow.yaml: %w", werr)
	}
	return dir, nil
}

// projectYAML is the dexaflow.yaml of the scaffolded project.
func projectYAML(cfg config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "schema_version: \"1.0\"\ndag_id: %s\npython_version: %q\n", cfg.dagID, cfg.python)
	if cfg.baseImage != "" {
		fmt.Fprintf(&b, "base_image: %s\n", cfg.baseImage)
	}
	if len(cfg.dependencies) == 0 {
		b.WriteString("dependencies: []\n")
	} else {
		b.WriteString("dependencies:\n")
		for _, d := range cfg.dependencies {
			fmt.Fprintf(&b, "  - %s\n", d)
		}
	}
	fmt.Fprintf(&b, "registry:\n  url: %s\n  image_name: %s\nbuild:\n  platforms:\n    - %s\n", cfg.registry, cfg.dagID, cfg.platform)
	if cfg.warm {
		// Prewarm the current version (ADR 0082), so its pool starts with
		// the register and not with its first run.
		b.WriteString("min_idle_workers: 1\n")
	}
	return b.String()
}

// deployArgs are the arguments of one deploy of dir.
func deployArgs(cfg config, dir string) []string {
	args := []string{"deploy", dir, "--yes", "--output", "json"}
	for _, f := range [][2]string{{"--server", cfg.server}, {"--token", cfg.token}, {"--config", cfg.cliConfig}} {
		if f[1] != "" {
			args = append(args, f[0], f[1])
		}
	}
	if cfg.warm {
		// Only an unpaused DAG is prewarmed.
		args = append(args, "--active")
	}
	return append(args, cfg.deployArgs...)
}

// deployOnce makes deploy i's code-only change, deploys, and times it from
// the start of the command to its exit, then waits for the warm-up.
func deployOnce(ctx context.Context, cfg config, dir string, i int, watch watchFunc) (deployReading, error) {
	dagPy := filepath.Join(dir, "dag.py")
	src, err := os.ReadFile(dagPy) //nolint:gosec // the project being measured
	if err != nil {
		return deployReading{}, fmt.Errorf("reading dag.py: %w", err)
	}
	if werr := os.WriteFile(dagPy, codeOnlyEdit(src, cfg.stamp+"-"+strconv.Itoa(i)), 0o600); werr != nil {
		return deployReading{}, fmt.Errorf("writing dag.py: %w", werr)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, cfg.cli, deployArgs(cfg, dir)...) //nolint:gosec // the operator's own binary
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	before := int64(0)
	if cfg.requests != nil {
		before = cfg.requests()
	}
	start := time.Now()
	runErr := cmd.Run()
	r := deployReading{wall: time.Since(start), requests: -1}
	if cfg.requests != nil {
		r.requests = cfg.requests() - before
	}
	r.builder, _ = builderLine(stderr.String())
	outcome, perr := parseDeploy(stdout.Bytes())
	if runErr != nil || perr != nil {
		return r, fmt.Errorf("dexaflow deploy: %w\n%s", errors.Join(runErr, perr), tail(stderr.String(), 20))
	}
	r.outcome = outcome
	if watch != nil {
		w, werr := watch(ctx, outcome.Image, start.Add(r.wall))
		if werr != nil {
			return r, werr
		}
		r.warm = &w
	}
	return r, nil
}

// tail is the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// verdict decides the measurement (see the package verdict), and with warm
// readings also that their p50 to Ready is under cfg.warmTarget.
func (r report) verdict(cfg config) (ok bool, why string) {
	walls := make([]time.Duration, 0, len(r.deploys))
	var offPath []string
	for i, d := range r.deploys {
		walls = append(walls, d.wall)
		if !codeOnly(d.builder) {
			offPath = append(offPath, fmt.Sprintf("deploy %d: %s", i+1, d.builder))
		}
	}
	ok, why = verdict(summarize(walls), cfg.target, offPath)
	if !ok || !cfg.warm {
		return ok, why
	}
	ready := summarize(r.readies())
	if ready.N != len(r.deploys) || ready.P50 >= cfg.warmTarget {
		return false, fmt.Sprintf("%s, but the warm-up p50 %s is not under the %s target", why, ready.P50.Round(10*time.Millisecond), cfg.warmTarget)
	}
	return true, fmt.Sprintf("%s, and the warm-up p50 %s is under the %s target", why, ready.P50.Round(10*time.Millisecond), cfg.warmTarget)
}

// readies are the times to Ready of the measured deploys' warm workers.
func (r report) readies() []time.Duration {
	var ds []time.Duration
	for _, d := range r.deploys {
		if d.warm != nil {
			ds = append(ds, d.warm.Ready)
		}
	}
	return ds
}

// printRow prints deploy i as a row of the table, after its header.
func printRow(out io.Writer, i int, r deployReading) error {
	var b strings.Builder
	label := strconv.Itoa(i)
	if i == 0 {
		b.WriteString("| # | Deploy | Builder | Registry requests | Warm worker seen | Warm worker Ready | Image pull |\n")
		b.WriteString("|---|---:|---|---:|---:|---:|---:|\n")
		label = "cold"
	}
	seen, ready, pull := "", "", ""
	if r.warm != nil {
		seen, ready = secs(r.warm.Seen), secs(r.warm.Ready)
		if r.warm.PullKnown {
			pull = secs(r.warm.Pull)
		}
	}
	reqs := ""
	if r.requests >= 0 {
		reqs = strconv.FormatInt(r.requests, 10)
	}
	fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n", label, secs(r.wall), builderPath(r.builder), reqs, seen, ready, pull)
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("printing deploy %d: %w", i, err)
	}
	return nil
}

// printSummary prints the percentiles and the verdict.
func printSummary(out io.Writer, cfg config, rep report) error {
	walls := make([]time.Duration, 0, len(rep.deploys))
	for _, d := range rep.deploys {
		walls = append(walls, d.wall)
	}
	st := summarize(walls)
	var b strings.Builder
	fmt.Fprintf(&b, "\ncode-only deploy: n=%d p50=%s p95=%s max=%s (cold deploy %s)\n", st.N, secs(st.P50), secs(st.P95), secs(st.Max), secs(rep.cold.wall))
	if cfg.warm {
		w := summarize(rep.readies())
		fmt.Fprintf(&b, "warm worker Ready after the deploy: n=%d p50=%s p95=%s max=%s\n", w.N, secs(w.P50), secs(w.P95), secs(w.Max))
	}
	ok, why := rep.verdict(cfg)
	result := "PASS"
	if !ok {
		result = "FAIL"
	}
	fmt.Fprintf(&b, "%s: %s\n", result, why)
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("printing the summary: %w", err)
	}
	return nil
}

// secs formats d in seconds with two decimals.
func secs(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 2, 64) + " s"
}
