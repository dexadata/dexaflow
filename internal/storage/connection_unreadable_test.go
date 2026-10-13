package storage

import (
	"errors"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// A listing marks a connection whose extra no configured key opens instead of
// failing the whole page (#1677): the other fields are kept, the extra is
// withheld, never returned as ciphertext. Reading it alone is an
// ErrValueUnreadable error naming the connection and the way out.
func TestListConnectionMarksAnUnreadableExtra(t *testing.T) {
	writer := &Repository{cipher: variableTestCipher(t, 1)}
	sealed, err := writer.cipher.Encrypt(`{"token":"s3cret"}`)
	if err != nil {
		t.Fatal(err)
	}
	port := 5432
	fields := domain.Connection{ConnID: "lost", ConnType: "postgres", Host: "db", Port: &port, Description: "a note"}

	wrong := &Repository{cipher: variableTestCipher(t, 99)}
	got := wrong.listConnection(fields, &sealed)
	if !got.ExtraUnreadable || got.Extra != "" || got.Host != "db" || got.Description != "a note" || got.Port == nil || *got.Port != 5432 {
		t.Errorf("listConnection = %+v, want the fields kept, extra withheld and marked unreadable", got)
	}

	_, err = wrong.toConnection(fields, &sealed)
	var safe *domain.SafeError
	if !errors.Is(err, domain.ErrValueUnreadable) || !errors.As(err, &safe) ||
		!strings.Contains(safe.ClientMessage(), `"lost"`) || !strings.Contains(safe.ClientMessage(), "delete the connection") {
		t.Errorf("toConnection = %v, want a SafeError of class ErrValueUnreadable naming the connection and the way out", err)
	}

	ok := writer.listConnection(fields, &sealed)
	if ok.ExtraUnreadable || ok.Extra != `{"token":"s3cret"}` {
		t.Errorf("readable listConnection = %+v", ok)
	}
	if empty := wrong.listConnection(fields, nil); empty.ExtraUnreadable || empty.Extra != "" {
		t.Errorf("a connection with no extra = %+v, want readable and empty", empty)
	}
}
