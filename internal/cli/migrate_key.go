package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
)

// preMigrateKeyName is the copy of config.yaml taken before migrate-key changes
// it (ADR 0065 step 1).
const preMigrateKeyName = "config.yaml.pre-migrate-key"

// Named step boundaries of migrate-key, C1 to C12 of the ADR 0065 test matrix.
// migrateKeyRun.crashAt is called at each one; production leaves it a no-op,
// and the crash-injection tests SIGKILL the process there.
const (
	kpAfterPreflight    = "after-preflight"     // C1: after the preflight, before the pre-image
	kpAfterPreImage     = "after-pre-image"     // C2: after the pre-image, before the config temp file
	kpRecordTempWritten = "record-temp-written" // C3: the temp file is written, not renamed
	kpRecordRenamed     = "record-renamed"      // C4: renamed, before the directory fsync
	kpAfterRecord       = "after-record"        // C5: config written, before it is read back
	kpAfterFirstUpdate  = "after-first-update"  // C6: inside the transaction, after the first UPDATE
	kpAfterUpdates      = "after-updates"       // C7: after every UPDATE, before the in-transaction check
	kpAfterVerify       = "after-verify"        // C8: after the check, before COMMIT
	kpAfterCommit       = "after-commit"        // C9: immediately after COMMIT returns
	kpAfterRecheck      = "after-recheck"       // C10: after the post-commit re-check, before the drop
	kpDropTempWritten   = "drop-temp-written"   // C11: during the drop rewrite, before rename
	kpAfterDrop         = "after-drop"          // C12: after the drop, before the pre-image cleanup
)

// keyDatastore is one Lite datastore migrate-key covers.
type keyDatastore struct {
	name string // for messages, e.g. "the managed datastore (~/.dexaflow/pgdata)"
	dsn  string // the Lite database in it
}

// migrateKeyRun is one `dexaflow lite migrate-key`.
//
// The order of its steps is the whole design (ADR 0065 section 2): every key is
// recorded in config.yaml, fsynced and read back BEFORE any row is written under
// the new one, the rows move in one transaction per datastore that commits only
// after a verified pass, and a predecessor leaves the config only after a
// committed pass re-checked under the new key alone in every datastore. At every
// instant, every stored ciphertext opens under a key the on-disk config records.
type migrateKeyRun struct {
	out        io.Writer
	stateDir   string // ~/.dexaflow
	datastores []keyDatastore
	dryRun     bool
	yes        bool
	confirm    func() bool
	newKey     func() (string, error)
	crashAt    func(point string)
	getenv     func(string) string
	write      func(path string, data []byte, opts atomicOpts) error
}

// errMigrateKeyDeclined reports a declined prompt: the install is not
// migrated, so the exit status is not 0.
var errMigrateKeyDeclined = errors.New("key migration declined; nothing was written")

// errUnreadableSecrets reports a preflight that found values no candidate key
// opens.
var errUnreadableSecrets = errors.New("some stored secrets open under no known key; nothing was written")

func (r *migrateKeyRun) defaults() {
	if r.out == nil {
		r.out = io.Discard
	}
	if r.newKey == nil {
		r.newKey = generateSecretKey
	}
	if r.crashAt == nil {
		r.crashAt = func(string) {}
	}
	if r.getenv == nil {
		r.getenv = os.Getenv
	}
	if r.write == nil {
		r.write = writeFileAtomicWith
	}
}

func (r *migrateKeyRun) configPath() string { return filepath.Join(r.stateDir, "config.yaml") }
func (r *migrateKeyRun) preImagePath() string {
	return filepath.Join(r.stateDir, preMigrateKeyName)
}
func (r *migrateKeyRun) say(format string, a ...any) { devPrintf(r.out, format+"\n", a...) }

// run executes the command. A nil error means the install ends fully migrated,
// including "already migrated, nothing to do"; any error means it did not, and
// the output says which recoverable state it is in.
func (r *migrateKeyRun) run(ctx context.Context) error {
	r.defaults()
	release, err := r.lockConfig()
	if err != nil {
		return err
	}
	defer release()

	cfg, raw, err := r.readConfig()
	if err != nil {
		return err
	}
	cfg = publishedKeyIsNotAKey(cfg)
	if note := envKeyNoteAlways(r.getenv); note != "" {
		r.say("%s", note)
	}
	dss, err := r.openDatastores(ctx)
	defer closeDatastores(ctx, dss)
	if err != nil {
		return err
	}
	pf, err := r.preflight(ctx, cfg, dss)
	r.crashAt(kpAfterPreflight)
	if err != nil {
		return err
	}
	if pf.alreadyMigrated(cfg) {
		return r.finishAlreadyMigrated(ctx, cfg, dss, pf)
	}
	r.printPlan(cfg, pf)
	if r.dryRun {
		r.say("  --dry-run: no file and no row was written.")
		return nil
	}
	if !r.yes && (r.confirm == nil || !r.confirm()) {
		r.say("  declined; no file and no row was written.")
		return errMigrateKeyDeclined
	}
	prog := &migrateProgress{}
	if merr := r.migrate(ctx, cfg, raw, pf, dss, prog); merr != nil {
		r.reportFailure(prog, merr)
		return merr
	}
	return nil
}

