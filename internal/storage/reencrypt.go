package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// ReencryptResult counts what one boot-time re-encryption pass did.
type ReencryptResult struct {
	// Migrated is the number of rows rewritten under the primary key.
	Migrated int
	// Skipped is the number of rows a concurrent writer changed between the
	// pass's read and its write; they were left to that writer.
	Skipped int
	// Unreadable is the number of columns no configured key opens.
	Unreadable int
}

// Clean reports whether the pass moved everything it found: nothing skipped
// and nothing unreadable. Only a clean pass completes a rotation.
func (r ReencryptResult) Clean() bool { return r.Skipped == 0 && r.Unreadable == 0 }

// ReencryptSecrets rewrites every stored connection secret that a non-primary
// key opened, so the rotation can finish and the old key be retired.
//
// Without this a rotation never completes: the previous key stays in the read
// set forever. "Set the new key and keep the old one" is not a rotation, it is
// a second key.
//
// This is the Pro boot sweep of ADR 0019. Lite switches it off and migrates only
// through `dexaflow lite migrate-key` (ADR 0065), which runs in one verified
// transaction instead.
//
// A no-op when nothing is stale, so it is safe to run on every boot: with a
// single key every row reports fresh and nothing is written.
//
// A row no configured key can open is LEFT ALONE and reported, both in the
// result and as an error. Its ciphertext is the only copy of a credential, the
// operator may still find the key, and overwriting or deleting it would destroy
// what it protects. Rows that could be migrated are still migrated. A row the
// optimistic guard skipped is counted in the result: it was not moved by this
// pass, so the pass is not complete, whatever else it did.
func (r *Repository) ReencryptSecrets(ctx context.Context) (ReencryptResult, error) {
	var res ReencryptResult
	stale, ok := r.cipher.(secrets.StaleReader)
	if !ok {
		return res, nil
	}
	rows, err := r.q.ListEncryptedConnectionSecrets(ctx)
	if err != nil {
		return res, fmt.Errorf("listing encrypted connections: %w", err)
	}

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
			res.Unreadable = len(unreadable)
			return res, fmt.Errorf("rewriting connection %q: %w", row.ConnID, uerr)
		}
		if n == 0 {
			// Someone rewrote the row between our read and our write. Their value
			// is the current one and is already under the current key; leaving it
			// is the whole point of the guard. It still counts: this pass did not
			// move it, and a later pass has to look at it again.
			res.Skipped++
			continue
		}
		res.Migrated++
	}

	res.Unreadable = len(unreadable)
	if len(unreadable) > 0 {
		return res, fmt.Errorf("re-encrypted %d connection(s); %d column(s) could not be opened by any configured key and were left untouched: %w",
			res.Migrated, len(unreadable), errors.Join(unreadable...))
	}
	return res, nil
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
