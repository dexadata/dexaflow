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
// A no-op when nothing is stale, so it is safe to run on every boot: with a
// single key every row reports fresh and nothing is written. It returns the
// number of rows rewritten.
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

	var migrated, skipped int
	var unreadable []error
	for _, row := range rows {
		pw, pwStale, pwErr := reencryptOne(stale, row.Password)
		ex, exStale, exErr := reencryptOne(stale, row.Extra)
		// The two columns are independent ciphertexts. A password that can be
		// migrated is migrated even when `extra` cannot be opened, and the other
		// way round: coupling them means one unreadable column pins the whole
		// row, and the predecessor key can never be retired.
		if pwErr != nil {
			unreadable = append(unreadable, fmt.Errorf("connection %q password: %w", row.ConnID, pwErr))
			pw, pwStale = row.Password, false
		}
		if exErr != nil {
			unreadable = append(unreadable, fmt.Errorf("connection %q extra: %w", row.ConnID, exErr))
			ex, exStale = row.Extra, false
		}
		if !pwStale && !exStale {
			continue
		}
		n, uerr := r.q.UpdateConnectionCiphertext(ctx, queries.UpdateConnectionCiphertextParams{
			ID: row.ID, Password: pw, Extra: ex,
			ExpectPassword: row.Password, ExpectExtra: row.Extra,
		})
		if uerr != nil {
			return migrated, fmt.Errorf("rewriting connection %q: %w", row.ConnID, uerr)
		}
		if n == 0 {
			// Someone rewrote the row between our read and our write. Their value
			// is the current one and is already under the current key; leaving it
			// is the whole point of the guard.
			skipped++
			continue
		}
		migrated++
	}
	if skipped > 0 {
		slog.Info("left connections that changed during the rotation to the writer that changed them",
			"skipped", skipped)
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
