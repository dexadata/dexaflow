package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// preRestoreName is the config.yaml a `dexaflow lite restore` replaced. It is
// kept until the next successful boot, then removed.
const preRestoreName = "config.yaml.pre-restore"

// liteKeyLock is the key-migration lock a `dexaflow lite` holds shared for as
// long as it supervises a server.
type liteKeyLock struct{ conn *pgx.Conn }

func (l *liteKeyLock) close(ctx context.Context) {
	if l != nil && l.conn != nil {
		_ = l.conn.Close(ctx) //nolint:errcheck // closing the session releases the lock; a dead session released it already
	}
}

// configPathForKeys is the config.yaml the boot reads keys from: --config when
// given, else ~/.dexaflow/config.yaml (which may not exist).
func configPathForKeys(cmd *cobra.Command) string {
	if p := configFilePath(cmd); p != "" {
		return p
	}
	if def, err := config.DefaultConfigFile(); err == nil {
		return def
	}
	return ""
}

// acquireLiteKeyBoot provisions the Lite database, takes the key-migration lock
// and reads the encryption keys, in that order, under the config lock (ADR 0065
// section 3):
//
//   - the config lock is taken shared and WAITED for, so a boot that arrives
//     during a migration reads the config the migration leaves, not the one
//     from before it;
//   - the advisory lock is taken BEFORE the keys are read and held until this
//     process exits, so no migration can run between the read and the server
//     that uses it;
//   - the keys come from the file alone, never the environment (gap 7).
//
// It then scans the encrypted columns read-only and prints the install's key
// state (section 5).
//
// The returned keyState is what that scan found; the caller removes
// config.yaml.pre-restore only when it is clean (ADR 0065 section 8).
func acquireLiteKeyBoot(ctx context.Context, cmd *cobra.Command, out io.Writer, o *devOptions) (*liteKeyLock, keyState, error) {
	stateDir, err := leoflowHome()
	if err != nil {
		return nil, keyState{}, err
	}
	if mkErr := os.MkdirAll(stateDir, 0o700); mkErr != nil {
		return nil, keyState{}, fmt.Errorf("creating %s: %w", stateDir, mkErr)
	}
	release, err := lockConfigOrWait(stateDir, func(s string) { devPrintln(out, s) })
	if err != nil {
		return nil, keyState{}, err
	}
	defer release()
	// --fresh drops the local database before it is recreated (#1104); doing
	// it under the config lock keeps it out of a running migration's way.
	if derr := provisionDevDatabase(ctx, cmd, out, o.fresh); derr != nil {
		return nil, keyState{}, derr
	}
	if merr := devMigrate(cmd); merr != nil {
		return nil, keyState{}, merr
	}
	lock, keys, err := lockAndReadKeys(ctx, configPathForKeys(cmd))
	if err != nil {
		return nil, keyState{}, err
	}
	o.secretKey, o.secretKeyPrevious = keys.secretKey, strings.Join(keys.previous, ",")
	if note := envKeyNote(liteSecretKeyList(o.secretKey, o.secretKeyPrevious), os.Getenv); note != "" {
		devPrintln(out, note)
	}
	st, serr := printKeyState(ctx, out, lock.conn, keys, describeBootDatastore(o))
	if serr != nil {
		lock.close(ctx)
		return nil, keyState{}, serr
	}
	return lock, st, nil
}

// lockAndReadKeys takes the key-migration lock shared on its own session in
// the Lite database, then reads the keys from cfgPath strictly.
func lockAndReadKeys(ctx context.Context, cfgPath string) (*liteKeyLock, liteKeyConfig, error) {
	conn, err := pgx.Connect(ctx, devDSNs().database)
	if err != nil {
		return nil, liteKeyConfig{}, fmt.Errorf("connecting to the Lite database to take the key-migration lock: %w", err)
	}
	lock := &liteKeyLock{conn: conn}
	ok, err := storage.TryKeyMigrationLockShared(ctx, conn)
	if err != nil {
		lock.close(ctx)
		return nil, liteKeyConfig{}, err
	}
	if !ok {
		lock.close(ctx)
		return nil, liteKeyConfig{}, errors.New("a key migration is in progress (`dexaflow lite migrate-key`); start Lite again once it has finished")
	}
	keys, err := readLiteKeyConfig(cfgPath)
	if err != nil {
		lock.close(ctx)
		return nil, liteKeyConfig{}, fmt.Errorf("refusing to start: the encryption keys in config.yaml cannot be read exactly (%w); "+
			"starting anyway could encrypt new secrets under a key the file does not record", err)
	}
	return lock, publishedKeyIsNotAKey(keys), nil
}

// printKeyState scans the encrypted columns this boot's datastore holds
// (read-only) and prints the install's key state.
func printKeyState(ctx context.Context, out io.Writer, conn *pgx.Conn, keys liteKeyConfig, scanned string) (keyState, error) {
	vals, err := storage.ReadSecretValues(ctx, conn)
	if err != nil {
		return keyState{}, fmt.Errorf("scanning the stored secrets: %w", err)
	}
	st, err := classifyKeyState(keys, vals)
	if err != nil {
		return keyState{}, fmt.Errorf("refusing to start: %w", err)
	}
	for _, line := range keyStateMessages(st, scanned) {
		devPrintln(out, line)
	}
	return st, nil
}

