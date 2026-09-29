package cli

import (
	"testing"
)

// `leoflow uninstall` keeps the datastore and says so: "a reinstall keeps your
// data". That was free while the encryption key was a constant compiled into
// the binary, because it came back with the reinstall. With a per-install key
// (#486) the key lives only in config.yaml, which uninstall deletes, so the
// preserved datastore comes back unreadable.
//
// Copying the key next to the datastore was tried and rejected: it put the key
// inside the artifact the threat model is about. So the command warns instead,
// and the user decides.
func TestUninstallWarnsWhenItWouldStrandAPreservedDatastore(t *testing.T) {
	cases := []struct {
		name      string
		hasKey    bool
		keepsData bool
		want      bool
	}{
		{"per-install key and data kept: the stranding case", true, true, true},
		{"no key configured: nothing to strand", false, true, false},
		{"data is being purged too: nothing survives to be unreadable", true, false, false},
		{"neither", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strandsDatastoreKey(tc.hasKey, tc.keepsData); got != tc.want {
				t.Errorf("strandsDatastoreKey(hasKey=%v, keepsData=%v) = %v, want %v",
					tc.hasKey, tc.keepsData, got, tc.want)
			}
		})
	}
}
