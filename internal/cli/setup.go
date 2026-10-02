package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	leoflow "github.com/dexadata/dexaflow"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/setup"
)

// leoflowLogo is the wordmark shown atop `leoflow setup` on a terminal (figlet
// "standard"). It is plain ASCII so it renders anywhere; printed only with color
// (TTY), so piped/CI output stays clean.
const leoflowLogo = ` _                __ _
| |    ___  ___  / _| | _____      __
| |   / _ \/ _ \| |_| |/ _ \ \ /\ / /
| |__|  __/ (_) |  _| | (_) \ V  V /
|_____\___|\___/|_| |_|\___/ \_/\_/`

// liteSettings are the Lite-edition choices the setup wizard gathers.
type liteSettings struct {
	Workspace  string
	Executor   string // "subprocess" (local) or "k8s" (mini-cluster)
	AdminEmail string
	Port       int
}

// setupManifest records what `leoflow setup` provisioned, so later runs and
// `leoflow doctor` can report the managed state.
type setupManifest struct {
	Python     string    `json:"python"`
	Workspace  string    `json:"workspace"`
	Tier       string    `json:"tier"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	Executor   string    `json:"executor,omitempty"`
	Port       int       `json:"port,omitempty"`
	AdminEmail string    `json:"admin_email,omitempty"`
	ParserCmd  string    `json:"parser_cmd,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// promptLine asks for a value with a default; an empty answer keeps the default.
// The label is highlighted and the default dimmed when color is enabled.
func promptLine(in *bufio.Reader, out io.Writer, p palette, label, def string) string {
	_, _ = fmt.Fprintf(out, "  %s%s%s [%s%s%s]: ", p.cyan, label, p.reset, p.dim, def, p.reset) //nolint:errcheck // best-effort prompt
	line, _ := in.ReadString('\n')                                                              //nolint:errcheck // empty -> default
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

// gatherLiteConfig resolves the Lite settings: it returns the defaults verbatim
// when non-interactive (e.g. `curl | sh`), and otherwise prompts on the TTY,
// guiding the executor choice. It is pure (injected reader/writer) so it is
// unit-tested for both paths.
func gatherLiteConfig(interactive bool, in *bufio.Reader, out io.Writer, def liteSettings) liteSettings {
	if !interactive {
		return def
	}
	p := newPalette(colorEnabled(out))
	_, _ = fmt.Fprintf(out, "\n%sLeoflow Lite setup%s — press Enter to accept each [default].\n", p.bold, p.reset) //nolint:errcheck // best-effort
	s := def
	s.Workspace = promptLine(in, out, p, "Where should your DAGs live (workspace)", def.Workspace)
	// Run mode, in plain language — not the internal "subprocess|k8s" jargon a
	// first-timer can't answer. The named choices map to the executor below.
	_, _ = fmt.Fprintf(out, "\n  %sHow should tasks run?%s\n", p.bold, p.reset)                                                                                                           //nolint:errcheck // best-effort
	_, _ = fmt.Fprintf(out, "    %s1)%s local    — %srecommended for everyday use%s: each task runs as a process here; simple, no Docker\n", p.cyan, p.reset, p.green, p.reset)           //nolint:errcheck // best-effort
	_, _ = fmt.Fprintf(out, "    %s2)%s cluster  — for a development environment that mirrors Production: real pod-per-task on a mini-Kubernetes (k3d); needs Docker\n", p.cyan, p.reset) //nolint:errcheck // best-effort
	for {
		choice := strings.ToLower(promptLine(in, out, p, "Run mode (1 or 2)", executorChoiceLabel(def.Executor)))
		if executor, ok := executorFromChoice(choice); ok {
			s.Executor = executor
			break
		}
		_, _ = fmt.Fprintln(out, "  please type 1 (local) or 2 (cluster)") //nolint:errcheck // best-effort
	}
	if port, err := strconv.Atoi(promptLine(in, out, p, "UI port", strconv.Itoa(def.Port))); err == nil && port > 0 {
		s.Port = port
	}
	s.AdminEmail = promptLine(in, out, p, "Admin email", def.AdminEmail)
	return s
}

// isInteractive reports whether f is a real terminal (so prompting makes sense).
// Using x/term distinguishes a TTY from a pipe (`curl | sh`) or /dev/null, which
// a ModeCharDevice check would wrongly treat as interactive.
func isInteractive(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// palette holds ANSI styles for the wizard; all fields are empty when color is
// disabled, so the same format strings render plain.
type palette struct{ bold, dim, green, cyan, reset string }

// colorEnabled reports whether to emit ANSI color: only to a real terminal, and
// never when NO_COLOR is set (https://no-color.org). Piped/CI output (a non-*os.File
// writer, or a redirected file) stays clean, so logs are never polluted.
func colorEnabled(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := out.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// newPalette returns ANSI styles when enabled, or empty strings otherwise.
func newPalette(enabled bool) palette {
	if !enabled {
		return palette{}
	}
	return palette{bold: "\x1b[1m", dim: "\x1b[2m", green: "\x1b[32m", cyan: "\x1b[36m", reset: "\x1b[0m"}
}

// executorChoiceLabel maps the executor to its menu number for the [default]
// prompt (so the default reads "1"/"2", matching the numbered menu).
func executorChoiceLabel(executor string) string {
	if executor == "k8s" {
		return "2"
	}
	return "1"
}

// executorFromChoice maps a run-mode answer to the internal executor, accepting
// the menu numbers (1/2), the friendly names (local/cluster), and the legacy raw
// values. Returns ok=false otherwise.
func executorFromChoice(choice string) (string, bool) {
	switch choice {
	case "1", "local", "subprocess":
		return "subprocess", true
	case "2", "cluster", "k8s", "kubernetes":
		return "k8s", true
	}
	return "", false
}

// newSetupCommand bootstraps the managed runtime: a usable Python 3.11 (the
// system one or a downloaded relocatable CPython), the ~/.dexaflow layout, and a
// workspace directory for the user's DAG projects.
func newSetupCommand() *cobra.Command {
	var workspace string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Bootstrap the managed Leoflow runtime (Python, parser, workspace).",
		Long: "setup prepares ~/.dexaflow: it ensures a Python 3.11 is available " +
			"(using a system interpreter if present, otherwise downloading a pinned, " +
			"checksum-verified relocatable CPython — no sudo, no system packages), " +
			"extracts the embedded parser and runtime sources, points the parser at the " +
			"interpreter (pure Python, no venv, no Airflow — ADR 0024), and creates a " +
			"workspace directory. Re-running is safe; --dry-run shows the plan.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd, workspace, dryRun)
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace dir for your DAG projects (default ~/leoflow)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "detect and print the plan without downloading or writing anything")
	return cmd
}

func runSetup(cmd *cobra.Command, workspaceFlag string, dryRun bool) error {
	out := cmd.OutOrStdout()
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	leoflowHome := stateDirIn(homeDir)

	r := setup.Detect(setup.Probe{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		LookPath: exec.LookPath, Stat: os.Stat, Getwd: os.Getwd,
	})

	def := liteSettings{Workspace: workspaceFlag, Executor: "subprocess", AdminEmail: "admin@leoflow.local", Port: 8088}
	if def.Workspace == "" {
		def.Workspace = filepath.Join(homeDir, "leoflow")
	}

	pal := newPalette(colorEnabled(out))
	if pal.cyan != "" { // only on a real terminal — keep CI/pipe output clean
		_, _ = fmt.Fprintf(out, "%s%s%s\n", pal.cyan, leoflowLogo, pal.reset) //nolint:errcheck // best-effort terminal output
	}
	_, _ = fmt.Fprintf(out, "leoflow setup\n\n  platform   %s/%s%s\n", r.OS, r.Arch, libcSuffix(r.Libc)) //nolint:errcheck // best-effort terminal output

	// Prompt only on first setup. On a re-run the config already exists and is not
	// rewritten, so re-asking would silently discard the answers — instead keep the
	// recorded settings (and point at reset-password to change the admin).
	firstRun := !liteConfigExists(leoflowHome)
	var lc liteSettings
	if firstRun {
		interactive := isInteractive(os.Stdin) && !dryRun
		lc = gatherLiteConfig(interactive, bufio.NewReader(os.Stdin), out, def)
	} else {
		lc = loadManifestSettings(leoflowHome, def)
		_, _ = fmt.Fprintln(out, "\n  already configured (~/.dexaflow/config.yaml) — keeping your settings.\n  change the admin with `leoflow lite reset-password`.") //nolint:errcheck // best-effort terminal output
	}

	_, _ = fmt.Fprintf(out, "\n  workspace  %s\n  executor   %s\n  port       %d\n  admin      %s\n", lc.Workspace, lc.Executor, lc.Port, lc.AdminEmail) //nolint:errcheck // best-effort terminal output
	switch {
	case r.PythonAvailable && r.Python311Path == "":
		// The case the old message got wrong: a host Python that can parse but is
		// not the one setup will use. Saying "using system 3.12" here and then
		// downloading is the gap #1224 reported, and a dry run that misdescribes
		// the real run is worse than no dry run.
		_, _ = fmt.Fprintf(out, "  python     %s can parse DAGs, but setup needs python3.11 specifically\n             and will download a managed CPython 3.11 under ~/.dexaflow/python (needs network)\n", r.PythonPath) //nolint:errcheck // best-effort terminal output
	case r.PythonAvailable:
		_, _ = fmt.Fprintf(out, "  python     using system %s\n", r.PythonPath) //nolint:errcheck // best-effort terminal output
	default:
		_, _ = fmt.Fprintln(out, "  python     no python3.11/3.12/3.13 on PATH; will install a relocatable CPython 3.11 under ~/.dexaflow/python") //nolint:errcheck // best-effort terminal output
	}

	if dryRun {
		_, _ = fmt.Fprintln(out, "\n  (dry run: nothing downloaded or written)") //nolint:errcheck // best-effort terminal output
		return nil
	}

	generated, perr := provisionLite(cmd, out, leoflowHome, r, lc)
	if perr != nil {
		return perr
	}

	if r.UnderMnt {
		_, _ = fmt.Fprintln(out, "\n  WARNING: under /mnt (WSL): keep DAG projects in the WSL native FS (~/...) for hot-reload.") //nolint:errcheck // best-effort terminal output
	}
	printSetupSummary(out, lc, generated)
	return nil
}

// provisionLite does the on-disk work: ensure Python, extract the parser/runtime
// sources, and (on first setup) generate the admin password, store only its hash,
// and write the config. It returns the generated plaintext password (empty on a
// re-run, where the existing config is left intact).
func provisionLite(cmd *cobra.Command, out io.Writer, leoflowHome string, r setup.Report, lc liteSettings) (string, error) {
	if mkErr := os.MkdirAll(leoflowHome, 0o750); mkErr != nil {
		return "", fmt.Errorf("creating %s: %w", leoflowHome, mkErr)
	}
	pyPath, pyErr := setup.EnsurePython(cmd.Context(), setup.EnsureOpts{
		Home: leoflowHome, GOOS: r.OS, GOARCH: r.Arch, Libc: r.Libc,
		LookPath: exec.LookPath, Stat: os.Stat,
		Logf: func(format string, a ...any) {
			_, _ = fmt.Fprintf(out, "  "+format+"\n", a...) //nolint:errcheck // best-effort terminal output
		},
	})
	if pyErr != nil {
		return "", fmt.Errorf("ensuring Python: %w", pyErr)
	}

	pysrcDir := filepath.Join(leoflowHome, "pysrc")
	if exErr := setup.ExtractFS(leoflow.PythonSources(), pysrcDir); exErr != nil {
		return "", fmt.Errorf("extracting embedded Python sources: %w", exErr)
	}
	_, _ = fmt.Fprintf(out, "  sources    extracted parser + runtime to %s\n", pysrcDir) //nolint:errcheck // best-effort terminal output

	// Fetch the Monaco editor bundle for the Lite web editor (ADR 0025).
	// Best-effort: an offline install still succeeds; the editor page shows a
	// `leoflow setup` hint until the bundle is present.
	if _, mErr := setup.EnsureMonaco(cmd.Context(), nil, leoflowHome, func(format string, a ...any) {
		_, _ = fmt.Fprintf(out, "  "+format+"\n", a...) //nolint:errcheck // best-effort terminal output
	}); mErr != nil {
		_, _ = fmt.Fprintf(out, "  WARNING: editor assets not fetched (%v) — the web editor will be unavailable until you re-run setup with network.\n", mErr) //nolint:errcheck // best-effort terminal output
	}

	// The parser is pure Python with vendored deps (ADR 0024) — no venv, no Airflow.
	parserCmd := fmt.Sprintf("env PYTHONPATH=%s %s -m leoflow_parser", filepath.Join(pysrcDir, "parser"), pyPath)

	// On first setup, generate the admin password, store ONLY its hash, and return
	// the plaintext for the one-time display. A re-run leaves the config untouched.
	var generated string
	if !liteConfigExists(leoflowHome) {
		pw, hash, herr := generateAdminCredential()
		if herr != nil {
			return "", herr
		}
		// Per-install JWT secret: a reinstall (which rewrites this file) invalidates
		// every token the previous install minted, so the SPA stops silently
		// accepting a stale browser token and the fresh login screen appears (#121).
		jwtSecret, jerr := generateJWTSecret()
		if jerr != nil {
			return "", jerr
		}
		// Per-install connection-encryption key. Lite shipped a constant
		// compiled into this repository, so every install shared it and a
		// database file gave up every credential in it (#486).
		//
		key, kerr := generateSecretKey()
		if kerr != nil {
			return "", kerr
		}
		sec := liteFileSecrets{jwtSecret: jwtSecret, secretKey: key}
		if wErr := writeLiteConfig(leoflowHome, parserCmd, lc, hash, sec); wErr != nil {
			return "", fmt.Errorf("writing config: %w", wErr)
		}
		generated = pw
	}

	if wsErr := os.MkdirAll(lc.Workspace, 0o750); wsErr != nil {
		return "", fmt.Errorf("creating workspace %s: %w", lc.Workspace, wsErr)
	}
	if wErr := writeSetupManifest(leoflowHome, setupManifest{
		Python: pyPath, Workspace: lc.Workspace, Tier: r.Tier.String(),
		OS: r.OS, Arch: r.Arch, Executor: lc.Executor, Port: lc.Port,
		AdminEmail: lc.AdminEmail, ParserCmd: parserCmd, UpdatedAt: time.Now().UTC(),
	}); wErr != nil {
		return "", fmt.Errorf("writing setup manifest: %w", wErr)
	}
	return generated, nil
}

// generateAdminCredential returns a humanized plaintext password and its bcrypt
// hash. The plaintext is shown once; only the hash is persisted.
func generateAdminCredential() (plaintext, hash string, err error) {
	plaintext, err = setup.GenerateHumanPassword()
	if err != nil {
		return "", "", fmt.Errorf("generating admin password: %w", err)
	}
	hash, err = auth.HashPassword(plaintext)
	if err != nil {
		return "", "", fmt.Errorf("hashing admin password: %w", err)
	}
	return plaintext, hash, nil
}

// printSetupSummary closes setup with the admin credentials (shown once) and the
// network-exposure warning, or — on a re-run — points at reset-password.
func printSetupSummary(out io.Writer, lc liteSettings, generatedPassword string) {
	p := newPalette(colorEnabled(out))
	if generatedPassword != "" {
		// High-contrast credentials block — users reported losing the password in
		// the install log ("fico buscando a info e nunca acho", #122). Horizontal
		// dividers, an uppercased title, and a bold + colored password value make it
		// unmissable. Stays readable on a non-TTY (palette empties to plain text).
		const sep = "═══════════════════════════════════════════════════════════════"
		_, _ = fmt.Fprintf(out, "\n  %s%s%s\n", p.bold, sep, p.reset)                                               //nolint:errcheck // best-effort terminal output
		_, _ = fmt.Fprintf(out, "    %sLEOFLOW LITE ADMIN — SAVE NOW (shown only once)%s\n", p.bold, p.reset)       //nolint:errcheck // best-effort terminal output
		_, _ = fmt.Fprintf(out, "  %s%s%s\n\n", p.bold, sep, p.reset)                                               //nolint:errcheck // best-effort terminal output
		_, _ = fmt.Fprintf(out, "    user:      %s\n", lc.AdminEmail)                                               //nolint:errcheck // best-effort terminal output
		_, _ = fmt.Fprintf(out, "    password:  %s%s%s%s%s\n", p.bold, p.cyan, generatedPassword, p.reset, p.reset) //nolint:errcheck // best-effort terminal output
		_, _ = fmt.Fprintf(out, "    open:      %shttp://localhost:%d%s\n\n", p.cyan, lc.Port, p.reset)             //nolint:errcheck // best-effort terminal output
		_, _ = fmt.Fprintln(out, "    Forgot it? Run: leoflow lite reset-password")                                 //nolint:errcheck // best-effort terminal output
		_, _ = fmt.Fprintf(out, "  %s%s%s\n", p.bold, sep, p.reset)                                                 //nolint:errcheck // best-effort terminal output
	} else {
		_, _ = fmt.Fprintln(out, "\n  admin already configured (~/.dexaflow/config.yaml); reset with `leoflow lite reset-password`.") //nolint:errcheck // best-effort terminal output
	}
	_, _ = fmt.Fprintln(out, "\n  SECURITY: Lite uses a short, human-friendly password and is meant for local/")     //nolint:errcheck // best-effort terminal output
	_, _ = fmt.Fprintln(out, "  trusted use only. Run it on an internal network or VPN — never expose it publicly.") //nolint:errcheck // best-effort terminal output
	// AAA close: tell the dev exactly what to do next, with what it does.
	_, _ = fmt.Fprintf(out, "\n  %s✓ You're all set!%s\n", p.green, p.reset) //nolint:errcheck // best-effort terminal output
	devPrintf(out, "\n      Start Leoflow Lite:        %sleoflow lite%s\n"+
		"        (opens the UI, scaffolds a starter DAG in %s if empty, and hot-reloads on save)\n"+
		"      Reach it from your network: %sleoflow lite --host 0.0.0.0%s\n",
		p.cyan, p.reset, lc.Workspace, p.cyan, p.reset)
}

// liteConfigExists reports whether ~/.dexaflow/config.yaml is already present.
func liteConfigExists(leoflowHome string) bool {
	_, err := os.Stat(filepath.Join(leoflowHome, "config.yaml"))
	return err == nil
}

// loadManifestSettings reads the previously-recorded Lite settings from
// setup.json (used on a re-run so prompts are not repeated), falling back to def.
func loadManifestSettings(leoflowHome string, def liteSettings) liteSettings {
	data, err := os.ReadFile(filepath.Join(leoflowHome, "setup.json"))
	if err != nil {
		return def
	}
	var m setupManifest
	if json.Unmarshal(data, &m) != nil {
		return def
	}
	out := def
	if m.Workspace != "" {
		out.Workspace = m.Workspace
	}
	if m.Executor != "" {
		out.Executor = m.Executor
	}
	if m.Port != 0 {
		out.Port = m.Port
	}
	if m.AdminEmail != "" {
		out.AdminEmail = m.AdminEmail
	}
	return out
}

// writeLiteConfig writes the Lite settings to ~/.dexaflow/config.yaml (0600). Only
// the bcrypt hash of the admin password is stored — never the plaintext. The
// per-install JWT secret rotates here (#121): a reinstall invalidates the prior
// install's tokens, so the SPA stops auto-accepting a stale browser token.
// writeLiteConfig rewrites ~/.dexaflow/config.yaml.
//
// The secrets travel as one struct because they must move together: every one
// of them is the only copy of something, and a rewrite that carries two of the
// three forward silently destroys the third.
func writeLiteConfig(leoflowHome, parserCmd string, lc liteSettings, adminHash string, sec liteFileSecrets) error {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "# Written by `leoflow setup` (Leoflow Lite).\n")
	_, _ = fmt.Fprintf(&b, "parser_cmd: %q\n", parserCmd)
	_, _ = fmt.Fprintf(&b, "workspace: %q\n", lc.Workspace)
	_, _ = fmt.Fprintf(&b, "lite_executor: %q\n", lc.Executor)
	_, _ = fmt.Fprintf(&b, "lite_port: %d\n", lc.Port)
	_, _ = fmt.Fprintf(&b, "admin_email: %q\n", lc.AdminEmail)
	_, _ = fmt.Fprintf(&b, "admin_password_hash: %q\n", adminHash)
	_, _ = fmt.Fprintf(&b, "jwt_secret: %q\n", sec.jwtSecret)
	// A secret is written only when there is one. Writing `secret_key: ""` would
	// record an empty key as the install's key, which reads back as "configured
	// and empty" rather than "absent", and the only copy of the real one is gone.
	if sec.secretKey != "" {
		_, _ = fmt.Fprintf(&b, "secret_key: %q\n", sec.secretKey)
	}
	if sec.secretKeyPrevious != "" {
		_, _ = fmt.Fprintf(&b, "secret_key_previous: %q\n", sec.secretKeyPrevious)
	}
	return writeFileAtomic(filepath.Join(leoflowHome, "config.yaml"), []byte(b.String()))
}

// generateSecretKey returns a fresh per-install connection-encryption key (32
// random bytes, hex-encoded). Same shape and lifecycle as the JWT secret: a
// reinstall rotates it, and the previous install's rows are re-encrypted rather
// than orphaned because the old key travels to the server as a read-only
// fallback (#486).
func generateSecretKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating secret key: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// generateJWTSecret returns a fresh per-install JWT signing secret (32 random
// bytes, hex-encoded). Persisted under ~/.dexaflow/config.yaml at setup so a
// reinstall rotates it and invalidates every token the previous install minted.
func generateJWTSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating jwt secret: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// writeSetupManifest persists the provisioning manifest to ~/.dexaflow/setup.json.
func writeSetupManifest(leoflowHome string, m setupManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(leoflowHome, "setup.json"), data, 0o600)
}

// libcSuffix renders " (musl)" / " (glibc)" or nothing for platforms without a
// reported libc (darwin).
func libcSuffix(libc string) string {
	if libc == "" {
		return ""
	}
	return " (" + libc + ")"
}
