package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
)

// migrateProgress records what this run verified, for the failure report.
type migrateProgress struct {
	// started is true once step 1 began: from then on "nothing was written" is
	// no longer a claim this run can make.
	started  bool
	verified []string
}

func (p *migrateProgress) ok(r *migrateKeyRun, format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	p.verified = append(p.verified, line)
	r.say("  ✓ %s", line)
}

// migrate runs steps 1 to 7. Each step prints its result only after that
// step's own verification.
func (r *migrateKeyRun) migrate(ctx context.Context, cfg liteKeyConfig, raw []byte, pf *preflightResult, dss []openedDatastore, prog *migrateProgress) error {
	r.removeStaleTemps()
	prog.started = true
	if err := r.savePreImage(raw, prog); err != nil {
		return err
	}
	r.crashAt(kpAfterPreImage)

	encKey, prev, err := r.recordKeys(cfg, raw, pf, prog)
	if err != nil {
		return err
	}
	enc, err := cipherFromKey(encKey)
	if err != nil {
		return err
	}
	preds := make([]secrets.Cipher, 0, len(prev))
	for _, p := range prev {
		c, cerr := cipherFromKey(p)
		if cerr != nil {
			return cerr
		}
		preds = append(preds, c)
	}
	for _, d := range dss {
		if terr := r.moveDatastore(ctx, d, enc, preds, prog); terr != nil {
			return terr
		}
	}
	if rerr := r.recheck(ctx, dss, enc, prog); rerr != nil {
		return rerr
	}
	r.crashAt(kpAfterRecheck)
	if derr := r.dropPredecessors(encKey, prev, prog); derr != nil {
		return derr
	}
	r.crashAt(kpAfterDrop)
	r.cleanupPreImage(ctx, liteKeyConfig{exists: true, secretKey: encKey}, dss)
	r.say("  Done. %s is now the only copy of the key that opens these secrets:", r.configPath())
	r.say("  back it up with `dexaflow lite backup`, and copy it out before any `dexaflow uninstall`.")
	r.say("  Start Lite with `dexaflow lite`.")
	return nil
}

// removeStaleTemps removes temp files an interrupted rewrite left next to
// config.yaml (a kill between the temp write and the rename).
func (r *migrateKeyRun) removeStaleTemps() {
	matches, _ := filepath.Glob(filepath.Join(r.stateDir, ".tmp-config.yaml-*"))       //nolint:errcheck // a bad pattern is impossible here
	pre, _ := filepath.Glob(filepath.Join(r.stateDir, ".tmp-"+preMigrateKeyName+"-*")) //nolint:errcheck // see above
	for _, m := range append(matches, pre...) {
		_ = os.Remove(m) //nolint:errcheck // best effort; a leftover temp file holds nothing the config does not
	}
}

// savePreImage is step 1: copy config.yaml aside, keeping an older copy from an
// interrupted run, since that one is the true pre-state.
func (r *migrateKeyRun) savePreImage(raw []byte, prog *migrateProgress) error {
	if _, err := os.Stat(r.preImagePath()); err == nil {
		prog.ok(r, "kept the copy of your config from an earlier run at %s", r.preImagePath())
		return nil
	}
	if err := r.write(r.preImagePath(), raw, atomicOpts{ownerFrom: r.configPath()}); err != nil {
		return fmt.Errorf("saving a copy of config.yaml before changing it: %w", err)
	}
	prog.ok(r, "saved your current config to %s", r.preImagePath())
	return nil
}

// encryptingKey reuses an existing secret_key (a resumed run, Stranded, a
// hand-set rotation) and generates one only when the config has none.
func (r *migrateKeyRun) encryptingKey(cfg liteKeyConfig) (string, error) {
	if cfg.secretKey != "" {
		return cfg.secretKey, nil
	}
	return r.newKey()
}

// predecessorsToRecord lists every key the config recorded, plus every other
// candidate that opened a value, minus the encrypting key. No recorded key
// leaves in step 2, even one that opened nothing; the published constant is
// written out literally rather than implied by an empty field.
func predecessorsToRecord(encKey string, pf *preflightResult) ([]string, error) {
	encBytes, err := secrets.ParseKey(encKey)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range pf.cands {
		if bytes.Equal(c.bytes, encBytes) {
			continue
		}
		if c.recorded || c.opened > 0 {
			out = append(out, c.raw)
		}
	}
	return out, nil
}

