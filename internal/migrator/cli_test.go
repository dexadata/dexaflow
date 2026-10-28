package migrator

import (
	"bytes"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func runMain(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Main(t.Context(), args, &out, &errOut, "v0.0.0-test")
	return code, out.String(), errOut.String()
}

// These drive Main, the entry point the image runs, without a database: every
// case fails before a connection is opened, or against a port nothing listens
// on.
func TestMainWithoutADatabase(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		code   int
		stderr []string
		stdout []string
	}{
		{name: "no command", args: nil, code: ExitUsage, stderr: []string{"Commands:"}},
		{name: "help", args: []string{"--help"}, code: 0, stdout: []string{"Commands:", "up [N]", "force V", "goto V", "version"}},
		{name: "version flag", args: []string{"-version"}, code: 0, stdout: []string{"v0.0.0-test"}},
		{name: "unknown flag", args: []string{"-nope", "up"}, code: ExitUsage},
		{name: "unknown command", args: []string{"-database=postgres://u@db/app", "drop"}, code: ExitUsage, stderr: []string{`unknown command "drop"`}},
		{name: "force without version", args: []string{"-database=postgres://u@db/app", "force"}, code: ExitUsage, stderr: []string{"force needs a version"}},
		{name: "force with junk", args: []string{"-database=postgres://u@db/app", "force", "x"}, code: ExitUsage},
		{name: "goto negative", args: []string{"-database=postgres://u@db/app", "goto", "-1"}, code: ExitUsage},
		{name: "up with junk", args: []string{"-database=postgres://u@db/app", "up", "zero"}, code: ExitUsage},
		{name: "empty database url", args: []string{"-database=", "up"}, code: ExitBadDSN, stderr: []string{"database.url is empty"}},
		{name: "wrong scheme", args: []string{"-database=mysql://u:p@db/app", "up"}, code: ExitBadDSN},
		{
			name:   "installation id without its ConfigMap",
			args:   []string{"-database=postgres://u@db/app", "-installation-id=abc", "up"},
			code:   ExitUsage,
			stderr: []string{"-installation-id needs -installation-configmap"},
		},
		{
			name:   "installation ConfigMap without a namespace",
			args:   []string{"-database=postgres://u@db/app", "-installation-configmap=rel-installation", "up"},
			code:   ExitUsage,
			stderr: []string{"-installation-configmap needs -installation-namespace"},
		},
		{
			name:   "installation id that is not a name",
			args:   []string{"-database=postgres://u@db/app", "-installation-configmap=c", "-installation-namespace=n", "-installation-id=not an id", "up"},
			code:   ExitUsage,
			stderr: []string{`installation id "not an id"`},
		},
		{
			name:   "installation outside a cluster",
			args:   []string{"-database=postgres://u:p@127.0.0.1:1/app?sslmode=disable", "-installation-configmap=c", "-installation-namespace=n", "up"},
			code:   ExitUsage,
			stderr: []string{"Kubernetes API"},
		},
		{
			name:   "missing CA file is caught before dialing",
			args:   []string{"-database=postgres://u:p@127.0.0.1:1/app?sslmode=verify-full&sslrootcert=/etc/leoflow/db-ca/absent.crt", "up"},
			code:   ExitMissingCA,
			stderr: []string{"/etc/leoflow/db-ca/absent.crt", "database.caConfigMap"},
		},
		{
			name:   "nothing listening",
			args:   []string{"-database=postgres://u:p@127.0.0.1:1/app?sslmode=disable&connect_timeout=2", "up"},
			code:   ExitTransient,
			stderr: []string{"cannot reach Postgres at 127.0.0.1:1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runMain(t, tc.args...)
			if code != tc.code {
				t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s", code, tc.code, stdout, stderr)
			}
			for _, s := range tc.stderr {
				if !strings.Contains(stderr, s) {
					t.Errorf("stderr lacks %q:\n%s", s, stderr)
				}
			}
			for _, s := range tc.stdout {
				if !strings.Contains(stdout, s) {
					t.Errorf("stdout lacks %q:\n%s", s, stdout)
				}
			}
			if strings.Contains(stderr, ":p@") {
				t.Errorf("stderr echoes the DSN with its password:\n%s", stderr)
			}
		})
	}
}

// The chart's podFailurePolicy fails the Job on the deterministic exit codes.
// The list lives in a template, so this keeps it equal to the Go side: a code
// added here and not there would be retried three times, and a code listed
// there that a transient error returns would stop retries for good.
func TestChartFailsTheJobOnTheDeterministicCodes(t *testing.T) {
	raw, err := os.ReadFile("../../helm/dexaflow/templates/migration-job.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)podFailurePolicy:.*?action: FailJob.*?values: \[([0-9, ]+)\]`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("migration-job.yaml has no podFailurePolicy FailJob rule with an onExitCodes values list")
	}
	fields := strings.Split(string(m[1]), ",")
	chart := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			t.Fatalf("exit code %q: %v", f, err)
		}
		chart = append(chart, n)
	}
	goSide := DeterministicExitCodes()
	slices.Sort(chart)
	slices.Sort(goSide)
	if !slices.Equal(chart, goSide) {
		t.Fatalf("chart fails the Job on %v, the wrapper's deterministic codes are %v", chart, goSide)
	}
}
