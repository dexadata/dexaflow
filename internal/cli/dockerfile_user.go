package cli

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
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
// continuation lines joined. It reads the file as BuildKit does: the escape
// parser directive on the first lines sets the character that continues a
// line, blank and comment lines inside a continuation are skipped, and the
// body of each heredoc of a RUN, COPY or ADD is content, never an
// instruction.
func eachInstruction(r io.Reader, fn func(line int, keyword, args string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	p := instructionReader{sc: sc, escape: '\\', directives: true}
	for p.next() {
		fn(p.start, p.keyword, p.args)
		for _, h := range heredocsOf(p.keyword, p.args) {
			p.skipHeredoc(h)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading the Dockerfile: %w", err)
	}
	return nil
}

// instructionReader reads the instructions of a Dockerfile one at a time.
type instructionReader struct {
	sc *bufio.Scanner
	// n is the number of the last line read.
	n int
	// escape continues a line at its end: a backslash unless the escape
	// directive says otherwise.
	escape rune
	// directives is true while the lines read may still be parser
	// directives: only the first lines are, up to any other line.
	directives bool
	// The last instruction read: the line it starts on, its keyword in
	// upper case and its arguments.
	start         int
	keyword, args string
}

// line reads the next line, without its line ending.
func (p *instructionReader) line() (string, bool) {
	if !p.sc.Scan() {
		return "", false
	}
	p.n++
	return strings.TrimRight(p.sc.Text(), "\r\n"), true
}

// next reads the next instruction, and reports false at the end of the
// file.
func (p *instructionReader) next() bool {
	var b strings.Builder
	for {
		raw, ok := p.line()
		if !ok {
			return false
		}
		text := strings.TrimSpace(raw)
		if p.directives && p.directive(text) {
			continue
		}
		p.directives = false
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if b.Len() == 0 {
			p.start = p.n
		}
		if cont, ok := strings.CutSuffix(text, string(p.escape)); ok {
			b.WriteString(cont)
			b.WriteByte(' ')
			continue
		}
		b.WriteString(text)
		p.keyword, p.args = splitInstruction(b.String())
		return true
	}
}

// splitInstruction returns the keyword of instruction, in upper case, and
// its arguments.
func splitInstruction(instruction string) (keyword, args string) {
	instruction = strings.TrimSpace(instruction)
	keyword = instruction
	if i := strings.IndexFunc(instruction, unicode.IsSpace); i >= 0 {
		keyword, args = instruction[:i], strings.TrimSpace(instruction[i:])
	}
	return strings.ToUpper(keyword), args
}

// parserDirective is a parser directive line: # name=value.
var parserDirective = regexp.MustCompile(`^#[ \t]*([a-zA-Z][a-zA-Z0-9]*)[ \t]*=[ \t]*(.+?)[ \t]*$`)

// directive reports whether text is a parser directive, and applies the
// escape one. An escape the build refuses is left to the build to report.
func (p *instructionReader) directive(text string) bool {
	m := parserDirective.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	switch strings.ToLower(m[1]) {
	case "escape":
		if m[2] == "`" || m[2] == `\` {
			p.escape = rune(m[2][0])
		}
	case "syntax", "check":
	default:
		return false
	}
	return true
}

// heredoc is a heredoc an instruction opens: the line that ends its body
// and whether leading tabs are stripped before comparing (<<-).
type heredoc struct {
	name  string
	chomp bool
}

// skipHeredoc reads the lines of h's body, up to and with its terminator,
// or to the end of the file.
func (p *instructionReader) skipHeredoc(h heredoc) {
	for {
		raw, ok := p.line()
		if !ok {
			return
		}
		if h.chomp {
			raw = strings.TrimLeft(raw, "\t")
		}
		if raw == h.name {
			return
		}
	}
}

// heredocWord is a word that opens a heredoc: an optional file descriptor,
// << or <<-, and the delimiter.
var heredocWord = regexp.MustCompile(`^\d*<<(-?)([^<]*)$`)

// heredocsOf returns the heredocs the instruction keyword args opens, in
// order: only RUN, COPY and ADD take one (also after ONBUILD). A word of
// the exec (JSON) form is quoted, so it never opens one.
func heredocsOf(keyword, args string) []heredoc {
	if keyword == "ONBUILD" {
		keyword, args = splitInstruction(args)
	}
	switch keyword {
	case "RUN", "COPY", "ADD":
	default:
		return nil
	}
	if !strings.Contains(args, "<<") {
		return nil
	}
	var docs []heredoc
	for _, word := range shellWords(args) {
		m := heredocWord.FindStringSubmatch(word)
		if len(m) < 3 || m[2] == "" {
			continue
		}
		if names := shellWords(m[2]); len(names) == 1 {
			docs = append(docs, heredoc{name: unquote(names[0]), chomp: m[1] == "-"})
		}
	}
	return docs
}

// shellWords splits s into words at blanks outside quotes, keeping the
// quotes and the escapes in each word.
func shellWords(s string) []string {
	var words []string
	var w strings.Builder
	var quote rune
	escaped, inWord := false, false
	for _, c := range s {
		switch {
		case escaped:
			escaped = false
		case c == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case unicode.IsSpace(c):
			if inWord {
				words = append(words, w.String())
				w.Reset()
				inWord = false
			}
			continue
		}
		w.WriteRune(c)
		inWord = true
	}
	if inWord {
		words = append(words, w.String())
	}
	return words
}

// unquote removes the quotes and the escapes of the shell word w.
func unquote(w string) string {
	var b strings.Builder
	var quote rune
	escaped := false
	for _, c := range w {
		switch {
		case escaped:
			escaped = false
		case c == '\\' && quote != '\'':
			escaped = true
			continue
		case quote != 0 && c == quote:
			quote = 0
			continue
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}
