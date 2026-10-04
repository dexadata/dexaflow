//go:build integration

package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
)

// The crash-injection tests re-run this test binary as the migration process
// and SIGKILL it at a named step boundary (ADR 0065 section 9). A returned
// error is not a crash: nothing after the kill point runs, no defer, no
// rollback the code chose to do.
const (
	childEnv      = "DEXAFLOW_TEST_MIGRATE_KEY_CHILD"
	childStateDir = "DEXAFLOW_TEST_MIGRATE_KEY_STATE_DIR"
	childDSNs     = "DEXAFLOW_TEST_MIGRATE_KEY_DSNS"
	childKillAt   = "DEXAFLOW_TEST_MIGRATE_KEY_KILL_AT"
)

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		os.Exit(runChild())
	}
	os.Exit(m.Run())
}

func runChild() int {
	killAt := os.Getenv(childKillAt)
	r := &migrateKeyRun{
		out:        os.Stdout,
		stateDir:   os.Getenv(childStateDir),
		datastores: datastoresFrom(os.Getenv(childDSNs)),
		yes:        true,
		crashAt: func(p string) {
			if p == killAt {
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				select {} // never returns; the signal is on its way
			}
		},
	}
	if err := r.run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func datastoresFrom(list string) []keyDatastore {
	var out []keyDatastore
	for i, dsn := range strings.Split(list, ",") {
		if dsn != "" {
			out = append(out, keyDatastore{name: fmt.Sprintf("test datastore %d", i+1), dsn: dsn})
		}
	}
	return out
}

// freshLiteDB creates a private, migrated database on the DATABASE_URL server.
func freshLiteDB(t *testing.T) string {
	t.Helper()
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("DATABASE_URL must point at a Postgres server for integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "lmk_" + hex.EncodeToString(b[:])
	if _, cerr := admin.Exec(ctx, "CREATE DATABASE "+name); cerr != nil {
		t.Skipf("cannot create a private database (%v)", cerr)
	}
	t.Cleanup(func() {
		c, cerr := pgx.Connect(context.Background(), base)
		if cerr == nil {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			_ = c.Close(context.Background())
		}
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	src, _ := iofs.New(migrations.Files, ".")
	mu := *u
	mu.Scheme = "pgx5"
	m, merr := migrate.NewWithSourceInstance("iofs", src, mu.String())
	if merr != nil {
		t.Fatal(merr)
	}
	defer func() { _, _ = m.Close() }()
	if uerr := m.Up(); uerr != nil && !errors.Is(uerr, migrate.ErrNoChange) {
		t.Fatal(uerr)
	}
	return u.String()
}

func cipherFor(t *testing.T, key string) secrets.Cipher {
	t.Helper()
	k, err := secrets.ParseKey(key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := secrets.NewAESGCM(k)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// seedRow inserts one connection whose password (and extra, when given) is
// sealed under the given keys, and records the plaintexts in seeded.
func seedRow(t *testing.T, dsn, connID, pwKey, extraKey string, seeded map[string]string) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(ctx) }()
	var pw, ex *string
	if pwKey != "" {
		s, _ := cipherFor(t, pwKey).Encrypt("pw-" + connID)
		pw = &s
		seeded[dsn+"|connections.password/"+connID] = "pw-" + connID
	}
	if extraKey != "" {
		s, _ := cipherFor(t, extraKey).Encrypt(`{"conn":"` + connID + `"}`)
		ex = &s
		seeded[dsn+"|connections.extra/"+connID] = `{"conn":"` + connID + `"}`
	}
	if _, err := c.Exec(ctx, `INSERT INTO connections (tenant_id, conn_id, conn_type, password, extra)
		SELECT id, $1, 'postgres', $2, $3 FROM tenants WHERE name = 'default'`, connID, pw, ex); err != nil {
		t.Fatal(err)
	}
}

const legacyConfig = "# Written by `dexaflow setup` (Dexaflow Lite).\n" +
	"parser_cmd: \"env PYTHONPATH=/p python3 -m leoflow_parser\"\n" +
	"workspace: \"/home/u/dexaflow\"\n" +
	"lite_executor: \"subprocess\"\n" +
	"lite_port: 8088\n" +
	"admin_email: \"admin@leoflow.local\"\n" +
	"admin_password_hash: \"$2a$12$abcdefghijklmnopqrstuv\"\n" +
	"jwt_secret: \"jwt-secret-kept\"\n"

func newStateDir(t *testing.T, cfg string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil { //nolint:gosec // a loose start mode is part of the test
		t.Fatal(err)
	}
	return dir
}

// recordedKeys builds the key set the on-disk config records: secret_key (or
// the published constant when absent) and every predecessor.
func recordedKeys(t *testing.T, stateDir string) (enc string, all []secrets.Cipher, cfg liteKeyConfig) {
	t.Helper()
	cfg, err := readLiteKeyConfig(filepath.Join(stateDir, "config.yaml"))
	if err != nil {
		t.Fatalf("on-disk config does not parse: %v", err)
	}
	enc = cfg.secretKey
	if enc == "" {
		enc = devSecretKey
	}
	for _, k := range append([]string{enc}, cfg.previous...) {
		all = append(all, cipherFor(t, k))
	}
	return enc, all, cfg
}

// assertInvariant is the invariant checker of ADR 0065 section 9:
// (I1) every non-empty ciphertext opens under some key the on-disk config
// records; (I2) every plaintext equals the seeded value; and, when final,
// (I3) every ciphertext opens under secret_key alone and no predecessor is
// recorded.
func assertInvariant(t *testing.T, stateDir string, dsns []string, seeded map[string]string, final bool) {
	t.Helper()
	enc, all, cfg := recordedKeys(t, stateDir)
	encC := cipherFor(t, enc)
	count := 0
	for _, dsn := range dsns {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		vals, err := storage.ReadSecretValues(context.Background(), c)
		_ = c.Close(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			count++
			key := dsn + "|" + v.Column.String() + "/" + v.Label
			plain, idx := secrets.OpenWith(all, v.Ciphertext)
			if idx < 0 {
				t.Errorf("I1: %s opens under no key the config records", key)
				continue
			}
			if plain != seeded[key] {
				t.Errorf("I2: %s is %q, seeded %q", key, plain, seeded[key])
			}
			if final {
				if _, derr := encC.Decrypt(v.Ciphertext); derr != nil {
					t.Errorf("I3: %s does not open under secret_key alone", key)
				}
			}
		}
	}
	if count != len(seeded) {
		t.Errorf("found %d secrets, seeded %d", count, len(seeded))
	}
	if final {
		if cfg.secretKey == "" || len(cfg.previous) != 0 {
			t.Errorf("I3: config records secret_key=%q previous=%v, want a key of its own and no predecessor", cfg.secretKey, cfg.previous)
		}
		if cfg.secretKey == devSecretKey {
			t.Error("I3: the published constant is the encrypting key")
		}
	}
}

func runMigrate(t *testing.T, stateDir string, dsns []string, mut func(*migrateKeyRun)) (string, error) {
	t.Helper()
	var out bytes.Buffer
	r := &migrateKeyRun{out: &out, stateDir: stateDir, yes: true}
	for _, d := range dsns {
		r.datastores = append(r.datastores, keyDatastore{name: "test datastore", dsn: d})
	}
	if mut != nil {
		mut(r)
	}
	err := r.run(context.Background())
	return out.String(), err
}

// otherFields returns the config with the key fields removed, to assert every
// other field survived both rewrites.
func otherFields(t *testing.T, stateDir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(l, "secret_key") && l != "" {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// The main path: a Legacy install with rows under the published key, and the
// operator's shell exporting a different key, ends fully migrated.
func TestMigrateKeyLegacyInstall(t *testing.T) {
	dsn := freshLiteDB(t)
	seeded := map[string]string{}
	seedRow(t, dsn, "warehouse", devSecretKey, devSecretKey, seeded)
	seedRow(t, dsn, "api", devSecretKey, "", seeded)
	seedRow(t, dsn, "nothing", "", "", seeded)
	state := newStateDir(t, legacyConfig)
	t.Setenv("LEOFLOW_SECRET_KEY", "an-exported-key-that-must-be-ignored")
	t.Setenv("DEXAFLOW_SECRET_KEY", "another-exported-key-also-ignored")

	out, err := runMigrate(t, state, []string{dsn}, nil)
	if err != nil {
		t.Fatalf("migrate-key: %v\n%s", err, out)
	}
	assertInvariant(t, state, []string{dsn}, seeded, true)
	if !strings.Contains(out, "LEOFLOW_SECRET_KEY") || !strings.Contains(out, "ignored") {
		t.Errorf("the exported key was not called out:\n%s", out)
	}
	if strings.Contains(out, "an-exported-key") {
		t.Error("the output printed a key value")
	}
	if got := otherFields(t, state); got != strings.TrimRight(legacyConfig, "\n") {
		t.Errorf("other fields changed:\n%s\nwant\n%s", got, legacyConfig)
	}
	fi, _ := os.Stat(filepath.Join(state, "config.yaml"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v, want 0600", fi.Mode().Perm())
	}
	if _, serr := os.Stat(filepath.Join(state, preMigrateKeyName)); !os.IsNotExist(serr) {
		t.Error("the pre-image was not cleaned up after a clean run")
	}
	for _, want := range []string{"fsynced, read back", "one transaction", "dexaflow lite backup", "uninstall"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// The published key no longer opens anything.
	c, _ := pgx.Connect(context.Background(), dsn)
	defer func() { _ = c.Close(context.Background()) }()
	if left, _ := storage.SecretValuesNotUnder(context.Background(), c, cipherFor(t, devSecretKey)); len(left) != 3 {
		t.Errorf("%d of 3 secrets no longer open under the published key, want all 3", len(left))
	}

	// Re-running on a migrated install writes nothing at all.
	before, _ := os.Stat(filepath.Join(state, "config.yaml"))
	xmins := rowVersions(t, dsn)
	out2, err := runMigrate(t, state, []string{dsn}, nil)
	if err != nil || !strings.Contains(out2, "already migrated") {
		t.Fatalf("second run: %v\n%s", err, out2)
	}
	after, _ := os.Stat(filepath.Join(state, "config.yaml"))
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Error("an already-migrated run rewrote config.yaml")
	}
	if got := rowVersions(t, dsn); got != xmins {
		t.Errorf("an already-migrated run wrote rows: %s -> %s", xmins, got)
	}
}

// rowVersions returns every connection row's xmin, which changes on any write.
func rowVersions(t *testing.T, dsn string) string {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(context.Background()) }()
	rows, err := c.Query(context.Background(), "SELECT conn_id || ':' || xmin::text FROM connections ORDER BY conn_id")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// The mixed case #1263 requires: rows under the published key, rows under the
// per-install key, and a row no key opens. The preflight refuses and leaves
// config and datastore byte-identical.
func TestMigrateKeyMixedDatastoreRefuses(t *testing.T) {
	dsn := freshLiteDB(t)
	own := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	seeded := map[string]string{}
	seedRow(t, dsn, "old", devSecretKey, "", seeded)
	seedRow(t, dsn, "new", own, own, seeded)
	seedRow(t, dsn, "orphan", "a-key-nobody-configured-32bytes!", "", seeded)
	cfg := legacyConfig + "secret_key: \"" + own + "\"\n"
	state := newStateDir(t, cfg)
	xmins := rowVersions(t, dsn)

	out, err := runMigrate(t, state, []string{dsn}, nil)
	if err == nil {
		t.Fatalf("a datastore with an unreadable secret must be refused:\n%s", out)
	}
	if !strings.Contains(out, "orphan") || !strings.Contains(out, "password") {
		t.Errorf("the refusal must name the connection and column:\n%s", out)
	}
	raw, _ := os.ReadFile(filepath.Join(state, "config.yaml"))
	if string(raw) != cfg {
		t.Errorf("config changed by a refused run:\n%s", raw)
	}
	if got := rowVersions(t, dsn); got != xmins {
		t.Errorf("rows written by a refused run")
	}
	entries, _ := os.ReadDir(state)
	for _, e := range entries {
		if e.Name() != "config.yaml" && e.Name() != configLockName {
			t.Errorf("a refused run left %s behind", e.Name())
		}
	}
}

// Stranded (gap 5): secret_key set, rows only under the constant, nothing
// recorded. The command recovers and migrates them, reusing the existing key.
func TestMigrateKeyRecoversAStrandedInstall(t *testing.T) {
	dsn := freshLiteDB(t)
	own := "1111111111111111111111111111111111111111111111111111111111111111"
	seeded := map[string]string{}
	seedRow(t, dsn, "stranded", devSecretKey, devSecretKey, seeded)
	seedRow(t, dsn, "fresh", own, "", seeded)
	state := newStateDir(t, legacyConfig+"secret_key: '"+own+"'\n")
	out, err := runMigrate(t, state, []string{dsn}, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertInvariant(t, state, []string{dsn}, seeded, true)
	if _, _, cfg := recordedKeys(t, state); cfg.secretKey != own {
		t.Errorf("an existing secret_key was replaced: %q", cfg.secretKey)
	}
}

// Rows under three keys: per-install, a hand-set predecessor, the constant.
func TestMigrateKeyThreeKeys(t *testing.T) {
	dsn := freshLiteDB(t)
	own := "2222222222222222222222222222222222222222222222222222222222222222"
	hand := "3333333333333333333333333333333333333333333333333333333333333333"
	seeded := map[string]string{}
	seedRow(t, dsn, "a", devSecretKey, hand, seeded)
	seedRow(t, dsn, "b", own, "", seeded)
	seedRow(t, dsn, "c", hand, own, seeded)
	state := newStateDir(t, legacyConfig+"secret_key: "+own+"\nsecret_key_previous: \""+hand+"\" # rotated by hand\n")
	out, err := runMigrate(t, state, []string{dsn}, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertInvariant(t, state, []string{dsn}, seeded, true)
}

// A Legacy install with zero connections still gets a key of its own before
// it writes its first secret.
func TestMigrateKeyLegacyWithNoConnections(t *testing.T) {
	dsn := freshLiteDB(t)
	state := newStateDir(t, legacyConfig)
	out, err := runMigrate(t, state, []string{dsn}, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertInvariant(t, state, []string{dsn}, map[string]string{}, true)
}

// Gap 9: rows in both the managed and the Docker datastore. Both are moved,
// and the predecessor is dropped only after both re-checks.
func TestMigrateKeyTwoDatastores(t *testing.T) {
	a, b := freshLiteDB(t), freshLiteDB(t)
	seeded := map[string]string{}
	seedRow(t, a, "in_a", devSecretKey, devSecretKey, seeded)
	seedRow(t, b, "in_b", devSecretKey, "", seeded)
	state := newStateDir(t, legacyConfig)
	out, err := runMigrate(t, state, []string{a, b}, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertInvariant(t, state, []string{a, b}, seeded, true)
}

// A server running against the datastore (it holds the lock shared) blocks the
// migration before step 1, with nothing written. With two datastores, a server
// on either one blocks it.
func TestMigrateKeyRefusesWhileAServerRuns(t *testing.T) {
	a, b := freshLiteDB(t), freshLiteDB(t)
	seeded := map[string]string{}
	seedRow(t, a, "in_a", devSecretKey, "", seeded)
	seedRow(t, b, "in_b", devSecretKey, "", seeded)
	server, err := pgx.Connect(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close(context.Background()) }()
	if ok, lerr := storage.TryKeyMigrationLockShared(context.Background(), server); lerr != nil || !ok {
		t.Fatal(ok, lerr)
	}
	state := newStateDir(t, legacyConfig)
	out, err := runMigrate(t, state, []string{a, b}, nil)
	if err == nil || !strings.Contains(out+err.Error(), "stop `dexaflow lite` first") {
		t.Fatalf("want a refusal naming `dexaflow lite`, got %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(filepath.Join(state, "config.yaml"))
	if string(raw) != legacyConfig {
		t.Error("config written while a server was running")
	}
	assertInvariant(t, state, []string{a, b}, seeded, false)
}

// Two migrations at once, each against its own datastore: the second refuses
// on the config lock and exactly one key is generated.
func TestMigrateKeyConcurrentRunsGenerateOneKey(t *testing.T) {
	a, b := freshLiteDB(t), freshLiteDB(t)
	seeded := map[string]string{}
	seedRow(t, a, "in_a", devSecretKey, "", seeded)
	seedRow(t, b, "in_b", devSecretKey, "", seeded)
	state := newStateDir(t, legacyConfig)

	var mu sync.Mutex
	var generated []string
	gen := func() (string, error) {
		k, err := generateSecretKey()
		mu.Lock()
		generated = append(generated, k)
		mu.Unlock()
		return k, err
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	var firstErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, firstErr = runMigrate(t, state, []string{a, b}, func(r *migrateKeyRun) {
			r.newKey = gen
			r.crashAt = func(p string) {
				if p == kpAfterPreflight {
					close(started)
					<-release
				}
			}
		})
	}()
	<-started
	_, secondErr := runMigrate(t, state, []string{b}, func(r *migrateKeyRun) { r.newKey = gen })
	close(release)
	wg.Wait()
	if !errors.Is(secondErr, errConfigLocked) {
		t.Errorf("the second run: %v, want errConfigLocked", secondErr)
	}
	if firstErr != nil {
		t.Fatalf("first run: %v", firstErr)
	}
	if len(generated) != 1 {
		t.Errorf("%d keys generated, want exactly 1", len(generated))
	}
	assertInvariant(t, state, []string{a, b}, seeded, true)
}

// A column that becomes unreadable between the preflight and the transaction:
// the transaction rolls back, the command exits non-zero, the config is in
// Pending, and I1 holds. Re-running after the row is fixed finishes the job.
func TestMigrateKeyColumnTurnsUnreadableMidRun(t *testing.T) {
	dsn := freshLiteDB(t)
	seeded := map[string]string{}
	seedRow(t, dsn, "good", devSecretKey, "", seeded)
	seedRow(t, dsn, "victim", devSecretKey, "", seeded)
	state := newStateDir(t, legacyConfig)
	var saved string
	out, err := runMigrate(t, state, []string{dsn}, func(r *migrateKeyRun) {
		r.crashAt = func(p string) {
			if p != kpAfterRecord {
				return
			}
			c, _ := pgx.Connect(context.Background(), dsn)
			defer func() { _ = c.Close(context.Background()) }()
			_ = c.QueryRow(context.Background(), "SELECT password FROM connections WHERE conn_id='victim'").Scan(&saved)
			bad, _ := cipherFor(t, "a-key-nobody-configured-32bytes!").Encrypt("x")
			_, _ = c.Exec(context.Background(), "UPDATE connections SET password=$1 WHERE conn_id='victim'", bad)
		}
	})
	if err == nil {
		t.Fatalf("a pass that met an unreadable column cannot succeed:\n%s", out)
	}
	for _, want := range []string{"Pending", "re-run `dexaflow lite migrate-key`"} {
		if !strings.Contains(out, want) {
			t.Errorf("failure output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "nothing was changed") {
		t.Errorf("a run that wrote the config claimed nothing changed:\n%s", out)
	}
	if _, _, cfg := recordedKeys(t, state); cfg.secretKey == "" || len(cfg.previous) == 0 {
		t.Errorf("config not in Pending: %+v", cfg)
	}
	// Put the original ciphertext back (the operator's fix), and re-run.
	c, _ := pgx.Connect(context.Background(), dsn)
	_, _ = c.Exec(context.Background(), "UPDATE connections SET password=$1 WHERE conn_id='victim'", saved)
	_ = c.Close(context.Background())
	assertInvariant(t, state, []string{dsn}, seeded, false)
	if out, err = runMigrate(t, state, []string{dsn}, nil); err != nil {
		t.Fatalf("re-run: %v\n%s", err, out)
	}
	assertInvariant(t, state, []string{dsn}, seeded, true)
}

// A full disk (or a read-only directory) at the temp write: refusal before any
// row is touched.
func TestMigrateKeyConfigWriteFailureTouchesNoRow(t *testing.T) {
	dsn := freshLiteDB(t)
	seeded := map[string]string{}
	seedRow(t, dsn, "a", devSecretKey, "", seeded)
	state := newStateDir(t, legacyConfig)
	xmins := rowVersions(t, dsn)
	out, err := runMigrate(t, state, []string{dsn}, func(r *migrateKeyRun) {
		r.write = func(path string, data []byte, opts atomicOpts) error {
			if filepath.Base(path) == "config.yaml" {
				return syscall.ENOSPC
			}
			return writeFileAtomicWith(path, data, opts)
		}
	})
	if err == nil {
		t.Fatalf("want a refusal:\n%s", out)
	}
	if got := rowVersions(t, dsn); got != xmins {
		t.Error("a row was touched although the key could not be recorded")
	}
	raw, _ := os.ReadFile(filepath.Join(state, "config.yaml"))
	if string(raw) != legacyConfig {
		t.Error("config changed")
	}
	assertInvariant(t, state, []string{dsn}, seeded, false)
}

// --dry-run in every state changes no file and no row.
func TestMigrateKeyDryRunWritesNothing(t *testing.T) {
	own := "4444444444444444444444444444444444444444444444444444444444444444"
	for name, cfg := range map[string]string{
		"Legacy":   legacyConfig,
		"Pending":  legacyConfig + "secret_key: " + own + "\nsecret_key_previous: " + devSecretKey + "\n",
		"Stranded": legacyConfig + "secret_key: " + own + "\n",
		"Migrated": legacyConfig + "secret_key: " + own + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			dsn := freshLiteDB(t)
			seeded := map[string]string{}
			if name == "Migrated" {
				seedRow(t, dsn, "a", own, "", seeded)
			} else {
				seedRow(t, dsn, "a", devSecretKey, "", seeded)
			}
			state := newStateDir(t, cfg)
			before := dirSnapshot(t, state)
			xmins := rowVersions(t, dsn)
			out, err := runMigrate(t, state, []string{dsn}, func(r *migrateKeyRun) { r.dryRun = true })
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if after := dirSnapshot(t, state); after != before {
				t.Errorf("dry run changed files:\n%s\n%s", before, after)
			}
			if rowVersions(t, dsn) != xmins {
				t.Error("dry run wrote rows")
			}
		})
	}
}

func dirSnapshot(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		fi, _ := e.Info()
		raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out = append(out, fmt.Sprintf("%s %v %d %s", e.Name(), fi.Mode(), fi.ModTime().UnixNano(), raw))
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// The twelve kill points of ADR 0065 section 9. At each one the process is
// SIGKILLed, the invariant (I1, I2) must hold on what it left, a Lite boot in
// that state must be able to read every secret and say what state it is in,
// and re-running the same command must finish the job (I3).
func TestMigrateKeyCrashInjection(t *testing.T) {
	points := []string{
		kpAfterPreflight, kpAfterPreImage, kpRecordTempWritten, kpRecordRenamed,
		kpAfterRecord, kpAfterFirstUpdate, kpAfterUpdates, kpAfterVerify,
		kpAfterCommit, kpAfterRecheck, kpDropTempWritten, kpAfterDrop,
	}
	for i, p := range points {
		t.Run(fmt.Sprintf("C%d_%s", i+1, p), func(t *testing.T) {
			dsn := freshLiteDB(t)
			seeded := map[string]string{}
			seedRow(t, dsn, "warehouse", devSecretKey, devSecretKey, seeded)
			seedRow(t, dsn, "api", devSecretKey, "", seeded)
			state := newStateDir(t, legacyConfig)

			cmd := exec.Command(os.Args[0], "-test.run=^$") //nolint:gosec // re-running this test binary
			cmd.Env = append(os.Environ(), childEnv+"=1", childStateDir+"="+state, childDSNs+"="+dsn, childKillAt+"="+p)
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("the child was not killed at %s: %v\n%s", p, err, out)
			}

			assertInvariant(t, state, []string{dsn}, seeded, false)
			assertBootCanRead(t, state, dsn, i+1 >= 4 && i+1 <= 11)

			rout, rerr := runMigrate(t, state, []string{dsn}, nil)
			if rerr != nil {
				t.Fatalf("re-run after a kill at %s: %v\n%s", p, rerr, rout)
			}
			assertInvariant(t, state, []string{dsn}, seeded, true)
			if _, serr := os.Stat(filepath.Join(state, preMigrateKeyName)); !os.IsNotExist(serr) {
				t.Errorf("pre-image left behind after the re-run completed")
			}
			if matches, _ := filepath.Glob(filepath.Join(state, ".tmp-*")); len(matches) != 0 {
				t.Errorf("temp files left behind: %v", matches)
			}
		})
	}
}

// assertBootCanRead runs the Lite boot's own key path on the state a kill left:
// the key list it would hand the server opens every secret, and the boot
// output names the state (Pending once both keys are recorded).
func assertBootCanRead(t *testing.T, state, dsn string, wantPending bool) {
	t.Helper()
	cfg, err := readLiteKeyConfig(filepath.Join(state, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := secrets.ParseKeys(liteSecretKeyList(cfg.secretKey, strings.Join(cfg.previous, ",")))
	if err != nil {
		t.Fatal(err)
	}
	var ciphers []secrets.Cipher
	for _, k := range keys {
		c, _ := secrets.NewAESGCM(k)
		ciphers = append(ciphers, c)
	}
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(context.Background()) }()
	vals, err := storage.ReadSecretValues(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vals {
		if _, idx := secrets.OpenWith(ciphers, v.Ciphertext); idx < 0 {
			t.Errorf("a Lite boot in this state cannot read %s of %s", v.Column.Column, v.Label)
		}
	}
	st, err := classifyKeyState(cfg, vals)
	if err != nil {
		t.Fatal(err)
	}
	msg := strings.Join(keyStateMessages(st, "the test datastore"), "\n")
	if wantPending && !strings.Contains(msg, "has not finished") {
		t.Errorf("boot output in this state lacks the Pending warning:\n%s", msg)
	}
}

// The migration's session terminated server-side mid-transaction: the pass
// rolls back, the config stays Pending, and a re-run completes.
func TestMigrateKeySessionTerminatedMidTransaction(t *testing.T) {
	dsn := freshLiteDB(t)
	seeded := map[string]string{}
	seedRow(t, dsn, "a", devSecretKey, devSecretKey, seeded)
	state := newStateDir(t, legacyConfig)
	out, err := runMigrate(t, state, []string{dsn}, func(r *migrateKeyRun) {
		r.crashAt = func(p string) {
			if p != kpAfterFirstUpdate {
				return
			}
			c, _ := pgx.Connect(context.Background(), dsn)
			defer func() { _ = c.Close(context.Background()) }()
			_, _ = c.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
				WHERE datname = current_database() AND pid <> pg_backend_pid()`)
		}
	})
	if err == nil {
		t.Fatalf("a terminated session cannot succeed:\n%s", out)
	}
	assertInvariant(t, state, []string{dsn}, seeded, false)
	if out, err = runMigrate(t, state, []string{dsn}, nil); err != nil {
		t.Fatalf("re-run: %v\n%s", err, out)
	}
	assertInvariant(t, state, []string{dsn}, seeded, true)
}
