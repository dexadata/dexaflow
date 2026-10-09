package domain

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// dockerfileSeparators are the characters that end a Dockerfile line or split a
// word, and that therefore cannot be quoted into safety. BuildKit splits words
// on `[\t\v\f\r ]+` (frontend/dockerfile/parser: reWhitespace) and ends a line
// on the newline, so vertical tab and form feed are separators exactly like a
// space is. Space and tab are NOT in this set because a COPY can quote those;
// the rest cannot appear in a path anyone meant to write.
const dockerfileSeparators = "\n\r\v\f"

// dockerfileLexMeta are the characters COPY's operand lexer rewrites, which the
// JSON form does NOT protect against. After parsing, every COPY operand goes
// through a second pass (instructions.SourcesAndDest.Expand -> shell.Lex
// .ProcessWord) that strips quotes, eats backslashes and expands $VAR. Measured
// against BuildKit v0.28.1, in the JSON form:
//
//	COPY ["d'a't.py", "/home/leoflow/d'a't.py"]  ->  copies dat.py
//	an\alytics -> analytics        $HOME -> the base image's value
//	a"b -> a hard "matching double-quote" build error
//
// So these are REFUSED rather than quoted. Quoting them was the first version of
// this guard and it was wrong: it moved `"` and `\` to a form that does not help
// while leaving `'` and `$` to silently copy a different path.
const dockerfileLexMeta = "'\"\\$"

// CheckDockerfileWord refuses a value the Dockerfile format would split into new
// words or new instructions: a line break, a vertical tab or a form feed.
//
// #1066 gave `dependencies` and `system_packages` a line-break guard because a
// newline ends the RUN instruction. Every other value the generator
// interpolates has the same defect with no shell in sight: a newline in
// `base_image` renders `FROM python:3.11-slim` followed by an attacker's own
// instruction, and the COPY operands behave identically.
//
// The value is named in the error because a stray newline in YAML is invisible
// in the source.
func CheckDockerfileWord(field, v string) error {
	if strings.ContainsAny(v, dockerfileSeparators) {
		return fmt.Errorf(
			"%s %q contains a line break or a vertical-tab/form-feed character, which ends the Dockerfile instruction or splits it into new words; remove it",
			field, v)
	}
	return nil
}

// CheckImageReference refuses a value going into FROM, which has no quoting at
// all. Any whitespace there is read as the `FROM <image> AS <stage>` form, and
// an image reference cannot contain whitespace anyway, so refusing it costs
// nothing.
func CheckImageReference(field, v string) error {
	if err := CheckDockerfileWord(field, v); err != nil {
		return err
	}
	if strings.ContainsAny(v, " \t") {
		return fmt.Errorf(
			"%s %q contains whitespace; an image reference cannot, and FROM has no quoting, so the rest would be read as a stage name",
			field, v)
	}
	return nil
}

// CheckCopySource refuses a COPY source path that no quoting can protect. A
// space or a tab is NOT refused: the renderer quotes those with the JSON form.
func CheckCopySource(field, src string) error {
	if err := CheckDockerfileWord(field, src); err != nil {
		return err
	}
	if i := strings.IndexAny(src, dockerfileLexMeta); i >= 0 {
		return fmt.Errorf(
			"%s %q contains %q, which Docker's COPY operand lexer rewrites (it strips quotes, eats backslashes and expands $VAR) in every form, so the file copied would not be the one named; remove it",
			field, src, src[i:i+1])
	}
	// `<` is a SEPARATE mechanism from the operand lexer, and saying otherwise
	// was worse than saying nothing: COPY is heredoc-capable at the parser
	// level, so `COPY <<EOF` opens a heredoc that swallows every later line of
	// the generated file, including the non-root USER drop, and then fails on
	// the missing terminator. The file is not mis-copied; the build does not
	// start.
	if strings.Contains(src, "<") {
		return fmt.Errorf(
			"%s %q contains `<`, which COPY reads as the start of a heredoc rather than as a path, swallowing the rest of the generated Dockerfile; remove it",
			field, src)
	}
	// A leading `--` is read as a COPY flag (`--from`, `--chown`, `--link`),
	// which eats the operand and leaves Docker to complain that COPY needs two
	// arguments. Naming the entry here is the whole point of validating it.
	if strings.HasPrefix(src, "--") {
		return fmt.Errorf(
			"%s %q starts with `--`, which COPY reads as one of its flags rather than as a path; remove it",
			field, src)
	}
	return nil
}

