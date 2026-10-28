package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// VariableCiphertextPrefix tags a stored variable value as sealed with the
// secret key (#507). The value column holds either a legacy plaintext value or
// this prefix followed by the cipher's output, so the stored form describes
// itself: no schema change, rows written by an older server stay readable, and
// a value an older server writes over a sealed one is plaintext again, which is
// exactly what that server meant.
const VariableCiphertextPrefix = "dexaflow:enc:v1:"

// VariableEncryption is how the server stores Variable values (#507).
type VariableEncryption string

const (
	// VariableEncryptionOff writes plaintext, as every release before it did.
	// It still reads values an earlier "on" sealed, and leaves them sealed.
	VariableEncryptionOff VariableEncryption = "off"
	// VariableEncryptionOn seals every write with the secret key, and the boot
	// sweep seals the plaintext rows already stored.
	VariableEncryptionOn VariableEncryption = "on"
	// VariableEncryptionDecrypt writes plaintext and the boot sweep restores
	// every sealed row to plaintext: the step before rolling back to a release
	// that cannot read sealed values.
	VariableEncryptionDecrypt VariableEncryption = "decrypt"
)

// ParseVariableEncryption reads the variable_encryption setting. Empty is the
// default, off; an unknown value is an error rather than a silent off.
func ParseVariableEncryption(s string) (VariableEncryption, error) {
	switch m := VariableEncryption(strings.ToLower(strings.TrimSpace(s))); m {
	case "", VariableEncryptionOff:
		return VariableEncryptionOff, nil
	case VariableEncryptionOn, VariableEncryptionDecrypt:
		return m, nil
	default:
		return "", fmt.Errorf("unknown variable_encryption %q (want off, on or decrypt)", s)
	}
}

// HasCipher reports whether a secret key is configured.
func (r *Repository) HasCipher() bool { return r.cipher != nil }

// sealVariable returns the stored form of a variable value under the current
// mode. Under "on" it is the tagged ciphertext, and without a key it refuses
// rather than storing plaintext the operator asked to have encrypted (ADR
// 0019). A value that itself starts with the tag is refused in every mode: it
// would read back as ciphertext.
func (r *Repository) sealVariable(plain string) (string, error) {
	if strings.HasPrefix(plain, VariableCiphertextPrefix) {
		return "", domain.Safef(domain.ErrValidation, "a variable value cannot start with %q, which marks an encrypted value", VariableCiphertextPrefix)
	}
	if r.varEncryption != VariableEncryptionOn {
		return plain, nil
	}
	if r.cipher == nil {
		return "", secrets.ErrNoKey
	}
	sealed, err := r.cipher.Encrypt(plain)
	if err != nil {
		return "", fmt.Errorf("encrypting variable: %w", err)
	}
	return VariableCiphertextPrefix + sealed, nil
}

// openVariable returns the plaintext of a stored value and whether it was
// sealed. A legacy plaintext value is returned as is, in every mode. A sealed
// value with no key, or one no configured key opens, is an error: returning
// the ciphertext as the value would hand garbage to a task.
func (r *Repository) openVariable(stored string) (plain string, sealed bool, err error) {
	ct, ok := strings.CutPrefix(stored, VariableCiphertextPrefix)
	if !ok {
		return stored, false, nil
	}
	if r.cipher == nil {
		return "", true, fmt.Errorf("variable is encrypted: %w", secrets.ErrNoKey)
	}
	plain, err = r.cipher.Decrypt(ct)
	if err != nil {
		return "", true, fmt.Errorf("decrypting variable: %w", err)
	}
	return plain, true, nil
}

// toVariable maps a stored row to the domain type, opening a sealed value. A
// sealed value no configured key opens is a domain.ErrValueUnreadable error
// whose message names the variable and the way out; the underlying cause
// stays in the wrap chain for the log.
func (r *Repository) toVariable(key, stored string, desc *string) (domain.Variable, error) {
	plain, sealed, err := r.openVariable(stored)
	if err != nil {
		return domain.Variable{}, fmt.Errorf("%w: %w", domain.Safef(domain.ErrValueUnreadable,
			"variable %q is encrypted with a secret key this server does not have; add that key back as a later secret key entry, or delete the variable and set it again", key), err)
	}
	return domain.Variable{Key: key, Value: plain, Description: strOrEmpty(desc), Encrypted: sealed}, nil
}

// listVariable maps a stored row for a listing. A value no configured key
// opens does not fail the listing: the variable is returned marked Unreadable
// with no value, and a warning is logged, the rule delivery to task pods
// follows (deliverVariable).
func (r *Repository) listVariable(key, stored string, desc *string) domain.Variable {
	v, err := r.toVariable(key, stored, desc)
	if err != nil {
		slog.Warn("listing variable without its value: decrypt failed (key rotated or wrong DEXAFLOW_SECRET_KEY?)",
			"key", key, "error", err)
		return domain.Variable{Key: key, Description: strOrEmpty(desc), Encrypted: true, Unreadable: true}
	}
	return v
}

