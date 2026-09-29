package secrets

// StaleReader is a Cipher that can also say whether a value it just decrypted
// was written under an older key, so a caller can re-encrypt it.
type StaleReader interface {
	Cipher
	// DecryptStale decrypts and reports whether the value was read with a
	// fallback key rather than the primary one.
	DecryptStale(ciphertext string) (plaintext string, stale bool, err error)
}

// fallbackCipher writes with one key and reads with several.
//
// Leoflow Lite encrypted every connection secret with a constant compiled into
// this repository, identical on every install on earth (#486). Moving to a
// per-install key cannot orphan what the old one wrote: a rotation that leaves
// existing credentials undecryptable is worse than the published key it
// replaces.
//
// So reads try the primary key first and then each fallback in turn, and a hit
// on a fallback is reported as stale so the caller re-encrypts under the
// primary. Writes only ever use the primary, which is what makes the rotation
// real rather than cosmetic.
//
// Trying keys in sequence is safe here, and only here, because AES-GCM is
// AUTHENTICATED: a wrong key fails to open rather than returning plausible
// garbage. With an unauthenticated mode this design would silently produce
// wrong plaintext.
type fallbackCipher struct {
	primary   Cipher
	fallbacks []Cipher
}

// WithFallback returns a Cipher that writes with primary and reads with primary
// then fallbacks, skipping nil entries.
//
// It always wraps, including when no fallback survives: the result is a
// StaleReader either way, which callers rely on. With an empty fallback list
// every read reports fresh, so a single-key deployment behaves exactly as the
// bare primary does.
func WithFallback(primary Cipher, fallbacks ...Cipher) StaleReader {
	kept := make([]Cipher, 0, len(fallbacks))
	for _, f := range fallbacks {
		if f != nil {
			kept = append(kept, f)
		}
	}
	return &fallbackCipher{primary: primary, fallbacks: kept}
}

// Encrypt seals with the primary key only, which is what makes the rotation
// real: nothing new is ever written under a fallback.
func (c *fallbackCipher) Encrypt(plaintext string) (string, error) {
	return c.primary.Encrypt(plaintext)
}

// Decrypt satisfies Cipher for callers that do not care which key opened the
// value.
func (c *fallbackCipher) Decrypt(ciphertext string) (string, error) {
	plain, _, err := c.DecryptStale(ciphertext)
	return plain, err
}

// DecryptStale tries the primary key, then each fallback, and reports a hit on
// a fallback so the caller can re-encrypt the value under the primary.
func (c *fallbackCipher) DecryptStale(ciphertext string) (plaintext string, stale bool, err error) {
	plain, perr := c.primary.Decrypt(ciphertext)
	if perr == nil {
		return plain, false, nil
	}
	for _, f := range c.fallbacks {
		if plain, ferr := f.Decrypt(ciphertext); ferr == nil {
			return plain, true, nil
		}
	}
	// The primary's error is returned, not the last fallback's: the primary is
	// the key the operator configured, so its failure is the one worth reading.
	return "", false, perr
}