func (r *migrateKeyRun) lockConfig() (func(), error) {
	if r.dryRun {
		return lockConfigDirIfPresent(r.stateDir, configLockShared)
	}
	// No config, nothing to migrate: refuse before creating even the lock file.
	// readConfig checks again under the lock.
	if _, err := os.Stat(r.configPath()); err != nil {
		return nil, fmt.Errorf("refusing to migrate: %s does not exist; run `dexaflow setup` first, then `dexaflow lite migrate-key` (no file and no row was written)", r.configPath())
	}
	return lockConfigDir(r.stateDir, configLockExclusive, false)
}

// readConfig parses config.yaml strictly and refuses a missing or unreadable
// one: the command never invents a config.
func (r *migrateKeyRun) readConfig() (liteKeyConfig, []byte, error) {
	cfg, err := readLiteKeyConfig(r.configPath())
	if err != nil {
		return cfg, nil, fmt.Errorf("refusing to migrate: %w; fix the file and run this again (no file and no row was written)", err)
	}
	if !cfg.exists {
		return cfg, nil, fmt.Errorf("refusing to migrate: %s does not exist; run `dexaflow setup` first, then `dexaflow lite migrate-key` (no file and no row was written)", r.configPath())
	}
	raw, err := os.ReadFile(r.configPath())
	if err != nil {
		return cfg, nil, fmt.Errorf("reading %s: %w", r.configPath(), err)
	}
	return cfg, raw, nil
}

// publishedKeyIsNotAKey treats a secret_key equal to the published constant as
// no key of its own: the install is Legacy, the constant is recorded as a
// predecessor, and a fresh key is generated. Reusing it would "migrate" onto
// the key the command exists to leave.
func publishedKeyIsNotAKey(cfg liteKeyConfig) liteKeyConfig {
	b, err := secrets.ParseKey(cfg.secretKey)
	if err != nil || !bytes.Equal(b, []byte(devSecretKey)) {
		return cfg
	}
	cfg.previous = append([]string{cfg.secretKey}, cfg.previous...)
	cfg.secretKey = ""
	return cfg
}

// openedDatastore is a datastore this run holds the exclusive lock on.
type openedDatastore struct {
	keyDatastore
	conn *pgx.Conn
}

func closeDatastores(ctx context.Context, dss []openedDatastore) {
	for _, d := range dss {
		_ = d.conn.Close(ctx) //nolint:errcheck // closing also releases the advisory lock
	}
}

// openDatastores connects to every datastore and takes the key-migration lock
// exclusively in each, refusing when a Lite server holds it. A datastore with
// no Lite database has never stored a secret and is skipped.
func (r *migrateKeyRun) openDatastores(ctx context.Context) ([]openedDatastore, error) {
	var out []openedDatastore
	for _, ds := range r.datastores {
		conn, err := pgx.Connect(ctx, ds.dsn)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "3D000" {
			r.say("  %s has no Lite database yet; nothing is stored there.", ds.name)
			continue
		}
		if err != nil {
			return out, fmt.Errorf("refusing to migrate: %s cannot be reached (%w); every datastore that might hold secrets must be scanned before a key can be dropped (no file and no row was written)", ds.name, err)
		}
		out = append(out, openedDatastore{keyDatastore: ds, conn: conn})
		ok, err := storage.TryKeyMigrationLockExclusive(ctx, conn)
		if err != nil {
			return out, err
		}
		if !ok {
			return out, fmt.Errorf("refusing to migrate: a Lite server is running against %s; stop `dexaflow lite` first (no file and no row was written)", ds.name)
		}
	}
	return out, nil
}

// candidateKey is one key the preflight tries.
type candidateKey struct {
	label    string // what messages call it; never the key itself
	raw      string
	bytes    []byte
	cipher   secrets.Cipher
	recorded bool // the config records it (an absent secret_key records the constant)
	opened   int  // values it opened
}

// unreadableValue is a value no candidate opens, and where it is.
type unreadableValue struct {
	datastore string
	value     storage.SecretValue
}

// preflightResult is what step 0 found.
type preflightResult struct {
	cands      []candidateKey // secret_key first when set, then predecessors, then the constant
	total      int
	conns      map[string]bool
	unreadable []unreadableValue
}

// alreadyMigrated: a key of its own, no predecessor, and every value opens
// under that key alone.
func (p *preflightResult) alreadyMigrated(cfg liteKeyConfig) bool {
	return cfg.secretKey != "" && len(cfg.previous) == 0 && p.total == p.cands[0].opened
}

