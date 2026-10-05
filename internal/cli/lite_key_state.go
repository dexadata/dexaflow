package cli

import (
	"fmt"

	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
)

// keyState is what a read-only scan found about an install's keys (ADR 0065
// section 5). Several conditions can hold at once.
type keyState struct {
	// legacy: the config records no secret_key, so the published constant
	// encrypts.
	legacy bool
	// pending: a predecessor is recorded, so a migration has not finished.
	pending bool
	// stranded counts values that open only under the published constant,
	// which the config does not record (gap 5).
	stranded int
	// unreadable counts values that open under no recorded key and not under
	// the constant either.
	unreadable int
}

// cipherFromKey builds an AES-GCM cipher from one configured key string.
func cipherFromKey(key string) (secrets.Cipher, error) {
	k, err := secrets.ParseKey(key)
	if err != nil {
		return nil, err
	}
	return secrets.NewAESGCM(k)
}

// recordedKeyStrings lists the keys a config records, encrypting key first: an
// absent secret_key records the published constant.
func recordedKeyStrings(cfg liteKeyConfig) []string {
	enc := cfg.secretKey
	if enc == "" {
		enc = devSecretKey
	}
	return append([]string{enc}, cfg.previous...)
}

// recordedCiphers builds a cipher for every key the config records. A key that
// does not parse is an error, never skipped: skipping it would report its rows
// as unreadable and hide the actual problem.
func recordedCiphers(cfg liteKeyConfig) ([]secrets.Cipher, error) {
	keys := recordedKeyStrings(cfg)
	out := make([]secrets.Cipher, 0, len(keys))
	for i, k := range keys {
		c, err := cipherFromKey(k)
		if err != nil {
			field := keyFieldSecret
			if i > 0 {
				field = fmt.Sprintf("%s entry %d", keyFieldPrevious, i)
			}
			return nil, fmt.Errorf("the %s in config.yaml is not a usable key: %w", field, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// classifyKeyState derives the install state from the config and the values a
// boot just read. It decrypts and never writes.
func classifyKeyState(cfg liteKeyConfig, vals []storage.SecretValue) (keyState, error) {
	recorded, err := recordedCiphers(cfg)
	if err != nil {
		return keyState{}, err
	}
	constant, err := cipherFromKey(devSecretKey)
	if err != nil {
		return keyState{}, err
	}
	st := keyState{legacy: cfg.secretKey == "", pending: cfg.secretKey != "" && len(cfg.previous) > 0}
	for _, v := range vals {
		if _, idx := secrets.OpenWith(recorded, v.Ciphertext); idx >= 0 {
			continue
		}
		if _, cerr := constant.Decrypt(v.Ciphertext); cerr == nil {
			st.stranded++
		} else {
			st.unreadable++
		}
	}
	return st, nil
}

// countOf renders n with the singular or plural noun phrase.
func countOf(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// keyStateMessages is the boot output for a state. Every line states only what
// this boot just scanned in the named datastore: never "re-encrypted", never
// "complete", never that a key is gone (ADR 0065 section 5). A Migrated install
// prints nothing.
func keyStateMessages(st keyState, scanned string) []string {
	var out []string
	switch {
	case st.legacy:
		out = append(out,
			"  WARNING: your connection secrets are encrypted with a key published in this repository,",
			"           which every Lite install shares. Anyone who obtains your datastore can read them.",
			"           Run `dexaflow lite migrate-key` to move them onto a key only this install has;",
			"           Lite must be stopped while it runs.")
	case st.pending:
		out = append(out,
			"  WARNING: a key migration was started and has not finished; both keys are still needed.",
			"           Stop Lite and run `dexaflow lite migrate-key` to finish it.")
	}
	if st.stranded > 0 {
		out = append(out, fmt.Sprintf("  WARNING: %s under the published key and this install cannot read them;",
			countOf(st.stranded, "stored secret is", "stored secrets are")),
			"           stop Lite and run `dexaflow lite migrate-key` to recover them.")
	}
	if st.unreadable > 0 {
		out = append(out, fmt.Sprintf("  WARNING: %s under no key this install records; they cannot be used until",
			countOf(st.unreadable, "stored secret opens", "stored secrets open")),
			"           their key is added to `secret_key_previous` in config.yaml, or they are re-entered.")
	}
	if len(out) > 0 && scanned != "" {
		out = append(out, "           (scanned "+scanned+")")
	}
	return out
}
