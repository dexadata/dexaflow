package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

// newPasswordCmd builds a bare command with a --password flag, mirroring the auth
// commands, so resolvePassword's --password/--password-stdin precedence can be
// exercised without a server.
func newPasswordCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("password", "", "")
	return cmd
}

func TestResolvePasswordStdinReadsOneLine(t *testing.T) {
	t.Parallel()
	cmd := newPasswordCmd()
	cmd.SetIn(bytes.NewBufferString("s3cr3t\nignored second line\n"))
	got, err := resolvePassword(cmd, "", true)
	if err != nil {
		t.Fatalf("resolvePassword: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("stdin password = %q, want s3cr3t", got)
	}
}

func TestResolvePasswordStdinConflictsWithExplicitFlag(t *testing.T) {
	t.Parallel()
	cmd := newPasswordCmd()
	if err := cmd.Flags().Set("password", "onflag"); err != nil { // explicit --password
		t.Fatal(err)
	}
	cmd.SetIn(bytes.NewBufferString("fromstdin\n"))
	if _, err := resolvePassword(cmd, "onflag", true); err == nil {
		t.Error("explicit --password + --password-stdin must be a conflict")
	}
}

func TestResolvePasswordEnvDefaultDoesNotConflict(t *testing.T) {
	t.Parallel()
	// The flag carries its LEOFLOW_PASSWORD default but the user did not set it
	// (Changed==false), so --password-stdin wins with no conflict.
	cmd := newPasswordCmd()
	cmd.SetIn(bytes.NewBufferString("fromstdin\n"))
	got, err := resolvePassword(cmd, "envdefault", true)
	if err != nil {
		t.Fatalf("env default must not conflict with --password-stdin: %v", err)
	}
	if got != "fromstdin" {
		t.Errorf("stdin must win over env default; got %q", got)
	}
}

func TestResolvePasswordFlagPassthrough(t *testing.T) {
	t.Parallel()
	cmd := newPasswordCmd()
	got, err := resolvePassword(cmd, "plain", false)
	if err != nil || got != "plain" {
		t.Errorf("no stdin: got (%q, %v), want (plain, nil)", got, err)
	}
}

func TestReadPasswordStdinEmptyErrors(t *testing.T) {
	t.Parallel()
	// Both no bytes at all and a single empty line must error — never a silent
	// empty password.
	for name, in := range map[string]string{"no bytes": "", "empty line": "\n"} {
		if _, err := readPasswordStdin(bytes.NewBufferString(in)); err == nil {
			t.Errorf("%s under --password-stdin must error, not return an empty password", name)
		}
	}
}
