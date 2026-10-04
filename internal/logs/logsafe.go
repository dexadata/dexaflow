package logs

import "strings"

// logSafe strips line breaks from a value that reaches a log line. Object keys
// embed user-chosen DAG and run ids, and store errors echo the key back, so
// logging either unfiltered would let a crafted id forge log entries.
func logSafe(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\r", "")
}
