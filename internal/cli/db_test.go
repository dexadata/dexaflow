package cli

import (
	"strings"
	"testing"
)

func TestNewDBCommandHasSubcommands(t *testing.T) {
	db := newDBCommand()
	got := map[string]bool{}
	for _, c := range db.Commands() {
		got[c.Name()] = true
	}
	for _, want := range []string{"migrate", "reset"} {
		if !got[want] {
			t.Errorf("db command missing subcommand %q", want)
		}
	}
}

func TestDBResetRequiresYes(t *testing.T) {
	cmd := devTestCmd()
	cmd.SetArgs([]string{"db", "reset"})
	// Wire the db subtree under a throwaway root so we can execute "db reset".
	cmd.AddCommand(newDBCommand())
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("db reset without --yes should refuse with a --yes hint, got %v", err)
	}
}

func TestBrewInstallArgs(t *testing.T) {
	if got := strings.Join(brewInstallArgs("k3d"), " "); got != "install k3d" {
		t.Errorf("brewInstallArgs = %q, want 'install k3d'", got)
	}
}

func TestLiteSubcommandsWired(t *testing.T) {
	want := map[string]bool{"provision": false, "reset-password": false}
	for _, c := range newLiteCommand().Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("`dexaflow lite` should have a %q subcommand", name)
		}
	}
}