// deliverVariable opens a value for delivery to a task pod. One value no key
// opens is skipped with a warning, the rule connections follow: the task that
// needs it fails, every other task keeps its variables.
func (r *Repository) deliverVariable(out map[string]string, key, stored string) {
	plain, _, err := r.openVariable(stored)
	if err != nil {
		slog.Warn("skipping variable: decrypt failed (key rotated or wrong DEXAFLOW_SECRET_KEY?)",
			"key", key, "error", err)
		return
	}
	out[key] = plain
}

// VariableSweepResult counts what one variable_encryption boot sweep did.
type VariableSweepResult struct {
	// Migrated is the number of rows rewritten into the configured form.
	Migrated int
	// Skipped is the number of rows a concurrent write changed between the
	// sweep's read and its write; that write's form stands.
	Skipped int
	// Unreadable is the number of sealed rows no configured key opens.
	Unreadable int
}

// Clean reports whether the sweep left nothing behind.
func (r VariableSweepResult) Clean() bool { return r.Skipped == 0 && r.Unreadable == 0 }

// ConvergeVariableEncryption rewrites stored variable values into the form the
// mode asks for: "on" seals every plaintext row, "decrypt" restores every
// sealed row to plaintext, and "off" changes nothing. It is idempotent and safe
// on every boot and from several replicas at once: each rewrite is guarded on
// the value it read. A sealed row no key opens is left untouched and reported,
// because its ciphertext is the only copy of that value.
func (r *Repository) ConvergeVariableEncryption(ctx context.Context) (VariableSweepResult, error) {
	var res VariableSweepResult
	if r.varEncryption != VariableEncryptionOn && r.varEncryption != VariableEncryptionDecrypt {
		return res, nil
	}
	if r.cipher == nil {
		return res, secrets.ErrNoKey
	}
	sealing := r.varEncryption == VariableEncryptionOn
	rows, err := r.q.ListVariablesForEncryptionSweep(ctx, queries.ListVariablesForEncryptionSweepParams{
		Prefix: VariableCiphertextPrefix, Sealed: !sealing,
	})
	if err != nil {
		return res, fmt.Errorf("listing variables: %w", err)
	}
	var unreadable []error
	for _, row := range rows {
		next, cerr := r.convergeVariable(row.Value, sealing)
		if cerr != nil {
			unreadable = append(unreadable, fmt.Errorf("variable %q: %w", row.Key, cerr))
			continue
		}
		n, uerr := r.q.UpdateVariableValueGuarded(ctx, queries.UpdateVariableValueGuardedParams{
			ID: row.ID, Value: next, Expect: row.Value,
		})
		if uerr != nil {
			res.Unreadable = len(unreadable)
			return res, fmt.Errorf("rewriting variable %q: %w", row.Key, uerr)
		}
		if n == 0 {
			res.Skipped++
			continue
		}
		res.Migrated++
	}
	res.Unreadable = len(unreadable)
	if len(unreadable) > 0 {
		return res, fmt.Errorf("%d variable(s) could not be opened by any configured key and were left untouched: %w",
			len(unreadable), errors.Join(unreadable...))
	}
	return res, nil
}

// convergeVariable returns the stored form of one row in the direction of the
// sweep. A plaintext row that starts with the tag cannot exist (writes refuse
// it), so sealing never sees one; a decrypted value that does is left sealed,
// since storing it would read back as ciphertext.
func (r *Repository) convergeVariable(stored string, sealing bool) (string, error) {
	if sealing {
		sealed, err := r.cipher.Encrypt(stored)
		if err != nil {
			return "", err
		}
		return VariableCiphertextPrefix + sealed, nil
	}
	plain, _, err := r.openVariable(stored)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(plain, VariableCiphertextPrefix) {
		return "", errors.New("its value starts with the ciphertext tag and cannot be stored as plaintext")
	}
	return plain, nil
}

// reencryptVariables moves every sealed variable that a non-primary key opened
// onto the primary key: the variable half of the ADR 0019 boot sweep. It runs
// in every mode, since a rotation must finish for sealed rows whether or not
// new writes are sealed.
func (r *Repository) reencryptVariables(ctx context.Context, stale secrets.StaleReader, res *ReencryptResult) ([]error, error) {
	rows, err := r.q.ListVariablesForEncryptionSweep(ctx, queries.ListVariablesForEncryptionSweepParams{
		Prefix: VariableCiphertextPrefix, Sealed: true,
	})
	if err != nil {
		return nil, fmt.Errorf("listing encrypted variables: %w", err)
	}
	var unreadable []error
	for _, row := range rows {
		ct := strings.TrimPrefix(row.Value, VariableCiphertextPrefix)
		out, rewritten, rerr := reencryptOne(stale, &ct)
		if rerr != nil {
			unreadable = append(unreadable, fmt.Errorf("variable %q: %w", row.Key, rerr))
			continue
		}
		if !rewritten {
			continue
		}
		n, uerr := r.q.UpdateVariableValueGuarded(ctx, queries.UpdateVariableValueGuardedParams{
			ID: row.ID, Value: VariableCiphertextPrefix + *out, Expect: row.Value,
		})
		if uerr != nil {
			return unreadable, fmt.Errorf("rewriting variable %q: %w", row.Key, uerr)
		}
		if n == 0 {
			res.Skipped++
			continue
		}
		res.Migrated++
	}
	return unreadable, nil
}
