package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestAnnounceReadyInheritedDags covers the visible half of #1104. `dexaflow lite`
// keeps its state under ~/.leoflow/dev and nothing resets it, so a DAG
// registered during a spike weeks ago is still registered: it schedules, it
// fires, it fails, and it fails inside a session that has nothing to do with it.
//
// The banner is where the fix belongs. The leftovers are invisible until one of
// them breaks, and someone who does not know they exist will not go looking for
// a flag to remove them — so the count has to arrive unasked, in the block
// already carrying the URL, the login and the project path.
func TestAnnounceReadyInheritedDags(t *testing.T) {
	t.Run("names the count and the way out when there are leftovers", func(t *testing.T) {
		var out bytes.Buffer
		announceReady(&out, "127.0.0.1", 8088, "admin@leoflow.local", "/ws", 3)
		s := out.String()
		if !strings.Contains(s, "3") {
			t.Errorf("banner must state how many were inherited; got %q", s)
		}
		if !strings.Contains(s, "--fresh") {
			t.Errorf("banner must name the way out; got %q", s)
		}
	})

	t.Run("says nothing when the slate is clean", func(t *testing.T) {
		// A line that always prints stops being read. Zero leftovers is the
		// common case and must add nothing to the block.
		var out bytes.Buffer
		announceReady(&out, "127.0.0.1", 8088, "admin@leoflow.local", "/ws", 0)
		if strings.Contains(out.String(), "--fresh") {
			t.Errorf("no leftovers means no line; got %q", out.String())
		}
	})

	t.Run("stays quiet when the count could not be determined", func(t *testing.T) {
		// A negative count means the lookup failed. Guessing "0" would claim a
		// clean slate we did not verify, and guessing any number would be worse.
		var out bytes.Buffer
		announceReady(&out, "127.0.0.1", 8088, "admin@leoflow.local", "/ws", -1)
		if strings.Contains(out.String(), "--fresh") {
			t.Errorf("an unknown count must not be reported; got %q", out.String())
		}
	})

	t.Run("singular reads correctly", func(t *testing.T) {
		var out bytes.Buffer
		announceReady(&out, "127.0.0.1", 8088, "admin@leoflow.local", "/ws", 1)
		s := out.String()
		if strings.Contains(s, "1 DAGs") {
			t.Errorf("one leftover should not read as plural; got %q", s)
		}
	})

	t.Run("the existing block is unchanged", func(t *testing.T) {
		// The URL, login and project path are what people actually came for.
		var out bytes.Buffer
		announceReady(&out, "127.0.0.1", 8088, "admin@leoflow.local", "/ws", 2)
		for _, want := range []string{"Dexaflow Lite is ready", "login:", "project:"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("banner lost %q: %s", want, out.String())
			}
		}
	})
}