// CheckRunArgument refuses a RUN argument no shell quoting survives: a line
// break ends the RUN instruction itself, and everything after it becomes a new
// Dockerfile line. Everything else is quoted by the renderer, deliberately,
// because PEP 508 markers legitimately carry `;`, `<` and double quotes (#1064).
func CheckRunArgument(field, w string) error {
	if strings.ContainsAny(w, "\n\r") {
		return fmt.Errorf(
			"%s entry %q contains a line break, which would end the RUN instruction and turn the rest into a new Dockerfile line; remove it",
			field, w)
	}
	return nil
}

// ValidateDockerfileValues refuses every value a generated Dockerfile (or its
// .dockerignore) would interpolate in a form the Dockerfile format or Docker's
// operand lexer would give a meaning the author did not write.
//
// It lives here rather than in the renderer because the renderer only runs under
// `compile --build`: `validate` and plain `compile` used to call such a project
// valid, and the refusal first arrived on the CI runner about to build it
// (#1268). Validate calls it, so every command that reads a dexaflow.yaml gets
// it, and both Dockerfile generators call it too, so a new generator that takes
// a config cannot skip it by forgetting a helper.
//
// Every check runs on the RAW value, under the name the author wrote, before
// any filepath.Clean or filepath.Base: those transforms hide the defect rather
// than solve it ("evil\nstuff/.." cleans to ".").
func (c *LeoflowConfig) ValidateDockerfileValues() error {
	if c == nil {
		return nil
	}
	if c.BaseImage != "" {
		if err := CheckImageReference("base_image", c.BaseImage); err != nil {
			return err
		}
	}
	for _, list := range []struct {
		field  string
		values []string
	}{{"dependencies", c.Dependencies}, {"system_packages", c.SystemPackages}} {
		for _, v := range list.values {
			if err := CheckRunArgument(list.field, v); err != nil {
				return err
			}
		}
	}
	if err := checkCopiedPaths(c); err != nil {
		return err
	}
	// exclude_paths reaches the line-oriented .dockerignore, not a COPY, so
	// only the separators matter there.
	for _, p := range c.ExcludePaths {
		if err := CheckDockerfileWord("exclude_paths", p); err != nil {
			return err
		}
	}
	return nil
}

// checkCopiedPaths checks every value the generated Dockerfile COPYs: the DAG
// source's file name, the dbt project directories and the include_paths
// entries, each in the form the renderer emits it.
func checkCopiedPaths(c *LeoflowConfig) error {
	if c.DagSource != "" {
		if err := copiedPath("dag_source", c.DagSource, filepath.Base(c.DagSource)); err != nil {
			return err
		}
	}
	if c.Dbt != nil && c.Dbt.Project != "" {
		if err := copiedPath("dbt.project", c.Dbt.Project, filepath.Clean(c.Dbt.Project)); err != nil {
			return err
		}
	}
	// Sorted so a config with several bad groups always names the same one.
	for _, name := range slices.Sorted(maps.Keys(c.DbtGroups)) {
		group := c.DbtGroups[name]
		if group == nil || group.Project == "" {
			continue
		}
		field := "dbt_groups." + name + ".project"
		if err := copiedPath(field, group.Project, filepath.Clean(group.Project)); err != nil {
			return err
		}
	}
	for _, raw := range c.IncludePaths {
		p := strings.TrimSpace(raw)
		if p == "" || p == "." {
			// Still checked raw: a LEADING newline would otherwise be trimmed
			// away and never named.
			if err := CheckDockerfileWord("include_paths", raw); err != nil {
				return err
			}
			continue
		}
		if err := copiedPath("include_paths", raw, filepath.Clean(p)); err != nil {
			return err
		}
	}
	return nil
}

// copiedPath checks the raw value for separators, then the rendered COPY
// operand for everything COPY itself would misread.
func copiedPath(field, raw, rendered string) error {
	if err := CheckDockerfileWord(field, raw); err != nil {
		return err
	}
	return CheckCopySource(field, rendered)
}
