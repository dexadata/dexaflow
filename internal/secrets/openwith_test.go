package secrets

import "testing"

func mustAES(t *testing.T, key string) Cipher {
	t.Helper()
	c, err := NewAESGCM([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// OpenWith reports WHICH key opened a value, which is what the key migration
// classifies by: under the encrypting key, under a predecessor, or under none.
func TestOpenWithReportsTheKeyThatOpened(t *testing.T) {
	a := mustAES(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	b := mustAES(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	c := mustAES(t, "cccccccccccccccccccccccccccccccc")
	ct, err := b.Encrypt("hello")
	if err != nil {
		t.Fatal(err)
	}
	plain, idx := OpenWith([]Cipher{a, b, c}, ct)
	if plain != "hello" || idx != 1 {
		t.Errorf("got %q at %d, want hello at 1", plain, idx)
	}
	if _, idx = OpenWith([]Cipher{a, c}, ct); idx != -1 {
		t.Errorf("a value no key opens reported index %d, want -1", idx)
	}
	if _, idx = OpenWith([]Cipher{nil, b}, ct); idx != 1 {
		t.Errorf("a nil entry must be skipped, got %d", idx)
	}
}
