package main

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestEditionBadgeSelection exercises the two edition-badge guards used at
// boot. The silver LITE pill is the Lite edition or the legacy dev auth bypass;
// the gold PRO pill is the Pro edition only. Demo and the unmarked default
// render no pill. Both badges share a switch on cfg.UI.Edition so a single
// regression in either selector would silently leave the SPA shell unbranded.
func TestEditionBadgeSelection(t *testing.T) {
	tests := []struct {
		name      string
		edition   string
		devNoAuth bool
		wantLite  bool
		wantPro   bool
	}{
		{name: "lite shows silver", edition: "lite", wantLite: true},
		{name: "pro shows gold", edition: "pro", wantPro: true},
		{name: "demo shows neither", edition: "demo"},
		{name: "empty edition shows neither", edition: ""},
		{name: "dev-no-auth bypass forces silver", edition: "", devNoAuth: true, wantLite: true},
		{name: "pro edition is exclusive with the lite path", edition: "pro", devNoAuth: false, wantPro: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.ServerConfig{
				UI:   config.UISection{Edition: tc.edition},
				Auth: config.AuthSection{DevNoAuth: tc.devNoAuth},
			}
			if got := showLiteBadge(cfg); got != tc.wantLite {
				t.Errorf("showLiteBadge(edition=%q, devNoAuth=%v) = %v, want %v",
					tc.edition, tc.devNoAuth, got, tc.wantLite)
			}
			if got := showProBadge(cfg); got != tc.wantPro {
				t.Errorf("showProBadge(edition=%q) = %v, want %v",
					tc.edition, got, tc.wantPro)
			}
		})
	}
}
