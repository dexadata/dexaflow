package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// loadQueries reads every *.sql file in dir and returns the sqlc queries in it,
// keyed by name. Reading the files the product is generated from, rather than
// copying the SQL here, keeps the experiment measuring what actually ships.
func loadQueries(dir string) (map[string]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .sql files in %s (run from the repository root or pass --queries)", dir)
	}
	all := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f) // #nosec G304 -- local query files chosen by the operator
		if err != nil {
			return nil, err
		}
		for name, q := range parseNamedQueries(string(b)) {
			all[name] = q
		}
	}
	return all, nil
}

// parseNamedQueries splits a sqlc query file on its "-- name: X :kind" headers.
// Each body runs from the line after its header to the next header, trimmed of
// surrounding whitespace and the trailing semicolon.
func parseNamedQueries(src string) map[string]string {
	out := map[string]string{}
	var name string
	var body []string
	flush := func() {
		if name == "" {
			return
		}
		q := strings.TrimSpace(strings.Join(body, "\n"))
		out[name] = strings.TrimSpace(strings.TrimSuffix(q, ";"))
	}
	for _, line := range strings.Split(src, "\n") {
		if rest, ok := strings.CutPrefix(line, "-- name: "); ok {
			flush()
			fields := strings.Fields(rest)
			name, body = "", nil
			if len(fields) > 0 {
				name = fields[0]
			}
			continue
		}
		body = append(body, line)
	}
	flush()
	return out
}

var (
	positionalRe = regexp.MustCompile(`\$(\d+)`)
	sqlcArgRe    = regexp.MustCompile(`sqlc\.n?arg\(\s*'?([A-Za-z_][A-Za-z0-9_]*)'?\s*\)`)
)

// bindSqlcArgs replaces sqlc.arg/sqlc.narg placeholders with numbered
// parameters after the highest $N already in the query, the way sqlc does: the
// same name always gets the same number. It returns the rewritten SQL and the
// names in parameter order, so a caller passes positional args first and then
// one value per returned name.
func bindSqlcArgs(sql string) (bound string, names []string) {
	highest := 0
	for _, m := range positionalRe.FindAllStringSubmatch(sql, -1) {
		var n int
		if _, err := fmt.Sscanf(m[1], "%d", &n); err == nil && n > highest {
			highest = n
		}
	}
	index := map[string]int{}
	bound = sqlcArgRe.ReplaceAllStringFunc(sql, func(m string) string {
		name := sqlcArgRe.FindStringSubmatch(m)[1]
		n, ok := index[name]
		if !ok {
			names = append(names, name)
			n = highest + len(names)
			index[name] = n
		}
		return fmt.Sprintf("$%d", n)
	})
	return bound, names
}

// seqScans walks an EXPLAIN (FORMAT JSON) plan node and returns the distinct
// relations read by a sequential scan, sorted by name.
func seqScans(node map[string]any) []string {
	seen := map[string]bool{}
	var walk func(map[string]any)
	walk = func(n map[string]any) {
		if n["Node Type"] == "Seq Scan" {
			if rel, ok := n["Relation Name"].(string); ok {
				seen[rel] = true
			}
		}
		children, _ := n["Plans"].([]any) //nolint:errcheck // a leaf node has no Plans
		for _, c := range children {
			if cm, ok := c.(map[string]any); ok {
				walk(cm)
			}
		}
	}
	walk(node)
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}