// recordKeys is steps 2 and 3: write the encrypting key and every predecessor
// into config.yaml (atomic, fsynced), then read it back strictly and require
// the keys byte for byte. No row is touched before this returns nil.
func (r *migrateKeyRun) recordKeys(cfg liteKeyConfig, raw []byte, pf *preflightResult, prog *migrateProgress) (encKey string, prev []string, err error) {
	encKey, err = r.encryptingKey(cfg)
	if err != nil {
		return "", nil, err
	}
	prev, err = predecessorsToRecord(encKey, pf)
	if err != nil {
		return "", nil, err
	}
	next, err := setLiteKeys(raw, encKey, prev)
	if err != nil {
		return "", nil, err
	}
	hook := func(stage string) {
		switch stage {
		case atomicTempWritten:
			r.crashAt(kpRecordTempWritten)
		case atomicRenamed:
			r.crashAt(kpRecordRenamed)
		}
	}
	if werr := r.write(r.configPath(), next, atomicOpts{hook: hook}); werr != nil {
		return "", nil, fmt.Errorf("recording the keys in %s (no row was touched): %w", r.configPath(), werr)
	}
	r.crashAt(kpAfterRecord)
	if verr := r.readBack(encKey, prev); verr != nil {
		return "", nil, verr
	}
	prog.ok(r, "recorded %s and %s in %s (fsynced, read back)",
		describeEncKey(cfg), describeKeys(prev), r.configPath())
	return encKey, prev, nil
}

func describeEncKey(cfg liteKeyConfig) string {
	if cfg.secretKey == "" {
		return "the new key"
	}
	return "this install's key"
}

// describeKeys names keys without printing them.
func describeKeys(keys []string) string {
	if len(keys) == 0 {
		return "no predecessor"
	}
	var names []string
	for i, k := range keys {
		if k == devSecretKey {
			names = append(names, "the published key")
		} else {
			names = append(names, fmt.Sprintf("predecessor %d", i+1))
		}
	}
	return strings.Join(names, " and ")
}

// readBack re-reads config.yaml through the strict parser and requires exactly
// the keys this run holds in memory.
func (r *migrateKeyRun) readBack(encKey string, prev []string) error {
	got, err := readLiteKeyConfig(r.configPath())
	if err != nil {
		return fmt.Errorf("reading %s back: %w", r.configPath(), err)
	}
	if got.secretKey != encKey || strings.Join(got.previous, ",") != strings.Join(prev, ",") {
		return fmt.Errorf("%s does not read back with the keys just written; stopping before any row is touched", r.configPath())
	}
	return nil
}

// moveDatastore is step 4 for one datastore.
func (r *migrateKeyRun) moveDatastore(ctx context.Context, d openedDatastore, enc secrets.Cipher, preds []secrets.Cipher, prog *migrateProgress) error {
	res, err := storage.MigrateSecretValues(ctx, d.conn, enc, preds, storage.KeyMigrationHooks{
		AfterFirstUpdate: func() { r.crashAt(kpAfterFirstUpdate) },
		AfterUpdates:     func() { r.crashAt(kpAfterUpdates) },
		AfterVerify:      func() { r.crashAt(kpAfterVerify) },
	})
	if err != nil {
		return fmt.Errorf("the transaction in %s rolled back (migrated %d, skipped %d, unreadable %d before it stopped): %w",
			d.name, res.Migrated, res.Skipped, res.Unreadable, err)
	}
	if !res.Clean() {
		return fmt.Errorf("the pass in %s was not clean (skipped %d, unreadable %d)", d.name, res.Skipped, res.Unreadable)
	}
	r.crashAt(kpAfterCommit)
	prog.ok(r, "re-encrypted %s in one transaction in %s; verified all %d open under the new key alone",
		countOf(res.Migrated, "secret", "secrets"), d.name, res.Verified)
	return nil
}

// recheck is step 5: a fresh read, outside any transaction, in every datastore.
func (r *migrateKeyRun) recheck(ctx context.Context, dss []openedDatastore, enc secrets.Cipher, prog *migrateProgress) error {
	for _, d := range dss {
		left, err := storage.SecretValuesNotUnder(ctx, d.conn, enc)
		if err != nil {
			return fmt.Errorf("re-checking %s: %w", d.name, err)
		}
		if len(left) > 0 {
			return fmt.Errorf("re-check after commit: %s in %s still need another key; the predecessor stays recorded",
				countOf(len(left), "secret", "secrets"), d.name)
		}
	}
	prog.ok(r, "re-checked after commit: 0 secrets need any other key, in %s", countOf(len(dss), "datastore", "datastores"))
	return nil
}

// dropPredecessors is step 6: rewrite config.yaml without secret_key_previous,
// then read it back.
func (r *migrateKeyRun) dropPredecessors(encKey string, prev []string, prog *migrateProgress) error {
	raw, err := os.ReadFile(r.configPath())
	if err != nil {
		return fmt.Errorf("reading %s: %w", r.configPath(), err)
	}
	next, err := setLiteKeys(raw, encKey, nil)
	if err != nil {
		return err
	}
	hook := func(stage string) {
		if stage == atomicTempWritten {
			r.crashAt(kpDropTempWritten)
		}
	}
	if werr := r.write(r.configPath(), next, atomicOpts{hook: hook}); werr != nil {
		return fmt.Errorf("dropping the predecessor from %s: %w", r.configPath(), werr)
	}
	if verr := r.readBack(encKey, nil); verr != nil {
		return verr
	}
	prog.ok(r, "dropped %s from %s (fsynced, read back)", describeKeys(prev), r.configPath())
	return nil
}

