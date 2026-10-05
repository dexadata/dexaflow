package cli

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
)

const (
	stOwnKey  = "an-install-own-key-for-tests-32b"
	stPrevKey = "a-hand-set-previous-key-32bytes!"
	stLostKey = "a-key-nobody-recorded-anywhere!!"
)

func sealedValue(t *testing.T, key, label string) storage.SecretValue {
	t.Helper()
	k, err := secrets.ParseKey(key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := secrets.NewAESGCM(k)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := c.Encrypt("v-" + label)
	if err != nil {
		t.Fatal(err)
	}
	return storage.SecretValue{Column: storage.EncryptedColumns()[0], RowID: label, Label: label, Ciphertext: ct}
}

// The boot output of ADR 0065 section 5, state by state. A message may state
// only what this boot just scanned; nothing here may claim a re-encryption, a
// completion or a removed key.
func TestKeyStateMessagesGolden(t *testing.T) {
	const ds = "the managed datastore (~/.dexaflow/pgdata)"
	cases := []struct {
		name  string
		cfg   liteKeyConfig
		vals  []storage.SecretValue
		want  []string // substrings, in order, across the joined output
		empty bool
	}{
		{
			name: "Legacy",
			cfg:  liteKeyConfig{exists: true},
			vals: []storage.SecretValue{sealedValue(t, devSecretKey, "a")},
			want: []string{"published in this repository", "dexaflow lite migrate-key", "Lite must be stopped", ds},
		},
		{
			name: "Legacy with no config file at all",
			cfg:  liteKeyConfig{},
			want: []string{"published in this repository", "dexaflow lite migrate-key"},
		},
		{
			name: "Pending",
			cfg:  liteKeyConfig{exists: true, secretKey: stOwnKey, previous: []string{devSecretKey}},
			vals: []storage.SecretValue{sealedValue(t, devSecretKey, "a"), sealedValue(t, stOwnKey, "b")},
			want: []string{"a key migration was started and has not finished", "both keys are still needed", "dexaflow lite migrate-key", ds},
		},
		{
			name: "Stranded",
			cfg:  liteKeyConfig{exists: true, secretKey: stOwnKey},
			vals: []storage.SecretValue{sealedValue(t, devSecretKey, "a"), sealedValue(t, devSecretKey, "b"), sealedValue(t, stOwnKey, "c")},
			want: []string{"2 stored secrets are under the published key and this install cannot read them", "dexaflow lite migrate-key", ds},
		},
		{
			name: "Unreadable alongside Migrated",
			cfg:  liteKeyConfig{exists: true, secretKey: stOwnKey},
			vals: []storage.SecretValue{sealedValue(t, stLostKey, "a"), sealedValue(t, stOwnKey, "b")},
			want: []string{"1 stored secret opens under no key this install records", "secret_key_previous", ds},
		},
		{
			name: "Unreadable alongside Pending",
			cfg:  liteKeyConfig{exists: true, secretKey: stOwnKey, previous: []string{stPrevKey}},
			vals: []storage.SecretValue{sealedValue(t, stLostKey, "a"), sealedValue(t, stPrevKey, "b")},
			want: []string{"a key migration was started", "1 stored secret opens under no key this install records"},
		},
		{
			name:  "Migrated",
			cfg:   liteKeyConfig{exists: true, secretKey: stOwnKey},
			vals:  []storage.SecretValue{sealedValue(t, stOwnKey, "a")},
			empty: true,
		},
		{
			name:  "Migrated with no secrets",
			cfg:   liteKeyConfig{exists: true, secretKey: stOwnKey},
			empty: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := classifyKeyState(tc.cfg, tc.vals)
			if err != nil {
				t.Fatal(err)
			}
			out := strings.Join(keyStateMessages(st, ds), "\n")
			if tc.empty {
				if out != "" {
					t.Errorf("a migrated install must print nothing, got:\n%s", out)
				}
				return
			}
			rest := out
			for _, w := range tc.want {
				i := strings.Index(rest, w)
				if i < 0 {
					t.Fatalf("missing %q (in order) in:\n%s", w, out)
				}
				rest = rest[i+len(w):]
			}
			for _, banned := range []string{"re-encrypted", "complete", "nothing was changed", "removed", " gone", "on first read"} {
				if strings.Contains(strings.ToLower(out), banned) {
					t.Errorf("boot output claims %q, which this boot did not verify:\n%s", banned, out)
				}
			}
		})
	}
}

// A malformed recorded key is reported, never treated as absent.
func TestClassifyKeyStateRefusesAMalformedKey(t *testing.T) {
	if _, err := classifyKeyState(liteKeyConfig{exists: true, secretKey: "short"}, nil); err == nil {
		t.Error("a key that does not parse must be an error")
	}
}