// buildCandidates lists secret_key, every predecessor and, always, the
// published constant: rows can be under it even when the config does not say
// so (gap 5). Duplicates (the same key bytes spelled twice) are dropped.
func buildCandidates(cfg liteKeyConfig) ([]candidateKey, error) {
	type spec struct {
		label, raw string
		recorded   bool
	}
	var specs []spec
	if cfg.secretKey != "" {
		specs = append(specs, spec{"this install's key (secret_key)", cfg.secretKey, true})
	}
	for i, p := range cfg.previous {
		specs = append(specs, spec{fmt.Sprintf("predecessor %d (secret_key_previous)", i+1), p, true})
	}
	specs = append(specs, spec{"the key published in this repository", devSecretKey, cfg.secretKey == ""})
	var out []candidateKey
	for _, s := range specs {
		b, err := secrets.ParseKey(s.raw)
		if err != nil {
			return nil, fmt.Errorf("refusing to migrate: %s in config.yaml is not a usable key: %w (no file and no row was written)", s.label, err)
		}
		if dup := findKey(out, b); dup >= 0 {
			out[dup].recorded = out[dup].recorded || s.recorded
			continue
		}
		c, err := secrets.NewAESGCM(b)
		if err != nil {
			return nil, err
		}
		if s.raw == devSecretKey {
			s.label = "the key published in this repository"
		}
		out = append(out, candidateKey{label: s.label, raw: s.raw, bytes: b, cipher: c, recorded: s.recorded})
	}
	return out, nil
}

func findKey(cands []candidateKey, b []byte) int {
	for i, c := range cands {
		if bytes.Equal(c.bytes, b) {
			return i
		}
	}
	return -1
}

// preflight is step 0: read every encrypted value in every datastore and
// classify it by the key that opens it. It writes nothing.
func (r *migrateKeyRun) preflight(ctx context.Context, cfg liteKeyConfig, dss []openedDatastore) (*preflightResult, error) {
	cands, err := buildCandidates(cfg)
	if err != nil {
		return nil, err
	}
	pf := &preflightResult{cands: cands, conns: map[string]bool{}}
	ciphers := make([]secrets.Cipher, len(cands))
	for i, c := range cands {
		ciphers[i] = c.cipher
	}
	for _, d := range dss {
		vals, rerr := storage.ReadSecretValues(ctx, d.conn)
		if rerr != nil {
			return nil, fmt.Errorf("reading the stored secrets in %s: %w", d.name, rerr)
		}
		for _, v := range vals {
			pf.total++
			pf.conns[d.name+"\x00"+v.RowID] = true
			if _, idx := secrets.OpenWith(ciphers, v.Ciphertext); idx >= 0 {
				pf.cands[idx].opened++
			} else {
				pf.unreadable = append(pf.unreadable, unreadableValue{datastore: d.name, value: v})
			}
		}
	}
	if len(pf.unreadable) > 0 {
		r.say("  Refusing to migrate: %s open under no key this install records, nor under the published key:", countOf(len(pf.unreadable), "stored secret", "stored secrets"))
		for _, u := range pf.unreadable {
			r.say("    - connection %q: %s (in %s)", u.value.Label, u.value.Column.Column, u.datastore)
		}
		r.say("  These values are already under a key nobody recorded. Re-enter or delete those connections,")
		r.say("  or add the key they were written with to `secret_key_previous` in %s, then run this again.", r.configPath())
		r.say("  No file and no row was written.")
		return pf, errUnreadableSecrets
	}
	return pf, nil
}

func (r *migrateKeyRun) printPlan(cfg liteKeyConfig, pf *preflightResult) {
	parts := make([]string, 0, len(pf.cands))
	for _, c := range pf.cands {
		parts = append(parts, fmt.Sprintf("%d under %s", c.opened, c.label))
	}
	r.say("  Found %s (%s) in %s: %s, 0 that no known key opens.",
		countOf(pf.total, "connection secret", "connection secrets"), countOf(len(pf.conns), "connection", "connections"),
		countOf(len(r.datastores), "datastore", "datastores"), strings.Join(parts, ", "))
	if cfg.secretKey == "" {
		r.say("  This re-encrypts them onto a new key that only this install has.")
	} else {
		r.say("  This re-encrypts them onto this install's existing key (secret_key) and then drops every predecessor.")
	}
	r.say("  The Lite server must stay stopped until this finishes.")
}

// finishAlreadyMigrated reports "already migrated", writing nothing except the
// removal of a leftover pre-image that records no key a row still needs.
func (r *migrateKeyRun) finishAlreadyMigrated(ctx context.Context, cfg liteKeyConfig, dss []openedDatastore, pf *preflightResult) error {
	r.say("  Nothing to do, already migrated: all %s open under this install's key alone, and no predecessor is recorded.",
		countOf(pf.total, "stored secret", "stored secrets"))
	if !r.dryRun {
		r.cleanupPreImage(ctx, cfg, dss)
	}
	return nil
}