// describeBootDatastore names the datastore this boot scanned; it cannot speak
// for the other one an install may have, which only migrate-key scans.
func describeBootDatastore(o *devOptions) string {
	if sock := managedSocketDir(); sock != "" {
		return "the managed datastore (" + sock + ")"
	}
	if o.noUp {
		return "the datastore this run uses"
	}
	return fmt.Sprintf("the Docker datastore (localhost:%d)", devDBPort(liteDevDir()))
}

// liteServerEnviron is the server's environment: the inherited one without any
// variable Lite sets for the key handling, then Lite's own. The server prefers
// DEXAFLOW_* over LEOFLOW_*, so an exported DEXAFLOW_SECRET_KEY would otherwise
// beat the LEOFLOW_SECRET_KEY built from config.yaml, and new rows would land
// under a key the file does not record (ADR 0065 gap 7).
func liteServerEnviron(base, env []string) []string {
	drop := map[string]bool{}
	for _, suffix := range []string{"SECRET_KEY", "SECRET_KEY_REENCRYPT_ON_BOOT", "SECRET_KEY_MIGRATION_LOCK"} {
		drop["DEXAFLOW_"+suffix] = true
		drop["LEOFLOW_"+suffix] = true
	}
	out := make([]string, 0, len(base)+len(env))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			out = append(out, kv)
		}
	}
	return append(out, env...)
}

// serverKeyLockCapability is what a server that takes the key-migration lock
// advertises in its version output.
const serverKeyLockCapability = "lite-key-lock"

// checkServerKeyLock refuses a server binary that does not advertise the
// key-migration lock ("no lock, no boot", ADR 0065 section 3). `dexaflow lite`
// can be pointed at an older dexaflow-server (PATH, ./bin, --server-bin) that
// would ignore DEXAFLOW_SECRET_KEY_MIGRATION_LOCK; this CLI's own lock already
// covers its lifetime, and the check keeps "both hold it" true.
func checkServerKeyLock(ctx context.Context, serverBin string) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, serverBin, "version").Output() //nolint:gosec // path resolved by resolveBinary
	if err == nil && advertisesKeyLock(string(out)) {
		return nil
	}
	return fmt.Errorf("%s does not take the Lite key-migration lock (it predates `dexaflow lite migrate-key`); "+
		"run the dexaflow-server that ships with this CLI, or pass --server-bin to point at it", serverBin)
}

func advertisesKeyLock(versionOut string) bool {
	for _, line := range strings.Split(versionOut, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "capabilities:")
		if !ok {
			continue
		}
		for _, c := range strings.Fields(rest) {
			if c == serverKeyLockCapability {
				return true
			}
		}
	}
	return false
}

// resolveServerBin locates the server binary, reports it, and refuses one that
// does not take the key-migration lock.
func resolveServerBin(ctx context.Context, cmd *cobra.Command, explicit string) (string, error) {
	bin, err := resolveAndReport(ctx, cmd, explicit, "server")
	if err != nil {
		return "", err
	}
	if cerr := checkServerKeyLock(ctx, bin); cerr != nil {
		return "", cerr
	}
	return bin, nil
}

// removePreRestoreAfterBoot is removePreRestore for this user's ~/.dexaflow.
func removePreRestoreAfterBoot(out io.Writer, st keyState) {
	if dir, err := leoflowHome(); err == nil {
		removePreRestore(out, dir, st)
	}
}

// removePreRestore removes the config.yaml a restore replaced, once a boot with
// the restored config succeeded and its scan read every stored secret under the
// keys that config records, and says so. A boot that found Stranded or
// Unreadable secrets keeps the file: it may hold the key they need (ADR 0065
// section 8).
func removePreRestore(out io.Writer, stateDir string, st keyState) {
	p := filepath.Join(stateDir, preRestoreName)
	if _, err := os.Stat(p); err != nil {
		return
	}
	if st.stranded > 0 || st.unreadable > 0 {
		devPrintf(out, "  kept %s (the config a restore replaced): some stored secrets do not open under the restored config's keys, and it may hold the key they need\n", p)
		return
	}
	if err := os.Remove(p); err != nil {
		devPrintf(out, "  could not remove %s: %v\n", p, err)
		return
	}
	devPrintf(out, "  removed %s (the config a restore replaced) now that Lite started with the restored one\n", p)
}

// errServerExited reports that the control plane `dexaflow lite` supervises
// stopped on its own.
var errServerExited = errors.New("the control plane stopped")

// superviseServer returns a context that is canceled when the server process
// exits, and a function that reports that exit (nil when the context ended
// for another reason, such as Ctrl-C). `dexaflow lite` holds its key-migration
// lock until the server it supervises exits (ADR 0065 section 3): a server that
// stopped because it lost its lock session stops `dexaflow lite` with it,
// rather than leaving it watching files for a control plane that is gone.
//
// It waits on the process, so the caller must not call server.Wait itself.
func superviseServer(ctx context.Context, server *exec.Cmd) (supervised context.Context, exited func() error) {
	sctx, cancel := context.WithCancelCause(ctx)
	go func() {
		werr := server.Wait()
		if werr == nil {
			werr = errors.New("exit status 0")
		}
		cancel(fmt.Errorf("%w (%w); see its log above", errServerExited, werr))
	}()
	exited = func() error {
		if cause := context.Cause(sctx); errors.Is(cause, errServerExited) {
			return cause
		}
		return nil
	}
	return sctx, exited
}
