package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/neochaotic/leoflow/internal/secrets"
	"github.com/neochaotic/leoflow/internal/storage/queries"
)

// ReencryptSecrets rewrites every stored connection secret that a non-primary
// key opened, so the rotation can finish and the old key be retired.
//
// Without this a rotation never completes: the previous key stays in the read
// set forever, and for Leoflow Lite that key was published in this repository
// (#486). "Set the new key and keep the old one" is not a rotation, it is a
// second key.
//
// It is a no-op unless the cipher can report which key opened a value, and a
// no-op when nothing is stale, so it is safe to run on every boot. It returns
// the number of rows rewritten.
//
// A row no configured key can open is LEFT ALONE and reported as an error. Its
// ciphertext is the only copy of a credential, the operator may still find the
// key, and overwriting or deleting it would destroy what it protects. Rows that
// could be migrated are still migrated: a single unreadable row must not block
// the rest of the rotation.
func (r *Repository) ReencryptSecrets(ctx context.Context) (int, error) {
	stale, ok := r.cipher.(secrets.StaleReader)
	if !ok {
		return 0, nil
	}
	rows, err := r.q.ListEncryptedConnectionSecrets(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing encrypted connections: %w", err)
	}

	var migrated int
	var unreadable []error
	for _, row := range rows {
		pw, pwStale, pwErr := reencryptOne(stale, row.Password)
		ex, exStale, exErr := reencryptOne(stale, row.Extra)
		if pwErr != nil || exErr != nil {
			unreadable = append(unreadable, fmt.Errorf("connection %q: %w", row.ConnID, errors.Join(pwErr, exErr)))
			continue
		}
		if !pwStale && !exStale {
			continue
		}
		if uerr := r.q.UpdateConnectionCiphertext(ctx, queries.UpdateConnectionCiphertextParams{
			ID: row.ID, Password: pw, Extra: ex,
		}); uerr != nil {
			return migrated, fmt.Errorf("rewriting connection %q: %w", row.ConnID, uerr)
		}
		migrated++
	}

	if len(unreadable) > 0 {
		return migrated, fmt.Errorf("re-encrypted %d connection(s); %d could not be opened by any configured key and were left untouched: %w",
			migrated, len(unreadable), errors.Join(unreadable...))
	}
	if migrated > 0 {
		slog.Info("re-encrypted connection secrets onto the current key", "connections", migrated,
			"note", "the previous key can be removed from LEOFLOW_SECRET_KEY once no other install needs it")
	}
	return migrated, nil
}

// reencryptOne re-seals one column under the primary key when a previous key
// opened it. A nil or empty column is passed through untouched: an absent
// secret is not a stale one.
func reencryptOne(c secrets.StaleReader, enc *string) (sealed *string, rewritten bool, err error) {
	if enc == nil || *enc == "" {
		return enc, false, nil
	}
	plain, stale, derr := c.DecryptStale(*enc)
	if derr != nil {
		return nil, false, derr
	}
	if !stale {
		return enc, false, nil
	}
	out, serr := c.Encrypt(plain)
	if serr != nil {
		return nil, false, serr
	}
	return &out, true, nil
}
