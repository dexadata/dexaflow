package main

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// auth.session_cookie_insecure is the one setting in this PR that hands the
// session token to the network. Boot has to say so in a record a monitoring
// rule can key on, which means the config key and the value as ATTRIBUTES, not
// only inside the prose: an alert written as a substring match on a sentence
// breaks silently the next time the sentence is reworded. Same shape and same
// reason as every other configWarning here.
func TestSessionCookieWarningsNameTheKeyAndTheValue(t *testing.T) {
	t.Run("off is silent", func(t *testing.T) {
		if w := sessionCookieWarnings(config.AuthSection{}); len(w) != 0 {
			t.Errorf("the hardened default warned: %+v", w)
		}
	})
	t.Run("on warns with fields", func(t *testing.T) {
		w := sessionCookieWarnings(config.AuthSection{SessionCookieInsecure: true})
		if len(w) != 1 {
			t.Fatalf("session_cookie_insecure produced %d warnings, want 1", len(w))
		}
		if w[0].Key != "auth.session_cookie_insecure" {
			t.Errorf("config_key = %q, want auth.session_cookie_insecure", w[0].Key)
		}
		if w[0].Value != "true" {
			t.Errorf("value = %q, want true", w[0].Value)
		}
		// The sentence has to be readable on its own in a plain-text log, and has
		// to say what is exposed rather than only that a flag is set.
		for _, want := range []string{"auth.session_cookie_insecure", "Secure", "plain http"} {
			if !strings.Contains(w[0].Msg, want) {
				t.Errorf("the warning never mentions %q: %q", want, w[0].Msg)
			}
		}
	})
}
