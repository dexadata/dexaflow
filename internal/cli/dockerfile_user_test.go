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
