package cli

import (
	"strings"
	"testing"
)

// TestFinalStageUser: the user the final stage of a Dockerfile runs as is
// its last USER, the one of the stage it is built FROM, or unset when the
// base image decides; root, a name and a variable are told apart from a
// numeric non-root id (#1889).
func TestFinalStageUser(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, dockerfile string
		kind             userKind
		value            string
		line             int
	}{
		{"numeric with group", "FROM python:3.12\nUSER 65532:65532\n", userNonRoot, "65532:65532", 2},
		{"numeric", "FROM x\nRUN true\nUSER 1000\n", userNonRoot, "1000", 3},
		{"root by name", "FROM x\nUSER root\n", userRoot, "root", 2},
		{"root by id", "FROM x\nUSER 0:0\n", userRoot, "0:0", 2},
		{"root by padded id", "FROM x\nUSER 000\n", userRoot, "000", 2},
		{"named, lower case instruction", "FROM x\nuser app\n", userNamed, "app", 2},
		{"named with numeric group", "FROM x\nUSER nobody:65534\n", userNamed, "nobody:65534", 2},
		{"unset", "FROM x\nRUN echo hi\n", userUnset, "", 0},
		{"variable", "FROM x\nARG UID=1000\nUSER $UID\n", userUnresolved, "$UID", 3},
		{"braced variable", "FROM x\nUSER ${UID}:0\n", userUnresolved, "${UID}:0", 2},
		{"last USER wins", "FROM x\nUSER 1000\nRUN true\nUSER root\n", userRoot, "root", 4},
		{"inherited from a stage", "FROM x AS build\nUSER 1000\nFROM build\nRUN y\n", userNonRoot, "1000", 2},
		{"stage names ignore case", "FROM x AS Build\nUSER root\nFROM build\n", userRoot, "root", 2},
		{"stage with platform flag", "FROM --platform=$BUILDPLATFORM x AS b\nUSER root\nFROM --platform=linux/amd64 b\n", userRoot, "root", 2},
		{"another stage's USER does not carry", "FROM x\nUSER 1000\nFROM y\nRUN z\n", userUnset, "", 0},
		{"continuation", "FROM x\nUSER \\\n  1000\n", userNonRoot, "1000", 2},
		{"comment", "FROM x\nUSER 1000\n# USER root\n", userNonRoot, "1000", 2},
		{"comment inside a continuation", "FROM x\nUSER \\\n# the id\n  1000\n", userNonRoot, "1000", 2},
		{"indented and windows lines", "FROM x\r\n   USER 1000\r\n", userNonRoot, "1000", 2},
		{"no FROM", "USER 1000\n", userUnset, "", 0},
		{"blank line inside a continuation", "FROM x\nUSER \\\n\n  1000\n", userNonRoot, "1000", 2},
		// A heredoc body is the content of a file or a script, never an
		// instruction, as BuildKit reads it (dockerfile 1.4).
		{"heredoc body is not an instruction", "FROM x\nUSER 1000\nCOPY <<EOF /etc/nginx/nginx.conf\nuser nginx;\nEOF\n", userNonRoot, "1000", 2},
		{"heredoc body FROM starts no stage", "FROM x\nUSER 1000\nRUN <<EOF\nFROM y\nEOF\n", userNonRoot, "1000", 2},
		{"USER after a heredoc", "FROM x\nUSER 1000\nCOPY <<EOF /a\nuser nginx;\nEOF\nUSER root\n", userRoot, "root", 6},
		{"two heredocs on one line", "FROM x\nUSER 1000\nRUN cat <<A - <<B >/f\nuser a\nA\nUSER root\nB\n", userNonRoot, "1000", 2},
		{"heredoc with a file descriptor", "FROM x\nUSER 1000\nRUN cat 0<<EOF\nUSER root\nEOF\n", userNonRoot, "1000", 2},
		{"heredoc on a continued line", "FROM x\nUSER 1000\nRUN \\\n  cat <<EOF\nUSER root\nEOF\n", userNonRoot, "1000", 2},
		{"tab-stripped heredoc", "FROM x\nUSER 1000\nRUN <<-EOF\n\tUSER root\n\tEOF\nUSER 1001\n", userNonRoot, "1001", 6},
		{"indented terminator ends only <<-", "FROM x\nUSER 1000\nRUN <<EOF\n\tEOF\nUSER root\nEOF\n", userNonRoot, "1000", 2},
		{"double-quoted delimiter", "FROM x\nUSER 1000\nCOPY <<\"EOF\" /a\nUSER root\nEOF\nUSER 1001\n", userNonRoot, "1001", 6},
		{"single-quoted delimiter", "FROM x\nUSER 1000\nADD <<'END' /a\nUSER root\nEND\nUSER 1001\n", userNonRoot, "1001", 6},
		{"heredoc with windows lines", "FROM x\r\nUSER 1000\r\nRUN <<EOF\r\nUSER root\r\nEOF\r\nUSER 1001\r\n", userNonRoot, "1001", 6},
		{"heredoc in ONBUILD", "FROM x\nUSER 1000\nONBUILD\tRUN <<EOF\nUSER root\nEOF\n", userNonRoot, "1000", 2},
		{"quoted << is no heredoc", "FROM x\nRUN echo \"<<EOF\"\nUSER root\n", userRoot, "root", 3},
		{"<< with a space is no heredoc", "FROM x\nRUN cat << EOF\nUSER root\n", userRoot, "root", 3},
		{"exec form takes no heredoc", "FROM x\nRUN [\"cat\", \"<<EOF\"]\nUSER root\n", userRoot, "root", 3},
		{"another instruction takes no heredoc", "FROM x\nENV A <<EOF\nUSER root\n", userRoot, "root", 3},
		// The escape parser directive, on the first lines, sets the
		// character that continues a line.
		{"escape directive", "# escape=`\nFROM x\nUSER `\n  root\n", userRoot, "root", 3},
		{"escape directive frees the backslash", "# escape=`\nFROM x\nUSER 1000\nRUN echo \\\nUSER root\n", userRoot, "root", 5},
		{"escape directive with spaces and case", "#  Escape = `\nFROM x\nUSER `\n  1000\n", userNonRoot, "1000", 3},
		{"escape directive after other directives", "# syntax=docker/dockerfile:1\n# escape=`\nFROM x\nUSER `\n  1000\n", userNonRoot, "1000", 4},
		{"escape directive after a comment is a comment", "# a comment\n# escape=`\nFROM x\nUSER \\\n  1000\n", userNonRoot, "1000", 4},
		{"escape directive after a blank line is a comment", "\n# escape=`\nFROM x\nUSER \\\n  1000\n", userNonRoot, "1000", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := finalStageUser(strings.NewReader(tc.dockerfile))
			if err != nil {
				t.Fatal(err)
			}
			if got.kind != tc.kind || got.value != tc.value || got.line != tc.line {
				t.Errorf("finalStageUser = %+v, want kind %v value %q line %d", got, tc.kind, tc.value, tc.line)
			}
		})
	}
}
