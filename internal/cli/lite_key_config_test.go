package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Every spelling YAML allows reads as the key it spells. A line-matching reader
// once read all three as empty, and a rewrite persisted the emptiness (ADR 0065,
// attempt 2).
func TestReadLiteKeyConfigParsesEveryValidSpelling(t *testing.T) {
	cases := map[string]struct {
		body     string
		key      string
		previous []string
	}{
		"plain":             {"secret_key: abc\n", "abc", nil},
		"single quoted":     {"secret_key: 'abc'\n", "abc", nil},
		"double + comment":  {"secret_key: \"abc\" # note\n", "abc", nil},
		"previous plain":    {"secret_key: abc\nsecret_key_previous: def\n", "abc", []string{"def"}},
		"previous quoted":   {"secret_key: abc\nsecret_key_previous: 'def'\n", "abc", []string{"def"}},
		"previous comment":  {"secret_key: abc\nsecret_key_previous: \"def\" # old\n", "abc", []string{"def"}},
		"previous list":     {"secret_key: abc\nsecret_key_previous: \"def, ghi\"\n", "abc", []string{"def", "ghi"}},
		"previous only":     {"secret_key_previous: def\n", "", []string{"def"}},
		"no keys at all":    {"workspace: /w\n", "", nil},
		"empty key is none": {"secret_key: \"\"\n", "", nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := readLiteKeyConfig(writeCfg(t, tc.body))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !got.exists || got.secretKey != tc.key || !reflect.DeepEqual(got.previous, tc.previous) {
				t.Errorf("got %+v, want key %q previous %v", got, tc.key, tc.previous)
			}
		})
	}
}

// The command refuses a file it cannot parse; "empty" is never a fallback
// (gap 8). The same for a file it can parse but would read differently from
// the loader: viper matches keys case-insensitively.
func TestReadLiteKeyConfigRefusesWhatItCannotReadExactly(t *testing.T) {
	for name, body := range map[string]string{
		"unparseable":      "secret_key: [abc\n",
		"not a mapping":    "- a\n- b\n",
		"empty file":       "",
		"case variant key": "Secret_Key: abc\n",
		"duplicate key":    "secret_key: abc\nsecret_key: def\n",
		"non scalar key":   "secret_key:\n  a: b\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := readLiteKeyConfig(writeCfg(t, body)); err == nil {
				t.Errorf("read %q as %+v, want a refusal", body, got)
			}
		})
	}
}

func TestReadLiteKeyConfigReportsAMissingFile(t *testing.T) {
	got, err := readLiteKeyConfig(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil || got.exists {
		t.Errorf("missing file: %+v %v, want exists=false and no error", got, err)
	}
}

// The command promises to carry every other field over unchanged, which a
// regenerated file cannot do (gap 8): the parsed document is edited instead.
func TestSetLiteKeysCarriesEveryOtherFieldOver(t *testing.T) {
	orig := "# Written by `dexaflow setup` (Dexaflow Lite).\n" +
		"parser_cmd: \"env PYTHONPATH=/p python -m leoflow_parser\"\n" +
		"workspace: \"/home/u/dexaflow\"\n" +
		"lite_executor: \"subprocess\"\n" +
		"lite_port: 8088\n" +
		"admin_email: \"admin@leoflow.local\"\n" +
		"admin_password_hash: \"$2a$10$abc\"\n" +
		"jwt_secret: \"jwt\" # keep me\n" +
		"some_future_field: {nested: [1, 2]}\n"
	out, err := setLiteKeys([]byte(orig), "newkey", []string{"oldkey", "constant"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "# keep me") || !strings.Contains(string(out), "# Written by") {
		t.Errorf("comments were dropped:\n%s", out)
	}
	var before, after map[string]any
	if uerr := yaml.Unmarshal([]byte(orig), &before); uerr != nil {
		t.Fatal(uerr)
	}
	if uerr := yaml.Unmarshal(out, &after); uerr != nil {
		t.Fatal(uerr)
	}
	if after["secret_key"] != "newkey" || after["secret_key_previous"] != "oldkey,constant" {
		t.Errorf("keys not recorded: %v", after)
	}
	delete(after, "secret_key")
	delete(after, "secret_key_previous")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("other fields changed:\nbefore %v\nafter  %v", before, after)
	}

	// The drop rewrite removes the predecessor and keeps the rest again.
	dropped, err := setLiteKeys(out, "newkey", nil)
	if err != nil {
		t.Fatal(err)
	}
	var final map[string]any
	if err := yaml.Unmarshal(dropped, &final); err != nil {
		t.Fatal(err)
	}
	if _, ok := final["secret_key_previous"]; ok {
		t.Errorf("predecessor not dropped: %v", final)
	}
	delete(final, "secret_key")
	if !reflect.DeepEqual(before, final) {
		t.Errorf("other fields changed by the drop:\nbefore %v\nafter  %v", before, final)
	}
}

// A value that YAML would read as something other than the string written (a
// key of digits, `true`, `null`) is quoted, so the file reads back byte for
// byte.
func TestSetLiteKeysRoundTripsAwkwardValues(t *testing.T) {
	for _, key := range []string{"0123456789012345678901234567890123456789012345678901234567890123", "true", "null", "a: b", "#x"} {
		out, err := setLiteKeys([]byte("workspace: /w\n"), key, []string{key + "p"})
		if err != nil {
			t.Fatal(err)
		}
		p := writeCfg(t, string(out))
		got, err := readLiteKeyConfig(p)
		if err != nil || got.secretKey != key || !reflect.DeepEqual(got.previous, []string{key + "p"}) {
			t.Errorf("%q round-tripped as %+v (%v)\n%s", key, got, err, out)
		}
	}
}
