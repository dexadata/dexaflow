package cli

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

// userKind is what a Dockerfile's USER says about the user a container of
// the image runs as, as the kubelet sees it under runAsNonRoot: only a
// numeric id other than 0 can be checked to be non-root.
type userKind int

const (
	// userUnset: the final stage sets no USER, so the base image decides.
	userUnset userKind = iota
	// userNonRoot: a numeric id other than 0.
	userNonRoot
	// userRoot: root, by name or as id 0.
	userRoot
	// userNamed: a user name, which the kubelet cannot check is not root.
	userNamed
	// userUnresolved: a build variable, which only the build resolves.
	userUnresolved
)

// dockerfileUser is the user the final stage of a Dockerfile runs as: the
// value of its USER and the line it is on, both empty for userUnset.
type dockerfileUser struct {
	kind  userKind
	value string
	line  int
}

// finalStageUser reads the Dockerfile r and returns the user its final
// stage runs as: the last USER of that stage, else the user of the stage it
// is built FROM when that is a stage of the same file, else unset. It reads
// instructions the way the build does: case does not matter, a trailing
// backslash continues a line, and a line starting with # is a comment.
func finalStageUser(r io.Reader) (dockerfileUser, error) {
	stages := map[string]dockerfileUser{}
	var current dockerfileUser
	stage, inStage := "", false
	err := eachInstruction(r, func(line int, keyword, args string) {
		switch keyword {
		case "FROM":
			base, name := fromStage(args)
			current = stages[strings.ToLower(base)]
			stage, inStage = strings.ToLower(name), true
		case "USER":
			if !inStage {
				return
			}
			current = classifyUser(args, line)
		default:
			return
		}
		if stage != "" {
			stages[stage] = current
		}
	})
	if err != nil {
		return dockerfileUser{}, err
	}
	return current, nil
}

// fromStage returns the image or stage a FROM builds on and the name it
// gives the stage ("" for none), past any --flag.
func fromStage(args string) (base, name string) {
	fields := strings.Fields(args)
	for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return "", ""
	}
	if len(fields) >= 3 && strings.EqualFold(fields[1], "AS") {
		return fields[0], fields[2]
	}
	return fields[0], ""
}

// classifyUser reads the value of a USER instruction on line.
func classifyUser(value string, line int) dockerfileUser {
	value = strings.TrimSpace(value)
	u := dockerfileUser{value: value, line: line}
	user, _, _ := strings.Cut(value, ":")
	switch {
	case strings.Contains(value, "$"):
		u.kind = userUnresolved
	case user == "root":
		u.kind = userRoot
	default:
		id, err := strconv.ParseUint(user, 10, 32)
		switch {
		case err != nil:
			u.kind = userNamed
		case id == 0:
			u.kind = userRoot
		default:
			u.kind = userNonRoot
		}
	}
	return u
}

// eachInstruction calls fn with each instruction of the Dockerfile r: the
// line it starts on, its keyword in upper case and its arguments, with
// continuation lines joined.
func eachInstruction(r io.Reader, fn func(line int, keyword, args string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var b strings.Builder
	start, n := 0, 0
	for sc.Scan() {
		n++
		text := strings.TrimSpace(strings.TrimRight(sc.Text(), "\r"))
		if strings.HasPrefix(text, "#") || (text == "" && b.Len() == 0) {
			continue
		}
		if b.Len() == 0 {
			start = n
		}
		if cont, ok := strings.CutSuffix(text, `\`); ok {
			b.WriteString(cont)
			b.WriteByte(' ')
			continue
		}
		b.WriteString(text)
		instruction := strings.TrimSpace(b.String())
		keyword, args := instruction, ""
		if i := strings.IndexFunc(instruction, unicode.IsSpace); i >= 0 {
			keyword, args = instruction[:i], strings.TrimSpace(instruction[i:])
		}
		fn(start, strings.ToUpper(keyword), args)
		b.Reset()
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading the Dockerfile: %w", err)
	}
	return nil
}