// cleanupPreImage is step 7. The pre-image is removed only when every key it
// records that still opens a stored value is also recorded by the current
// config; otherwise it may hold the only copy of a needed key and is kept.
func (r *migrateKeyRun) cleanupPreImage(ctx context.Context, current liteKeyConfig, dss []openedDatastore) {
	path := r.preImagePath()
	pre, err := readLiteKeyConfig(path)
	if err != nil || !pre.exists {
		if err != nil {
			r.say("  kept %s: it could not be read (%v)", path, err)
		}
		return
	}
	if needed := preImageStillNeeded(ctx, pre, current, dss); needed {
		r.say("  kept %s: it records a key that still opens a stored secret", path)
		return
	}
	if rerr := os.Remove(path); rerr != nil {
		r.say("  could not remove %s: %v", path, rerr)
		return
	}
	_ = syncDir(r.stateDir) //nolint:errcheck // best effort; a surviving pre-image is harmless
}

func preImageStillNeeded(ctx context.Context, pre, current liteKeyConfig, dss []openedDatastore) bool {
	keep := map[string]bool{}
	for _, k := range recordedKeyStrings(current) {
		if b, err := secrets.ParseKey(k); err == nil {
			keep[string(b)] = true
		}
	}
	for _, k := range recordedKeyStrings(pre) {
		b, err := secrets.ParseKey(k)
		if err != nil {
			return true
		}
		if keep[string(b)] {
			continue
		}
		c, err := secrets.NewAESGCM(b)
		if err != nil {
			return true
		}
		for _, d := range dss {
			vals, rerr := storage.ReadSecretValues(ctx, d.conn)
			if rerr != nil {
				return true
			}
			for _, v := range vals {
				if _, derr := c.Decrypt(v.Ciphertext); derr == nil {
					return true
				}
			}
		}
	}
	return false
}

// reportFailure prints, in order: what was verified, the state the install is
// in, which keys the config records, and that re-running is the way forward
// (ADR 0065 section 5).
func (r *migrateKeyRun) reportFailure(prog *migrateProgress, cause error) {
	r.say("  ✗ %v", cause)
	if len(prog.verified) > 0 {
		r.say("  Verified before the failure:")
		for _, v := range prog.verified {
			r.say("    ✓ %s", v)
		}
	}
	cfg, err := readLiteKeyConfig(r.configPath())
	switch {
	case err != nil:
		r.say("  State: unknown, %s could not be read: %v", r.configPath(), err)
	case cfg.secretKey == "":
		r.say("  State: Legacy. config.yaml records only the published key.")
	case len(cfg.previous) > 0:
		r.say("  State: Pending. config.yaml records this install's key and %s; both are still needed.", describeKeys(cfg.previous))
	default:
		r.say("  State: config.yaml records this install's key and no predecessor.")
	}
	if prog.started {
		if _, serr := os.Stat(r.preImagePath()); serr == nil {
			r.say("  Your config as it was before this migration is kept at %s; do not restore it once Lite has run since.", r.preImagePath())
		}
	}
	r.say("  Every stored secret still opens under a key config.yaml records; re-run `dexaflow lite migrate-key` to continue.")
}

// envKeyVars are the variables a shell may export that would otherwise override
// the key in config.yaml.
var envKeyVars = []string{"DEXAFLOW_SECRET_KEY", "LEOFLOW_SECRET_KEY"}

// envKeyNote returns one line naming an exported key variable that differs
// from fileList and is ignored, or "". The file is the source of truth (gap 7).
func envKeyNote(fileList string, getenv func(string) string) string {
	var set []string
	for _, v := range envKeyVars {
		if val := getenv(v); val != "" && val != fileList {
			set = append(set, v)
		}
	}
	return envKeyLine(set)
}

// envKeyNoteAlways is envKeyNote for migrate-key, which says so whenever a
// variable is set at all, so nobody believes it was used.
func envKeyNoteAlways(getenv func(string) string) string {
	var set []string
	for _, v := range envKeyVars {
		if getenv(v) != "" {
			set = append(set, v)
		}
	}
	return envKeyLine(set)
}

func envKeyLine(set []string) string {
	if len(set) == 0 {
		return ""
	}
	return "  note: " + strings.Join(set, " and ") + " is set in your environment and is ignored; Lite's keys come from ~/.dexaflow/config.yaml only."
}
