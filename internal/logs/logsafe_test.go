package logs

import "testing"

// Object keys carry DAG and run ids a user chose, and store errors repeat them,
// so anything logged from them must not be able to forge a second log line.
func TestLogSafeStripsLineBreaks(t *testing.T) {
	for in, want := range map[string]string{
		"logs/dag/run/1.log":           "logs/dag/run/1.log",
		"logs/evil\nlevel=ERROR msg=x": "logs/evillevel=ERROR msg=x",
		"a\r\nb":                       "ab",
	} {
		if got := logSafe(in); got != want {
			t.Errorf("logSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
